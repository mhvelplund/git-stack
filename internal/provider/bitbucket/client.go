// Package bitbucket is the Bitbucket Cloud provider. Bitbucket has no gh- or
// glab-like CLI that runs on every platform, so it calls the 2.0 REST API
// directly.
package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mhvelplund/git-stack/pkg/protocol"
)

const (
	// Host is Bitbucket Cloud's git host.
	Host = "bitbucket.org"
	// DefaultBaseURL is Bitbucket Cloud's API root.
	DefaultBaseURL = "https://api.bitbucket.org/2.0"
)

// authHint tells the user how to give the provider credentials.
const authHint = "set BITBUCKET_TOKEN to an access token, or BITBUCKET_EMAIL and BITBUCKET_API_TOKEN to an Atlassian " +
	"API token with Bitbucket scopes, or store a bitbucket.org credential in git's credential helper"

// Auth adds credentials to a request.
type Auth func(req *http.Request)

// GitCredential asks git's credential helpers for the credential of an https
// host, as `git credential fill` does.
type GitCredential func(host string) (user, password string, err error)

// accessTokenUser is the user name git uses with a workspace, project or
// repository access token; the REST API takes such a token only as Bearer.
const accessTokenUser = "x-token-auth"

// Credentials picks the first credential available, in order:
// BITBUCKET_TOKEN (Bearer), BITBUCKET_EMAIL with BITBUCKET_API_TOKEN (Basic),
// then git's credential helpers for bitbucket.org (Basic, or Bearer for an
// access token stored under git's x-token-auth user).
func Credentials(getenv func(string) string, git GitCredential) (Auth, error) {
	if tok := getenv("BITBUCKET_TOKEN"); tok != "" {
		return func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+tok) }, nil
	}
	if email, tok := getenv("BITBUCKET_EMAIL"), getenv("BITBUCKET_API_TOKEN"); email != "" && tok != "" {
		return func(req *http.Request) { req.SetBasicAuth(email, tok) }, nil
	}
	if git != nil {
		if user, pass, err := git(Host); err == nil && user != "" && pass != "" {
			if user == accessTokenUser {
				return func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+pass) }, nil
			}
			return func(req *http.Request) { req.SetBasicAuth(user, pass) }, nil
		}
	}
	return nil, protocol.Errorf(protocol.CodeAuth, "no Bitbucket credentials: %s", authHint)
}

// gitCredentialTimeout bounds `git credential fill`, in case a helper waits
// for input despite being told not to. A variable so tests can shorten it.
var gitCredentialTimeout = 15 * time.Second

// ExecGitCredential runs `git credential fill`, never prompting: with no
// stored credential, git would otherwise ask through GIT_ASKPASS (an editor's
// askpass, say) or Git Credential Manager would open a login window.
func ExecGitCredential(host string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCredentialTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "credential.interactive=never", "-c", "core.askPass=", "credential", "fill")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS=")
	// A helper killed along with git can outlive it and hold its output pipes
	// open; stop waiting for them shortly after the timeout.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", "", err
	}
	var user, pass string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if v, ok := strings.CutPrefix(line, "username="); ok {
			user = v
		} else if v, ok := strings.CutPrefix(line, "password="); ok {
			pass = v
		}
	}
	return user, pass, nil
}

// Client makes Bitbucket API calls for one repository.
type Client struct {
	HTTP      *http.Client
	BaseURL   string
	Auth      Auth
	Workspace string
	Slug      string
}

// Endpoint is the source or destination of a pull request.
type Endpoint struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit struct {
		Hash string `json:"hash"`
	} `json:"commit"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// Reviewer is a pull request reviewer.
type Reviewer struct {
	UUID string `json:"uuid"`
}

// PR is the subset of a Bitbucket pull request that git-stack uses.
type PR struct {
	ID          int        `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       string     `json:"state"` // OPEN, MERGED, DECLINED, SUPERSEDED
	Draft       bool       `json:"draft"`
	Source      Endpoint   `json:"source"`
	Destination Endpoint   `json:"destination"`
	Reviewers   []Reviewer `json:"reviewers"`
	// CloseSourceBranch is the "delete branch after merging" option.
	CloseSourceBranch bool `json:"close_source_branch"`
	Links             struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

// fromFork reports whether the source branch lives in another repository.
func (p *PR) fromFork() bool {
	s, d := p.Source.Repository.FullName, p.Destination.Repository.FullName
	return s != "" && d != "" && !strings.EqualFold(s, d)
}

// repo is the API path of the repository.
func (c *Client) repo() string {
	return "/repositories/" + url.PathEscape(c.Workspace) + "/" + url.PathEscape(c.Slug)
}

// apiError is Bitbucket's error body.
type apiError struct {
	Error struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
	} `json:"error"`
}

// do sends a request with a JSON body (unless nil) and decodes a JSON response
// into out (unless nil). path is relative to BaseURL, or an absolute URL as
// found in "next" and "links". It returns the HTTP status.
func (c *Client) do(method, path string, body, out any) (int, error) {
	target := path
	if !strings.Contains(path, "://") {
		target = strings.TrimSuffix(c.BaseURL, "/") + path
	}
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, target, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Auth != nil {
		c.Auth(req)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, protocol.Errorf(protocol.CodeFailure, "%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, protocol.Errorf(protocol.CodeFailure, "%s %s: %v", method, path, err)
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, statusError(method, path, resp.StatusCode, data)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, protocol.Errorf(protocol.CodeFailure, "%s %s: decoding response: %v", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// statusError maps an HTTP error to a protocol error code.
func statusError(method, path string, status int, data []byte) error {
	msg := http.StatusText(status)
	var e apiError
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
		if e.Error.Detail != "" {
			msg += ": " + e.Error.Detail
		}
	}
	msg = fmt.Sprintf("%s %s: HTTP %d: %s", method, path, status, msg)
	switch status {
	case http.StatusNotFound:
		return protocol.Errorf(protocol.CodeNotFound, "%s", msg)
	case http.StatusUnauthorized, http.StatusForbidden:
		return protocol.Errorf(protocol.CodeAuth, "%s (%s)", msg, authHint)
	}
	return protocol.Errorf(protocol.CodeFailure, "%s", msg)
}

// maxPages bounds how far list follows "next".
const maxPages = 20

// page is one page of a paged response.
type page[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

// list fetches every page of a paged collection, up to maxPages, or only the
// first page if first is set.
func list[T any](c *Client, path string, query url.Values, first bool) ([]T, error) {
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out []T
	for i := 0; i < maxPages && path != ""; i++ {
		var p page[T]
		if _, err := c.do(http.MethodGet, path, nil, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Values...)
		if first {
			break
		}
		path = p.Next
	}
	return out, nil
}

// quote writes s as a BBQL string literal.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (c *Client) prs() string { return c.repo() + "/pullrequests" }

func (c *Client) pr(id int) string { return fmt.Sprintf("%s/%d", c.prs(), id) }

// GetPR fetches a pull request.
func (c *Client) GetPR(id int) (*PR, error) {
	var pr PR
	if _, err := c.do(http.MethodGet, c.pr(id), nil, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// ListPRs returns the pull requests matching the BBQL query q, newest first.
func (c *Client) ListPRs(q string, first bool) ([]*PR, error) {
	return list[*PR](c, c.prs(), url.Values{"q": {q}, "sort": {"-created_on"}, "pagelen": {"50"}}, first)
}

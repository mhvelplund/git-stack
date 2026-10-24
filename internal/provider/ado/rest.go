package ado

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gitlab.com/malephex/git-stack/pkg/protocol"
)

// patEnv holds a personal access token, as for the azure-devops extension.
const patEnv = "AZURE_DEVOPS_EXT_PAT"

// devOpsResource is Azure DevOps' Entra ID resource, for access tokens.
const devOpsResource = "499b84ac-1321-427f-aa17-267ca6975798"

// restAuthHint tells the user how to give the REST calls credentials.
const restAuthHint = "az cannot retarget pull requests or complete them with a rebase, so git-stack calls the REST API " +
	"for that; set " + patEnv + " to a personal access token with Code (read and write) scope, or use an `az login` " +
	"identity that the organization accepts"

// REST calls the Azure DevOps REST API for what az cannot do.
type REST struct {
	HTTP *http.Client
	// Auth returns the Authorization header value.
	Auth func() (string, error)
}

// NewREST authenticates with $AZURE_DEVOPS_EXT_PAT if set, else with an
// access token for the az login.
func NewREST(getenv func(string) string, run Runner) *REST {
	auth := func() (string, error) {
		if pat := getenv(patEnv); pat != "" {
			return "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+pat)), nil
		}
		out, err := run("account", "get-access-token", "--resource", devOpsResource, "--query", "accessToken", "--output", "tsv")
		if err != nil {
			return "", protocol.Errorf(protocol.CodeAuth, "getting an access token: %v (%s)", err, restAuthHint)
		}
		return "Bearer " + strings.TrimSpace(string(out)), nil
	}
	// Without credentials Azure DevOps redirects to a sign-in page; report
	// that rather than follow it.
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &REST{HTTP: hc, Auth: auth}
}

// PatchPR changes a pull request.
func (r *REST) PatchPR(repo Repository, id int, body, out any) error {
	u := fmt.Sprintf("%s/%s/_apis/git/repositories/%s/pullrequests/%d?api-version=%s",
		repo.OrgURL, url.PathEscape(repo.Project), url.PathEscape(repo.Name), id, apiVersion)
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPatch, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	auth, err := r.Auth()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return protocol.Errorf(protocol.CodeFailure, "PATCH pull request %d: %v", id, err)
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(resp.Body)
	if err != nil {
		return protocol.Errorf(protocol.CodeFailure, "PATCH pull request %d: %v", id, err)
	}
	ok := resp.StatusCode < 300 && resp.StatusCode != http.StatusNonAuthoritativeInfo &&
		strings.Contains(resp.Header.Get("Content-Type"), "json")
	if !ok {
		return restError(id, resp.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return protocol.Errorf(protocol.CodeFailure, "PATCH pull request %d: decoding response: %v", id, err)
		}
	}
	return nil
}

// restError maps a failed REST call to a protocol error. A redirect or a
// non-JSON 2xx is Azure DevOps' sign-in page.
func restError(id, status int, data []byte) error {
	var e struct {
		Message string `json:"message"`
	}
	msg := http.StatusText(status)
	if json.Unmarshal(data, &e) == nil && e.Message != "" {
		msg = e.Message
	}
	msg = fmt.Sprintf("PATCH pull request %d: HTTP %d: %s", id, status, msg)
	switch {
	case status == http.StatusNotFound:
		return protocol.Errorf(protocol.CodeNotFound, "%s", msg)
	case status == http.StatusUnauthorized || status == http.StatusForbidden || (status >= 200 && status < 400):
		return protocol.Errorf(protocol.CodeAuth, "%s (%s)", msg, restAuthHint)
	}
	return protocol.Errorf(protocol.CodeFailure, "%s", msg)
}

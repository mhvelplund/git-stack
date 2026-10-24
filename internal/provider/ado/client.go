// Package ado is the Azure DevOps provider. It works through the az CLI and
// its azure-devops extension, except for the two pull request changes az
// cannot make (changing the target branch, and completing with a rebase),
// which go to the REST API directly.
package ado

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"

	"gitlab.com/malephex/git-stack/pkg/protocol"
)

// apiVersion is the Azure DevOps REST API version used for every call.
const apiVersion = "7.1"

// Runner executes az with args and returns stdout.
type Runner func(args ...string) ([]byte, error)

// ExecRunner runs the az binary. It never installs extensions: without the
// azure-devops extension az fails at once, and classify says how to add it.
func ExecRunner(args ...string) ([]byte, error) {
	cmd := exec.Command("az", args...)
	cmd.Env = append(os.Environ(),
		"AZURE_EXTENSION_USE_DYNAMIC_INSTALL=no",
		"AZURE_CORE_ONLY_SHOW_ERRORS=true",
		"AZURE_CORE_NO_COLOR=true",
		"AZURE_CORE_COLLECT_TELEMETRY=false",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("az %s: %s (%w)", strings.Join(args[:min(len(args), 4)], " "), msg, err)
	}
	return stdout.Bytes(), nil
}

// Repository locates a repository: the organization (or collection) URL, the
// project and the repository name.
type Repository struct {
	OrgURL, Project, Name string
}

// ParseRepo finds the repository of a git-stack Repo, whose path has the form
// <collection...>/<project>/_git/<repo>.
func ParseRepo(r protocol.Repo) (Repository, error) {
	segs := strings.Split(strings.Trim(r.Path, "/"), "/")
	for i := 1; i+1 < len(segs); i++ {
		if segs[i] == "_git" && i+2 == len(segs) {
			org := "https://" + r.Host
			if i > 1 {
				org += "/" + strings.Join(segs[:i-1], "/")
			}
			return Repository{OrgURL: org, Project: segs[i-1], Name: segs[i+1]}, nil
		}
	}
	return Repository{}, protocol.Errorf(protocol.CodeFailure, "%q is not an Azure DevOps repository path (<org>/<project>/_git/<repo>)", r.Path)
}

// WebURL is the repository's web page.
func (r Repository) WebURL() string {
	return r.OrgURL + "/" + url.PathEscape(r.Project) + "/_git/" + url.PathEscape(r.Name)
}

// Client makes Azure DevOps calls for one repository.
type Client struct {
	Run  Runner
	Repo Repository
	// REST makes the calls az cannot.
	REST *REST
}

// Ref is a commit reference.
type Ref struct {
	CommitID string `json:"commitId"`
}

// Identity is a user reference.
type Identity struct {
	ID string `json:"id"`
}

// PR is the subset of an Azure DevOps pull request that git-stack uses.
type PR struct {
	ID                    int       `json:"pullRequestId"`
	Title                 string    `json:"title"`
	Description           string    `json:"description"`
	Status                string    `json:"status"` // active, completed, abandoned
	IsDraft               bool      `json:"isDraft"`
	SourceRefName         string    `json:"sourceRefName"`
	TargetRefName         string    `json:"targetRefName"`
	LastMergeSourceCommit *Ref      `json:"lastMergeSourceCommit"`
	AutoCompleteSetBy     *Identity `json:"autoCompleteSetBy"`
	CompletionOptions     *struct {
		DeleteSourceBranch bool `json:"deleteSourceBranch"`
	} `json:"completionOptions"`
	// MergeStatus is notSet, queued, conflicts, succeeded, rejectedByPolicy or failure.
	MergeStatus         string `json:"mergeStatus"`
	MergeFailureMessage string `json:"mergeFailureMessage"`
	// CompletionQueueTime is set once someone asked to complete the PR.
	CompletionQueueTime string          `json:"completionQueueTime"`
	ForkSource          json.RawMessage `json:"forkSource"`
	Repository          struct {
		Name string `json:"name"`
	} `json:"repository"`
}

// fromFork reports whether the source branch lives in a fork.
func (p *PR) fromFork() bool {
	s := strings.TrimSpace(string(p.ForkSource))
	return s != "" && s != "null"
}

// autoComplete reports whether the PR completes by itself once policies pass.
func (p *PR) autoComplete() bool {
	return p.AutoCompleteSetBy != nil && p.AutoCompleteSetBy.ID != "" && p.AutoCompleteSetBy.ID != noIdentity
}

// head is the commit the source branch was at when last evaluated.
func (p *PR) head() string {
	if p.LastMergeSourceCommit == nil {
		return ""
	}
	return p.LastMergeSourceCommit.CommitID
}

// noIdentity clears autoCompleteSetBy.
const noIdentity = "00000000-0000-0000-0000-000000000000"

// branchRef turns a branch name into a ref name.
func branchRef(name string) string { return "refs/heads/" + name }

// branchName strips refs/heads/ from a ref name.
func branchName(ref string) string { return strings.TrimPrefix(ref, "refs/heads/") }

// az runs az with common arguments and decodes JSON output into out.
func (c *Client) az(out any, args ...string) error {
	args = append(args, "--organization", c.Repo.OrgURL, "--output", "json", "--only-show-errors")
	data, err := c.Run(args...)
	if err != nil {
		return classify(err)
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return protocol.Errorf(protocol.CodeFailure, "parse az %s output: %v", strings.Join(args[:min(len(args), 3)], " "), err)
	}
	return nil
}

// textArg passes text to az through a file (az reads "@path" arguments from
// files): titles and Markdown cannot be passed safely as arguments to az.cmd
// on Windows. It returns the argument and a cleanup function.
func textArg(text string) (string, func(), error) {
	f, err := os.CreateTemp("", "git-stack-ado-*.txt")
	if err != nil {
		return "", nil, err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}
	return "@" + f.Name(), func() { os.Remove(f.Name()) }, nil
}

// invoke makes a GET call with `az devops invoke`. Only reads work through
// it: for pull requests it always picks the project-level location, which
// does not accept writes.
func (c *Client) invoke(area, resource string, route map[string]string, query url.Values, out any) error {
	args := []string{"devops", "invoke", "--area", area, "--resource", resource, "--api-version", apiVersion}
	if len(route) > 0 {
		args = append(args, "--route-parameters")
		args = append(args, sortedPairs(route)...)
	}
	if len(query) > 0 {
		q := map[string]string{}
		for k, v := range query {
			q[k] = v[0]
		}
		args = append(args, "--query-parameters")
		args = append(args, sortedPairs(q)...)
	}
	return c.az(out, args...)
}

func sortedPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// classify maps az errors to protocol error codes.
func classify(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "misspelled or not recognized"):
		return protocol.Errorf(protocol.CodeFailure, "%s (the azure-devops extension is missing: run `az extension add --name azure-devops`, then `az devops login`)", msg)
	case strings.Contains(msg, "TF401180") || strings.Contains(msg, "TF401019") || strings.Contains(low, "does not exist") || strings.Contains(low, "not found"):
		return protocol.Errorf(protocol.CodeNotFound, "%s", msg)
	case strings.Contains(msg, "TF400813") || strings.Contains(low, "az login") || strings.Contains(low, "az devops login") ||
		strings.Contains(low, "not authorized") || strings.Contains(low, "unauthorized"):
		return protocol.Errorf(protocol.CodeAuth, "%s (run `az login` or `az devops login`)", msg)
	}
	return protocol.Errorf(protocol.CodeFailure, "%s", msg)
}

// GetPR fetches one pull request.
func (c *Client) GetPR(id int) (*PR, error) {
	var pr PR
	err := c.invoke("git", "pullRequests", map[string]string{"project": c.Repo.Project, "pullRequestId": fmt.Sprint(id)}, nil, &pr)
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

// pageSize and maxPages bound ListPRs.
const (
	pageSize = 100
	maxPages = 10
)

// ListPRs returns this repository's pull requests matching the search
// criteria (e.g. "status", "sourceRefName"), newest first. The list is
// project-wide (see invoke), so other repositories' PRs are dropped here.
func (c *Client) ListPRs(criteria map[string]string) ([]*PR, error) {
	var out []*PR
	for page := 0; page < maxPages; page++ {
		q := url.Values{"$top": {fmt.Sprint(pageSize)}, "$skip": {fmt.Sprint(page * pageSize)}}
		for k, v := range criteria {
			q.Set("searchCriteria."+k, v)
		}
		var resp struct {
			Value []*PR `json:"value"`
		}
		if err := c.invoke("git", "pullRequests", map[string]string{"project": c.Repo.Project}, q, &resp); err != nil {
			return nil, err
		}
		for _, pr := range resp.Value {
			if strings.EqualFold(pr.Repository.Name, c.Repo.Name) {
				out = append(out, pr)
			}
		}
		if len(resp.Value) < pageSize {
			break
		}
	}
	return out, nil
}

// CreatePR opens a pull request.
func (c *Client) CreatePR(n protocol.NewCR) (*PR, error) {
	title, done, err := textArg(n.Title)
	if err != nil {
		return nil, err
	}
	defer done()
	desc, done2, err := textArg(n.Body)
	if err != nil {
		return nil, err
	}
	defer done2()
	var pr PR
	err = c.az(&pr, "repos", "pr", "create", "--project", c.Repo.Project, "--repository", c.Repo.Name,
		"--source-branch", n.Head, "--target-branch", n.Base, "--title", title, "--description", desc,
		"--draft", fmt.Sprint(n.Draft))
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

// PRUpdate lists the changes `az repos pr update` can make; nil fields are
// left alone.
type PRUpdate struct {
	Title, Description  *string
	Draft, AutoComplete *bool
	// Status "completed" completes the PR at its current head; Squash then
	// chooses squashing.
	Status string
	Squash *bool
}

// UpdatePR changes a pull request with `az repos pr update`.
func (c *Client) UpdatePR(id int, u PRUpdate) (*PR, error) {
	args := []string{"repos", "pr", "update", "--id", fmt.Sprint(id)}
	for _, t := range []struct {
		flag string
		text *string
	}{{"--title", u.Title}, {"--description", u.Description}} {
		if t.text == nil {
			continue
		}
		arg, done, err := textArg(*t.text)
		if err != nil {
			return nil, err
		}
		defer done()
		args = append(args, t.flag, arg)
	}
	for _, b := range []struct {
		flag string
		v    *bool
	}{{"--draft", u.Draft}, {"--auto-complete", u.AutoComplete}, {"--squash", u.Squash}} {
		if b.v != nil {
			args = append(args, b.flag, fmt.Sprint(*b.v))
		}
	}
	if u.Status != "" {
		args = append(args, "--status", u.Status, "--delete-source-branch", "false")
	}
	var pr PR
	if err := c.az(&pr, args...); err != nil {
		return nil, err
	}
	return &pr, nil
}

// RepoInfo is the subset of a repository that git-stack uses.
type RepoInfo struct {
	ID            string `json:"id"`
	DefaultBranch string `json:"defaultBranch"`
}

// GetRepo fetches the repository.
func (c *Client) GetRepo() (*RepoInfo, error) {
	var r RepoInfo
	err := c.invoke("git", "repositories", map[string]string{"project": c.Repo.Project, "repositoryId": c.Repo.Name}, nil, &r)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Policy is a branch policy configuration.
type Policy struct {
	IsEnabled  bool `json:"isEnabled"`
	IsBlocking bool `json:"isBlocking"`
	IsDeleted  bool `json:"isDeleted"`
	Type       struct {
		ID string `json:"id"`
	} `json:"type"`
	Settings struct {
		Scope []struct {
			RepositoryID string `json:"repositoryId"`
			RefName      string `json:"refName"`
			MatchKind    string `json:"matchKind"` // exact, prefix, defaultBranch
		} `json:"scope"`
		AllowNoFastForward bool `json:"allowNoFastForward"`
		AllowSquash        bool `json:"allowSquash"`
		AllowRebase        bool `json:"allowRebase"`
		AllowRebaseMerge   bool `json:"allowRebaseMerge"`
	} `json:"settings"`
}

// Policies lists the project's branch policies.
func (c *Client) Policies() ([]*Policy, error) {
	var resp struct {
		Value []*Policy `json:"value"`
	}
	err := c.invoke("policy", "configurations", map[string]string{"project": c.Repo.Project}, nil, &resp)
	return resp.Value, err
}

package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
	graphql "github.com/cli/shurcooL-graphql"

	"gitlab.com/malephex/git-stack/pkg/protocol"
)

// Info describes the GitHub provider.
var Info = protocol.Info{
	Name:         "github",
	Terms:        protocol.Terms{Short: "PR", Long: "pull request", Ref: "#%d"},
	MergeMethods: []string{protocol.MethodMerge, protocol.MethodSquash, protocol.MethodRebase},
	NativeStacks: true,
}

// Plugin is the GitHub provider plugin, for protocol.Serve. opts carries
// test overrides (Transport, AuthToken); the host comes from the repository.
func Plugin(opts api.ClientOptions) protocol.Plugin {
	return protocol.Plugin{
		Info: Info,
		// A GitHub Enterprise Server is recognised when gh is logged in to it.
		Detect: func(repo protocol.Repo) bool {
			return exec.Command("gh", "auth", "status", "--hostname", repo.Host).Run() == nil
		},
		Open: func(repo protocol.Repo) (protocol.Provider, error) {
			owner, name, ok := strings.Cut(repo.Path, "/")
			if !ok || strings.Contains(name, "/") {
				return nil, protocol.Errorf(protocol.CodeFailure, "%q is not an owner/repo path", repo.Path)
			}
			o := opts
			o.Host = repo.Host
			c, err := newClient(o, owner, name)
			if err != nil {
				return nil, protocol.Errorf(protocol.CodeAuth, "%v (run `gh auth login --hostname %s`)", err, repo.Host)
			}
			return &Provider{c: c}, nil
		},
	}
}

func newClient(opts api.ClientOptions, owner, repo string) (*Client, error) {
	if opts.Host == "" {
		opts.Host = "github.com"
	}
	gql, err := api.NewGraphQLClient(opts)
	if err != nil {
		return nil, fmt.Errorf("creating GraphQL client: %w", err)
	}
	rest, err := api.NewRESTClient(opts)
	if err != nil {
		return nil, fmt.Errorf("creating REST client: %w", err)
	}
	return &Client{gql: gql, rest: rest, host: opts.Host, owner: owner, repo: repo, slug: owner + "/" + repo}, nil
}

// Provider adapts Client to protocol.Provider.
type Provider struct {
	c *Client
	// noStacks is set once the Stacks API answered 404: stacked PRs are not
	// enabled here (or the host is a GitHub Enterprise Server without them),
	// so only the description block records the stack.
	noStacks bool
}

var _ protocol.Provider = (*Provider)(nil)

// prNode is the pull request data git-stack needs, as GraphQL returns it.
type prNode struct {
	ID                string            `graphql:"id"`
	Number            int               `graphql:"number"`
	State             string            `graphql:"state"` // OPEN, CLOSED, MERGED
	URL               string            `graphql:"url"`
	Title             string            `graphql:"title"`
	Body              string            `graphql:"body"`
	HeadRefName       string            `graphql:"headRefName"`
	BaseRefName       string            `graphql:"baseRefName"`
	HeadRefOid        string            `graphql:"headRefOid"`
	IsDraft           bool              `graphql:"isDraft"`
	IsCrossRepository bool              `graphql:"isCrossRepository"`
	Mergeable         string            `graphql:"mergeable"` // MERGEABLE, CONFLICTING, UNKNOWN
	MergeQueueEntry   *MergeQueueEntry  `graphql:"mergeQueueEntry"`
	AutoMergeRequest  *AutoMergeRequest `graphql:"autoMergeRequest"`
}

func (n *prNode) cr() *protocol.CR {
	state := protocol.StateOpen
	switch n.State {
	case "CLOSED":
		state = protocol.StateClosed
	case "MERGED":
		state = protocol.StateMerged
	}
	return &protocol.CR{
		Number: n.Number, Title: n.Title, Body: n.Body, State: state, Draft: n.IsDraft,
		Head: n.HeadRefName, Base: n.BaseRefName, URL: n.URL, HeadSHA: n.HeadRefOid,
		// A queued PR merges by itself, like one set to auto-merge.
		AutoMerge: n.AutoMergeRequest != nil || (n.MergeQueueEntry != nil && n.MergeQueueEntry.ID != ""),
		Checking:  n.State == "OPEN" && n.Mergeable == "UNKNOWN",
		FromFork:  n.IsCrossRepository,
	}
}

func (p *Provider) vars(extra map[string]any) map[string]any {
	v := map[string]any{"owner": graphql.String(p.c.owner), "name": graphql.String(p.c.repo)}
	for k, x := range extra {
		v[k] = x
	}
	return v
}

func (p *Provider) node(number int) (*prNode, error) {
	n, err := toGraphQLInt(number)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeFailure, "%v", err)
	}
	var q struct {
		Repository struct {
			PullRequest *prNode `graphql:"pullRequest(number: $number)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	if err := p.c.gql.Query("GitStackPR", &q, p.vars(map[string]any{"number": n})); err != nil {
		return nil, classify(fmt.Errorf("querying #%d: %w", number, err))
	}
	if q.Repository.PullRequest == nil {
		return nil, protocol.Errorf(protocol.CodeNotFound, "#%d not found", number)
	}
	return q.Repository.PullRequest, nil
}

// GetCR fetches a pull request.
func (p *Provider) GetCR(number int) (*protocol.CR, error) {
	n, err := p.node(number)
	if err != nil {
		return nil, err
	}
	return n.cr(), nil
}

func (p *Provider) list(filter string, vars map[string]any) ([]*prNode, error) {
	// The filter varies, so the query is written out rather than derived from a struct.
	query := `query($owner:String!,$name:String!` + varDecls(vars) + `){repository(owner:$owner,name:$name){pullRequests(` + filter + `){nodes{` + prFields + `}}}}`
	var resp struct {
		Repository struct {
			PullRequests struct {
				Nodes []*prJSON `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	}
	all := map[string]any{"owner": p.c.owner, "name": p.c.repo}
	for k, v := range vars {
		all[k] = v
	}
	if err := p.c.gql.Do(query, all, &resp); err != nil {
		return nil, classify(err)
	}
	out := make([]*prNode, len(resp.Repository.PullRequests.Nodes))
	for i, n := range resp.Repository.PullRequests.Nodes {
		out[i] = n.node()
	}
	return out, nil
}

// FindCRForBranch returns the open PR whose head is branch, else the most
// recently merged one, else nil. PRs from forks are ignored.
func (p *Provider) FindCRForBranch(branch string) (*protocol.CR, error) {
	nodes, err := p.list("headRefName:$head,states:[OPEN,MERGED],first:20,orderBy:{field:CREATED_AT,direction:DESC}", map[string]any{"head": branch})
	if err != nil {
		return nil, err
	}
	for _, want := range []string{"OPEN", "MERGED"} {
		for _, n := range nodes {
			if n.State == want && !n.IsCrossRepository {
				return n.cr(), nil
			}
		}
	}
	return nil, nil
}

// SearchCRs returns open PRs whose body contains text. GitHub's search does
// not reliably index text in HTML comments, so open PRs are filtered here.
func (p *Provider) SearchCRs(text string) ([]*protocol.CR, error) {
	nodes, err := p.list("states:[OPEN],first:100,orderBy:{field:UPDATED_AT,direction:DESC}", nil)
	if err != nil {
		return nil, err
	}
	var out []*protocol.CR
	for _, n := range nodes {
		if strings.Contains(n.Body, text) {
			out = append(out, n.cr())
		}
	}
	return out, nil
}

// ListCRsTargeting returns open PRs, not from forks, whose base is branch.
func (p *Provider) ListCRsTargeting(branch string) ([]*protocol.CR, error) {
	nodes, err := p.list("baseRefName:$base,states:[OPEN],first:100", map[string]any{"base": branch})
	if err != nil {
		return nil, err
	}
	var out []*protocol.CR
	for _, n := range nodes {
		if !n.IsCrossRepository {
			out = append(out, n.cr())
		}
	}
	return out, nil
}

// CreateCR opens a pull request.
func (p *Provider) CreateCR(n protocol.NewCR) (*protocol.CR, error) {
	pr, err := p.c.CreatePR(n.Base, n.Head, n.Title, n.Body, n.Draft)
	if err != nil {
		return nil, classify(err)
	}
	return p.GetCR(pr.Number)
}

// UpdateCR changes the base, title or body.
func (p *Provider) UpdateCR(number int, u protocol.Update) (*protocol.CR, error) {
	fields := map[string]string{}
	if u.Base != nil {
		fields["base"] = *u.Base
	}
	if u.Title != nil {
		fields["title"] = *u.Title
	}
	if u.Body != nil {
		fields["body"] = *u.Body
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", p.c.owner, p.c.repo, number)
	if err := p.c.rest.Patch(path, bytes.NewReader(body), nil); err != nil {
		return nil, classify(err)
	}
	return p.GetCR(number)
}

// MarkReady takes a pull request out of draft.
func (p *Provider) MarkReady(number int) (*protocol.CR, error) {
	n, err := p.node(number)
	if err != nil {
		return nil, err
	}
	if n.IsDraft {
		if err := p.c.MarkPRReadyForReview(n.ID); err != nil {
			return nil, classify(err)
		}
	}
	return p.GetCR(number)
}

// CancelAutoMerge turns auto-merge off.
func (p *Provider) CancelAutoMerge(number int) error {
	n, err := p.node(number)
	if err != nil {
		return err
	}
	if n.AutoMergeRequest == nil {
		return nil
	}
	return classify(p.c.DisableAutoMerge(n.ID))
}

// Merge merges the pull request at sha. When GitHub refuses a direct merge
// (checks pending, reviews required, merge queue), it enables auto-merge
// instead, so the PR merges, or joins the queue, once the rules are met.
func (p *Provider) Merge(number int, method, sha string) (*protocol.CR, error) {
	if method == "" {
		method = protocol.MethodMerge
	}
	body, err := json.Marshal(map[string]string{"merge_method": method, "sha": sha})
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/merge", p.c.owner, p.c.repo, number)
	mergeErr := p.c.rest.Put(path, bytes.NewReader(body), nil)
	if mergeErr == nil {
		return p.GetCR(number)
	}
	var httpErr *api.HTTPError
	if !errors.As(mergeErr, &httpErr) || (httpErr.StatusCode != http.StatusMethodNotAllowed && httpErr.StatusCode != http.StatusConflict) {
		return nil, classify(mergeErr)
	}
	if httpErr.StatusCode == http.StatusConflict {
		return nil, protocol.Errorf(protocol.CodeFailure, "#%d moved on GitHub since it was pushed: %v", number, mergeErr)
	}
	n, err := p.node(number)
	if err != nil {
		return nil, err
	}
	if err := p.enableAutoMerge(n.ID, method, sha); err != nil {
		cr := n.cr()
		cr.MergeError = fmt.Sprintf("%v; enabling auto-merge also failed: %v", mergeErr, err)
		return cr, nil
	}
	return p.GetCR(number)
}

func (p *Provider) enableAutoMerge(id, method, sha string) error {
	var m struct {
		EnablePullRequestAutoMerge struct {
			PullRequest struct {
				ID string
			}
		} `graphql:"enablePullRequestAutoMerge(input: $input)"`
	}
	type EnablePullRequestAutoMergeInput struct {
		PullRequestID   string `json:"pullRequestId"`
		MergeMethod     string `json:"mergeMethod"`
		ExpectedHeadOid string `json:"expectedHeadOid,omitempty"`
	}
	vars := map[string]any{"input": EnablePullRequestAutoMergeInput{
		PullRequestID: id, MergeMethod: strings.ToUpper(method), ExpectedHeadOid: sha,
	}}
	return p.c.gql.Mutate("EnablePullRequestAutoMerge", &m, vars)
}

// GetBranch fetches branch protection and whether name is the default branch.
func (p *Provider) GetBranch(name string) (*protocol.Branch, error) {
	var b struct {
		Protected bool `json:"protected"`
	}
	if err := p.c.rest.Get(fmt.Sprintf("repos/%s/%s/branches/%s", p.c.owner, p.c.repo, name), &b); err != nil {
		return nil, classify(err)
	}
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := p.c.rest.Get(fmt.Sprintf("repos/%s/%s", p.c.owner, p.c.repo), &r); err != nil {
		return nil, classify(err)
	}
	return &protocol.Branch{Protected: b.Protected, Default: r.DefaultBranch == name}, nil
}

// GetRepo returns the viewer's default merge method for the repository.
func (p *Provider) GetRepo() (*protocol.RepoSettings, error) {
	cfg, err := p.c.RepoMergeConfig()
	if err != nil {
		return nil, classify(err)
	}
	method := cfg.DefaultMethod
	if method == "" || !cfg.Allows(method) {
		if allowed := cfg.AllowedMethods(); len(allowed) > 0 {
			method = allowed[0]
		}
	}
	return &protocol.RepoSettings{DefaultMergeMethod: method}, nil
}

// SyncStack makes GitHub's native stack hold exactly numbers (the open PRs,
// bottom first). A stack needs at least two PRs; an existing stack whose open
// PRs are a prefix of numbers is extended, any other is replaced.
func (p *Provider) SyncStack(numbers []int) error {
	if p.noStacks || len(numbers) == 0 {
		return nil
	}
	rs, err := p.c.FindStackForPR(numbers[0])
	if p.stacksUnavailable(err) {
		return nil
	}
	if err != nil {
		return classify(err)
	}
	if rs != nil {
		var open []int
		for _, pr := range rs.PRDetails {
			if !pr.IsMerged() {
				open = append(open, pr.Number)
			}
		}
		if isPrefix(open, numbers) {
			if len(open) == len(numbers) {
				return nil
			}
			_, err := p.c.AddToStack(rs.Number, numbers[len(open):])
			return classify(err)
		}
		if _, _, err := p.c.Unstack(rs.Number); err != nil {
			return classify(err)
		}
	}
	if len(numbers) < 2 {
		return nil
	}
	_, err = p.c.CreateStack(numbers)
	if p.stacksUnavailable(err) {
		return nil
	}
	return classify(err)
}

// Unstack dissolves the native stack that holds the first of numbers.
func (p *Provider) Unstack(numbers []int) error {
	if p.noStacks || len(numbers) == 0 {
		return nil
	}
	rs, err := p.c.FindStackForPR(numbers[0])
	if p.stacksUnavailable(err) || (err == nil && rs == nil) {
		return nil
	}
	if err != nil {
		return classify(err)
	}
	_, _, err = p.c.Unstack(rs.Number)
	return classify(err)
}

// stacksUnavailable records a 404 from the Stacks API.
func (p *Provider) stacksUnavailable(err error) bool {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
		p.noStacks = true
		return true
	}
	return false
}

func isPrefix(p, s []int) bool {
	if len(p) > len(s) {
		return false
	}
	for i := range p {
		if p[i] != s[i] {
			return false
		}
	}
	return true
}

// classify maps GitHub errors to protocol error codes.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusNotFound:
			return protocol.Errorf(protocol.CodeNotFound, "%v", err)
		case http.StatusUnauthorized, http.StatusForbidden:
			return protocol.Errorf(protocol.CodeAuth, "%v", err)
		}
	}
	var gqlErr *api.GraphQLError
	if errors.As(err, &gqlErr) && gqlErr.Match("NOT_FOUND", "") {
		return protocol.Errorf(protocol.CodeNotFound, "%v", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "authentication token not found") || strings.Contains(msg, "gh auth login") {
		return protocol.Errorf(protocol.CodeAuth, "%s (run `gh auth login`)", msg)
	}
	return protocol.Errorf(protocol.CodeFailure, "%s", msg)
}

package bitbucket

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mhvelplund/git-stack/pkg/protocol"
)

// Info describes the Bitbucket provider. Bitbucket has no short syntax for
// pull requests in Markdown (#12 links an issue), so CRs are linked by URL,
// and it may escape raw HTML, so the block hides in link references.
var Info = protocol.Info{
	Name:         "bitbucket",
	Terms:        protocol.Terms{Short: "PR", Long: "pull request"},
	BlockStyle:   protocol.BlockLinkRef,
	MergeMethods: []string{protocol.MethodMerge, protocol.MethodSquash, protocol.MethodRebase},
}

// Options configures the plugin; the zero value talks to Bitbucket Cloud
// with credentials from the environment or git.
type Options struct {
	BaseURL       string
	HTTP          *http.Client
	Getenv        func(string) string
	GitCredential GitCredential
	// Sleep waits between polls of an asynchronous merge.
	Sleep func(time.Duration)
}

// Plugin is the Bitbucket Cloud provider plugin, for protocol.Serve. It has
// no Detect: bitbucket.org is a known host, and Data Center is not supported.
func Plugin(opts Options) protocol.Plugin {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.GitCredential == nil {
		opts.GitCredential = ExecGitCredential
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	return protocol.Plugin{
		Info: Info,
		Open: func(repo protocol.Repo) (protocol.Provider, error) {
			ws, slug, ok := strings.Cut(repo.Path, "/")
			if !ok || strings.Contains(slug, "/") {
				return nil, protocol.Errorf(protocol.CodeFailure, "%q is not a workspace/repo path", repo.Path)
			}
			auth, err := Credentials(opts.Getenv, opts.GitCredential)
			if err != nil {
				return nil, err
			}
			c := &Client{HTTP: opts.HTTP, BaseURL: opts.BaseURL, Auth: auth, Workspace: ws, Slug: slug}
			return &Provider{C: c, sleep: opts.Sleep}, nil
		},
	}
}

// Provider adapts Client to protocol.Provider.
type Provider struct {
	C     *Client
	sleep func(time.Duration)
}

var _ protocol.Provider = (*Provider)(nil)

// ToCR converts a Bitbucket pull request. HeadSHA is Bitbucket's abbreviated
// (12 character) hash.
func ToCR(p *PR) *protocol.CR {
	if p == nil {
		return nil
	}
	state := protocol.StateClosed // DECLINED, SUPERSEDED
	switch p.State {
	case "OPEN":
		state = protocol.StateOpen
	case "MERGED":
		state = protocol.StateMerged
	}
	return &protocol.CR{
		Number: p.ID, Title: p.Title, Body: p.Description, State: state, Draft: p.Draft,
		Head: p.Source.Branch.Name, Base: p.Destination.Branch.Name, URL: p.Links.HTML.Href,
		HeadSHA: p.Source.Commit.Hash, DeleteHead: p.CloseSourceBranch, FromFork: p.fromFork(),
	}
}

func toCR(p *PR, err error) (*protocol.CR, error) {
	if err != nil {
		return nil, err
	}
	return ToCR(p), nil
}

// GetCR fetches a pull request.
func (p *Provider) GetCR(n int) (*protocol.CR, error) { return toCR(p.C.GetPR(n)) }

// FindCRForBranch returns the open PR whose head is branch, else the most
// recently merged one, else nil. PRs from forks are ignored.
func (p *Provider) FindCRForBranch(branch string) (*protocol.CR, error) {
	prs, err := p.C.ListPRs(`source.branch.name = `+quote(branch)+` AND (state = "OPEN" OR state = "MERGED")`, true)
	if err != nil {
		return nil, err
	}
	for _, want := range []string{"OPEN", "MERGED"} {
		for _, pr := range prs {
			if pr.State == want && !pr.fromFork() {
				return ToCR(pr), nil
			}
		}
	}
	return nil, nil
}

// SearchCRs returns open PRs whose description contains text. BBQL's ~ is
// case-insensitive, so matches are checked again here.
func (p *Provider) SearchCRs(text string) ([]*protocol.CR, error) {
	prs, err := p.C.ListPRs(`state = "OPEN" AND description ~ `+quote(text), false)
	if err != nil {
		return nil, err
	}
	var out []*protocol.CR
	for _, pr := range prs {
		if strings.Contains(pr.Description, text) {
			out = append(out, ToCR(pr))
		}
	}
	return out, nil
}

// ListCRsTargeting returns open PRs, not from forks, whose base is branch.
func (p *Provider) ListCRsTargeting(branch string) ([]*protocol.CR, error) {
	prs, err := p.C.ListPRs(`destination.branch.name = `+quote(branch)+` AND state = "OPEN"`, false)
	if err != nil {
		return nil, err
	}
	var out []*protocol.CR
	for _, pr := range prs {
		if !pr.fromFork() {
			out = append(out, ToCR(pr))
		}
	}
	return out, nil
}

func branchRef(name string) map[string]any {
	return map[string]any{"branch": map[string]string{"name": name}}
}

// CreateCR opens a pull request.
func (p *Provider) CreateCR(n protocol.NewCR) (*protocol.CR, error) {
	body := map[string]any{
		"title": n.Title, "description": n.Body, "draft": n.Draft, "close_source_branch": false,
		"source": branchRef(n.Head), "destination": branchRef(n.Base),
	}
	var pr PR
	if _, err := p.C.do(http.MethodPost, p.C.prs(), body, &pr); err != nil {
		return nil, err
	}
	return ToCR(&pr), nil
}

// put changes fields of a pull request. Bitbucket's PUT wants the title and
// drops reviewers that are left out, so both are sent along.
func (p *Provider) put(n int, fields map[string]any) (*protocol.CR, error) {
	cur, err := p.C.GetPR(n)
	if err != nil {
		return nil, err
	}
	reviewers := make([]Reviewer, 0, len(cur.Reviewers))
	for _, r := range cur.Reviewers {
		reviewers = append(reviewers, Reviewer{UUID: r.UUID})
	}
	body := map[string]any{"title": cur.Title, "reviewers": reviewers}
	for k, v := range fields {
		body[k] = v
	}
	var pr PR
	if _, err := p.C.do(http.MethodPut, p.C.pr(n), body, &pr); err != nil {
		return nil, err
	}
	return ToCR(&pr), nil
}

// UpdateCR changes the base, title or description.
func (p *Provider) UpdateCR(n int, u protocol.Update) (*protocol.CR, error) {
	fields := map[string]any{}
	if u.Base != nil {
		fields["destination"] = branchRef(*u.Base)
	}
	if u.Title != nil {
		fields["title"] = *u.Title
	}
	if u.Body != nil {
		fields["description"] = *u.Body
	}
	return p.put(n, fields)
}

// MarkReady takes a pull request out of draft.
func (p *Provider) MarkReady(n int) (*protocol.CR, error) {
	return p.put(n, map[string]any{"draft": false})
}

// CancelAutoMerge does nothing: Bitbucket Cloud has no auto-merge to cancel.
func (p *Provider) CancelAutoMerge(int) error { return nil }

// mergeStrategies maps git-stack's merge methods to Bitbucket's.
var mergeStrategies = map[string]string{
	protocol.MethodMerge:  "merge_commit",
	protocol.MethodSquash: "squash",
	protocol.MethodRebase: "fast_forward",
}

// mergeTask is the status of an asynchronous merge.
type mergeTask struct {
	TaskStatus  string `json:"task_status"` // PENDING, SUCCESS
	MergeResult *PR    `json:"merge_result"`
	Links       struct {
		Self struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
}

const (
	mergePollInterval = 2 * time.Second
	mergePolls        = 90 // three minutes
)

// Merge merges the pull request at once. Bitbucket cannot pin the head
// commit, so the PR is checked to be at sha first. A merge that Bitbucket
// runs asynchronously is waited for.
func (p *Provider) Merge(n int, method, sha string) (*protocol.CR, error) {
	if method == "" {
		method = protocol.MethodMerge
	}
	strategy, ok := mergeStrategies[method]
	if !ok {
		return nil, protocol.Errorf(protocol.CodeUnsupported, "merge method %q is not supported", method)
	}
	cur, err := p.C.GetPR(n)
	if err != nil {
		return nil, err
	}
	if head := cur.Source.Commit.Hash; sha != "" && head != "" && !strings.HasPrefix(sha, head) {
		return nil, protocol.Errorf(protocol.CodeFailure, "PR %d moved on Bitbucket since it was pushed (head %s, expected %s)", n, head, sha)
	}
	var raw json.RawMessage
	status, err := p.C.do(http.MethodPost, p.C.pr(n)+"/merge", map[string]any{"merge_strategy": strategy, "close_source_branch": false}, &raw)
	if err != nil {
		return nil, err
	}
	if status != http.StatusAccepted {
		return p.GetCR(n)
	}
	var task mergeTask
	if err := json.Unmarshal(raw, &task); err != nil {
		return nil, protocol.Errorf(protocol.CodeFailure, "decoding merge task of PR %d: %v", n, err)
	}
	href := task.Links.Self.Href
	for i := 0; task.TaskStatus == "PENDING" && href != ""; i++ {
		if i == mergePolls {
			return nil, protocol.Errorf(protocol.CodeFailure, "timed out waiting for Bitbucket to merge PR %d", n)
		}
		p.sleep(mergePollInterval)
		task = mergeTask{}
		if _, err := p.C.do(http.MethodGet, href, nil, &task); err != nil {
			return nil, err
		}
	}
	return p.GetCR(n)
}

// GetBranch reports whether name is the main branch, and whether a branch
// restriction guards it. Reading restrictions needs admin rights; without
// them the branch counts as unprotected.
func (p *Provider) GetBranch(name string) (*protocol.Branch, error) {
	main, err := p.mainBranch()
	if err != nil {
		return nil, err
	}
	b := &protocol.Branch{Default: main == name}
	rules, err := list[restriction](p.C, p.C.repo()+"/branch-restrictions", url.Values{"pagelen": {"100"}}, false)
	if protocol.CodeOf(err) == protocol.CodeAuth {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if r.guards(name) {
			b.Protected = true
			break
		}
	}
	return b, nil
}

// restriction is a branch restriction (permission) rule.
type restriction struct {
	Kind      string `json:"kind"`
	MatchKind string `json:"branch_match_kind"` // glob, branching_model
	Pattern   string `json:"pattern"`
}

// guards reports whether r stops pushes to, force pushes to, or deletion of
// branch. Branching-model rules are ignored.
func (r restriction) guards(branch string) bool {
	switch r.Kind {
	case "push", "force", "delete":
	default:
		return false
	}
	if r.MatchKind != "glob" || r.Pattern == "" {
		return false
	}
	parts := strings.Split(r.Pattern, "*")
	for i, s := range parts {
		parts[i] = regexp.QuoteMeta(s)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(branch)
}

func (p *Provider) mainBranch() (string, error) {
	var r struct {
		MainBranch struct {
			Name string `json:"name"`
		} `json:"mainbranch"`
	}
	if _, err := p.C.do(http.MethodGet, p.C.repo(), nil, &r); err != nil {
		return "", err
	}
	return r.MainBranch.Name, nil
}

// GetRepo returns the main branch's default merge strategy.
func (p *Provider) GetRepo() (*protocol.RepoSettings, error) {
	main, err := p.mainBranch()
	if err != nil {
		return nil, err
	}
	var b struct {
		DefaultMergeStrategy string `json:"default_merge_strategy"`
	}
	if main != "" {
		segs := strings.Split(main, "/")
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		if _, err := p.C.do(http.MethodGet, p.C.repo()+"/refs/branches/"+strings.Join(segs, "/"), nil, &b); err != nil {
			return nil, err
		}
	}
	method := protocol.MethodMerge
	switch s := b.DefaultMergeStrategy; {
	case strings.HasPrefix(s, "squash"):
		method = protocol.MethodSquash
	case s == "fast_forward" || strings.HasPrefix(s, "rebase"):
		method = protocol.MethodRebase
	}
	return &protocol.RepoSettings{DefaultMergeMethod: method}, nil
}

// SyncStack is a no-op: Bitbucket has no stack object; the description block
// is the stack.
func (p *Provider) SyncStack([]int) error { return nil }

// Unstack is a no-op, like SyncStack.
func (p *Provider) Unstack([]int) error { return nil }

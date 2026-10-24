package ado

import (
	"fmt"
	"strings"

	"github.com/mhvelplund/git-stack/pkg/protocol"
)

// Info describes the Azure DevOps provider. Descriptions are limited to 4000
// characters.
var Info = protocol.Info{
	Name:         "ado",
	Terms:        protocol.Terms{Short: "PR", Long: "pull request", Ref: "!%d"},
	MaxBody:      4000,
	MergeMethods: []string{protocol.MethodMerge, protocol.MethodSquash, protocol.MethodRebase},
}

// Plugin is the Azure DevOps provider plugin, for protocol.Serve.
func Plugin(run Runner, rest *REST) protocol.Plugin {
	return protocol.Plugin{
		Info: Info,
		// Azure DevOps Server (and any other Azure DevOps host) is recognised
		// by its /_git/ repository paths.
		Detect: func(repo protocol.Repo) bool {
			_, err := ParseRepo(repo)
			return err == nil
		},
		Open: func(repo protocol.Repo) (protocol.Provider, error) {
			r, err := ParseRepo(repo)
			if err != nil {
				return nil, err
			}
			return &Provider{C: &Client{Run: run, Repo: r, REST: rest}}, nil
		},
	}
}

// Provider adapts Client to protocol.Provider.
type Provider struct {
	C *Client
}

var _ protocol.Provider = (*Provider)(nil)

// ToCR converts an Azure DevOps pull request.
func (p *Provider) ToCR(pr *PR) *protocol.CR {
	if pr == nil {
		return nil
	}
	state := protocol.StateOpen
	switch pr.Status {
	case "completed":
		state = protocol.StateMerged
	case "abandoned":
		state = protocol.StateClosed
	default:
		// Completion was requested and is running (auto-complete instead
		// waits for policies, and shows as AutoMerge).
		if pr.CompletionQueueTime != "" && !pr.autoComplete() {
			state = protocol.StateMerging
		}
	}
	cr := &protocol.CR{
		Number: pr.ID, Title: pr.Title, Body: pr.Description, State: state, Draft: pr.IsDraft,
		Head: branchName(pr.SourceRefName), Base: branchName(pr.TargetRefName),
		URL:       fmt.Sprintf("%s/pullrequest/%d", p.C.Repo.WebURL(), pr.ID),
		AutoMerge: pr.autoComplete(), Checking: pr.MergeStatus == "queued",
		MergeError: pr.MergeFailureMessage, FromFork: pr.fromFork(),
	}
	if pr.LastMergeSourceCommit != nil {
		cr.HeadSHA = pr.LastMergeSourceCommit.CommitID
	}
	if pr.CompletionOptions != nil {
		cr.DeleteHead = pr.CompletionOptions.DeleteSourceBranch
	}
	return cr
}

func (p *Provider) toCR(pr *PR, err error) (*protocol.CR, error) {
	if err != nil {
		return nil, err
	}
	return p.ToCR(pr), nil
}

// GetCR fetches a pull request.
func (p *Provider) GetCR(n int) (*protocol.CR, error) { return p.toCR(p.C.GetPR(n)) }

// FindCRForBranch returns the active PR whose source is branch, else the most
// recently completed one, else nil. PRs from forks are ignored.
func (p *Provider) FindCRForBranch(branch string) (*protocol.CR, error) {
	prs, err := p.C.ListPRs(map[string]string{"sourceRefName": branchRef(branch), "status": "all"})
	if err != nil {
		return nil, err
	}
	for _, want := range []string{"active", "completed"} {
		var best *PR
		for _, pr := range prs {
			if pr.Status == want && !pr.fromFork() && (best == nil || pr.ID > best.ID) {
				best = pr
			}
		}
		if best != nil {
			return p.ToCR(best), nil
		}
	}
	return nil, nil
}

// listedDescriptionMax is how much of a description PR lists return.
const listedDescriptionMax = 400

// SearchCRs returns active PRs whose description contains text. Azure DevOps
// cannot search descriptions, and lists cut them short, so long ones are
// fetched in full.
func (p *Provider) SearchCRs(text string) ([]*protocol.CR, error) {
	prs, err := p.C.ListPRs(map[string]string{"status": "active"})
	if err != nil {
		return nil, err
	}
	var out []*protocol.CR
	for _, pr := range prs {
		if !strings.Contains(pr.Description, text) && len([]rune(pr.Description)) >= listedDescriptionMax {
			if pr, err = p.C.GetPR(pr.ID); err != nil {
				return nil, err
			}
		}
		if strings.Contains(pr.Description, text) {
			out = append(out, p.ToCR(pr))
		}
	}
	return out, nil
}

// ListCRsTargeting returns active PRs, not from forks, whose target is branch.
func (p *Provider) ListCRsTargeting(branch string) ([]*protocol.CR, error) {
	prs, err := p.C.ListPRs(map[string]string{"targetRefName": branchRef(branch), "status": "active"})
	if err != nil {
		return nil, err
	}
	var out []*protocol.CR
	for _, pr := range prs {
		if !pr.fromFork() {
			out = append(out, p.ToCR(pr))
		}
	}
	return out, nil
}

// CreateCR opens a pull request.
func (p *Provider) CreateCR(n protocol.NewCR) (*protocol.CR, error) { return p.toCR(p.C.CreatePR(n)) }

// UpdateCR changes the target branch, title or description. az cannot change
// the target branch, so that goes to the REST API.
func (p *Provider) UpdateCR(n int, u protocol.Update) (*protocol.CR, error) {
	var pr *PR
	if u.Base != nil {
		pr = &PR{}
		if err := p.C.REST.PatchPR(p.C.Repo, n, map[string]any{"targetRefName": branchRef(*u.Base)}, pr); err != nil {
			return nil, err
		}
	}
	if u.Title != nil || u.Body != nil {
		var err error
		if pr, err = p.C.UpdatePR(n, PRUpdate{Title: u.Title, Description: u.Body}); err != nil {
			return nil, err
		}
	}
	if pr == nil {
		return p.GetCR(n)
	}
	return p.ToCR(pr), nil
}

func ptr[T any](v T) *T { return &v }

// MarkReady takes a pull request out of draft.
func (p *Provider) MarkReady(n int) (*protocol.CR, error) {
	return p.toCR(p.C.UpdatePR(n, PRUpdate{Draft: ptr(false)}))
}

// CancelAutoMerge turns auto-complete off.
func (p *Provider) CancelAutoMerge(n int) error {
	_, err := p.C.UpdatePR(n, PRUpdate{AutoComplete: ptr(false)})
	return err
}

// Merge completes the pull request, if its source branch is still at sha.
// When Azure DevOps refuses (required reviewers, builds or other policies),
// it sets the PR to auto-complete instead, so it completes once the policies
// pass. az completes with a merge commit or a squash; a rebase goes to the
// REST API.
func (p *Provider) Merge(n int, method, sha string) (*protocol.CR, error) {
	if method == "" {
		method = protocol.MethodMerge
	}
	if method != protocol.MethodMerge && method != protocol.MethodSquash && method != protocol.MethodRebase {
		return nil, protocol.Errorf(protocol.CodeUnsupported, "merge method %q is not supported", method)
	}
	cur, err := p.C.GetPR(n)
	if err != nil {
		return nil, err
	}
	if head := cur.head(); sha != "" && head != "" && head != sha {
		return nil, protocol.Errorf(protocol.CodeFailure, "!%d moved on Azure DevOps since it was pushed (head %s, expected %s)", n, head, sha)
	}
	squash := ptr(method == protocol.MethodSquash)
	var pr *PR
	var mergeErr error
	if method == protocol.MethodRebase {
		pr = &PR{}
		mergeErr = p.C.REST.PatchPR(p.C.Repo, n, map[string]any{
			"status": "completed", "lastMergeSourceCommit": Ref{CommitID: cur.head()},
			"completionOptions": map[string]any{"mergeStrategy": "rebase", "deleteSourceBranch": false},
		}, pr)
	} else {
		pr, mergeErr = p.C.UpdatePR(n, PRUpdate{Status: "completed", Squash: squash})
	}
	if mergeErr == nil {
		return p.ToCR(pr), nil
	}
	if protocol.CodeOf(mergeErr) == protocol.CodeAuth {
		return nil, mergeErr
	}
	pr, err = p.C.UpdatePR(n, PRUpdate{AutoComplete: ptr(true), Squash: squash})
	if err == nil && method == protocol.MethodRebase {
		pr = &PR{}
		err = p.C.REST.PatchPR(p.C.Repo, n, map[string]any{
			"completionOptions": map[string]any{"mergeStrategy": "rebase", "deleteSourceBranch": false},
		}, pr)
	}
	if err != nil {
		cr, gerr := p.GetCR(n)
		if gerr != nil {
			return nil, mergeErr
		}
		cr.MergeError = fmt.Sprintf("%v; setting auto-complete also failed: %v", mergeErr, err)
		return cr, nil
	}
	return p.ToCR(pr), nil
}

// GetBranch reports whether name is the default branch, and whether a
// blocking branch policy applies to it.
func (p *Provider) GetBranch(name string) (*protocol.Branch, error) {
	repo, err := p.C.GetRepo()
	if err != nil {
		return nil, err
	}
	b := &protocol.Branch{Default: repo.DefaultBranch == branchRef(name)}
	policies, err := p.C.Policies()
	if err != nil {
		return nil, err
	}
	for _, pol := range policies {
		if pol.IsEnabled && pol.IsBlocking && !pol.IsDeleted && pol.applies(repo, branchRef(name)) {
			b.Protected = true
			break
		}
	}
	return b, nil
}

// applies reports whether the policy covers ref in repo.
func (pol *Policy) applies(repo *RepoInfo, ref string) bool {
	for _, s := range pol.Settings.Scope {
		if s.RepositoryID != "" && !strings.EqualFold(s.RepositoryID, repo.ID) {
			continue
		}
		switch strings.ToLower(s.MatchKind) {
		case "exact":
			if s.RefName == ref {
				return true
			}
		case "prefix":
			if strings.HasPrefix(ref, s.RefName) {
				return true
			}
		case "defaultbranch":
			if ref == repo.DefaultBranch {
				return true
			}
		}
	}
	return false
}

// mergeStrategyPolicy is the policy type "Require a merge strategy".
const mergeStrategyPolicy = "fa4e907d-c16b-4a4c-9dfa-4916e5d171ab"

// GetRepo returns the default branch's preferred merge method: the first
// strategy its "Require a merge strategy" policy allows, else merge.
func (p *Provider) GetRepo() (*protocol.RepoSettings, error) {
	repo, err := p.C.GetRepo()
	if err != nil {
		return nil, err
	}
	policies, err := p.C.Policies()
	if err != nil {
		return nil, err
	}
	method := protocol.MethodMerge
	for _, pol := range policies {
		if !pol.IsEnabled || pol.IsDeleted || !strings.EqualFold(pol.Type.ID, mergeStrategyPolicy) || !pol.applies(repo, repo.DefaultBranch) {
			continue
		}
		s := pol.Settings
		switch {
		case s.AllowNoFastForward:
			method = protocol.MethodMerge
		case s.AllowSquash:
			method = protocol.MethodSquash
		case s.AllowRebase || s.AllowRebaseMerge:
			method = protocol.MethodRebase
		}
		break
	}
	return &protocol.RepoSettings{DefaultMergeMethod: method}, nil
}

// SyncStack is a no-op: Azure DevOps has no stack object; the description
// block is the stack.
func (p *Provider) SyncStack([]int) error { return nil }

// Unstack is a no-op, like SyncStack.
func (p *Provider) Unstack([]int) error { return nil }

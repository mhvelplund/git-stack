package gitlab

import (
	"github.com/mhvelplund/git-stack/pkg/protocol"
)

// Info describes the GitLab provider.
var Info = protocol.Info{
	Name:         "gitlab",
	Terms:        protocol.Terms{Short: "MR", Long: "merge request", Ref: "!%d"},
	MergeMethods: []string{protocol.MethodMerge, protocol.MethodSquash, protocol.MethodRebase},
}

// Plugin is the GitLab provider plugin, for protocol.Serve.
func Plugin(run Runner) protocol.Plugin {
	return protocol.Plugin{
		Info: Info,
		// A self-hosted GitLab is recognised when glab is logged in to it.
		Detect: func(repo protocol.Repo) bool {
			_, err := run("auth", "status", "--hostname", repo.Host)
			return err == nil
		},
		Open: func(repo protocol.Repo) (protocol.Provider, error) {
			return &Provider{C: &Client{Run: run, Project: Project{Host: repo.Host, Path: repo.Path}}}, nil
		},
	}
}

// Provider adapts Client to protocol.Provider.
type Provider struct {
	C *Client
}

var _ protocol.Provider = (*Provider)(nil)

// ToCR converts a GitLab MR.
func ToCR(m *MR) *protocol.CR {
	if m == nil {
		return nil
	}
	state := m.State
	switch m.State {
	case "opened":
		state = protocol.StateOpen
	case "locked":
		state = protocol.StateMerging
	}
	return &protocol.CR{
		Number: m.IID, Title: m.Title, Body: m.Description, State: state, Draft: m.Draft,
		Head: m.SourceBranch, Base: m.TargetBranch, URL: m.WebURL, HeadSHA: m.SHA,
		AutoMerge: m.AutoMerge, DeleteHead: m.RemoveSource, Checking: m.Checking(),
		MergeError: m.MergeError, FromFork: m.fromFork(),
	}
}

func toCRs(list []*MR, err error) ([]*protocol.CR, error) {
	if err != nil {
		return nil, err
	}
	out := make([]*protocol.CR, len(list))
	for i, m := range list {
		out[i] = ToCR(m)
	}
	return out, nil
}

func toCR(m *MR, err error) (*protocol.CR, error) {
	if err != nil {
		return nil, err
	}
	return ToCR(m), nil
}

func (p *Provider) GetCR(n int) (*protocol.CR, error) { return toCR(p.C.GetMR(n)) }

func (p *Provider) FindCRForBranch(branch string) (*protocol.CR, error) {
	return toCR(p.C.FindMRForBranch(branch))
}

func (p *Provider) SearchCRs(text string) ([]*protocol.CR, error) { return toCRs(p.C.SearchMRs(text)) }

func (p *Provider) ListCRsTargeting(branch string) ([]*protocol.CR, error) {
	return toCRs(p.C.ListMRsTargeting(branch))
}

func (p *Provider) CreateCR(n protocol.NewCR) (*protocol.CR, error) {
	return toCR(p.C.CreateMR(NewMR{Source: n.Head, Target: n.Base, Title: n.Title, Description: n.Body, Draft: n.Draft}))
}

func (p *Provider) UpdateCR(n int, u protocol.Update) (*protocol.CR, error) {
	fields := map[string]any{}
	if u.Base != nil {
		fields["target_branch"] = *u.Base
	}
	if u.Title != nil {
		fields["title"] = *u.Title
	}
	if u.Body != nil {
		fields["description"] = *u.Body
	}
	return toCR(p.C.UpdateMR(n, fields))
}

func (p *Provider) MarkReady(n int) (*protocol.CR, error) {
	mr, err := p.C.GetMR(n)
	if err != nil {
		return nil, err
	}
	return toCR(p.C.MarkReady(mr))
}

func (p *Provider) CancelAutoMerge(n int) error { return p.C.CancelAutoMerge(n) }

// Merge requests an auto-merge. GitLab has no per-merge choice between a merge
// commit and a rebase (that is the project's merge method), so "merge" and
// "rebase" both keep the commits.
func (p *Provider) Merge(n int, method, sha string) (*protocol.CR, error) {
	squash := method == protocol.MethodSquash
	if !squash {
		// GitLab ignores squash=false on merge and goes by the MR's own
		// setting, which projects that encourage squashing turn on for new MRs.
		if mr, err := p.C.GetMR(n); err == nil && mr.Squash {
			if _, err := p.C.UpdateMR(n, map[string]any{"squash": false}); err != nil {
				return nil, protocol.Errorf(protocol.CodeFailure, "could not turn off squashing for !%d: %v", n, err)
			}
		}
	}
	return toCR(p.C.Merge(n, squash, sha))
}

func (p *Provider) GetBranch(name string) (*protocol.Branch, error) {
	b, err := p.C.GetBranch(name)
	if err != nil {
		return nil, err
	}
	return &protocol.Branch{Protected: b.Protected, Default: b.Default}, nil
}

func (p *Provider) GetRepo() (*protocol.RepoSettings, error) {
	proj, err := p.C.GetProject()
	if err != nil {
		return nil, err
	}
	method := protocol.MethodMerge
	if proj.SquashOption == "always" || proj.SquashOption == "default_on" {
		method = protocol.MethodSquash
	}
	return &protocol.RepoSettings{DefaultMergeMethod: method}, nil
}

// SyncStack is a no-op: GitLab has no stack object; the description block is
// the stack.
func (p *Provider) SyncStack([]int) error { return nil }

// Unstack is a no-op, like SyncStack.
func (p *Provider) Unstack([]int) error { return nil }

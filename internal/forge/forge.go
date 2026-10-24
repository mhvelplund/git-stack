// Package forge connects git-stack to the host of a repository through a
// provider plugin, or to no host at all (provider "none").
package forge

import (
	"fmt"

	"github.com/mhvelplund/git-stack/pkg/protocol"
)

// Type aliases keep the core's code short; the types live in pkg/protocol so
// that plugins outside this module can use them.
type (
	CR           = protocol.CR
	NewCR        = protocol.NewCR
	Update       = protocol.Update
	Branch       = protocol.Branch
	RepoSettings = protocol.RepoSettings
	Info         = protocol.Info
	Repo         = protocol.Repo
)

// None is the provider used when the host is unknown: plain git, no CRs.
const None = "none"

// Forge is a provider bound to one repository.
type Forge struct {
	protocol.Provider
	Info Info
	Repo Repo
}

// New binds a plugin, reached through t, to repo.
func New(t Transport, info Info, repo Repo) *Forge {
	return &Forge{Provider: &client{t: t, repo: repo}, Info: info, Repo: repo}
}

// NewNone returns the git-only forge.
func NewNone(repo Repo) *Forge {
	return &Forge{Provider: noneProvider{}, Info: NoneInfo, Repo: repo}
}

// NoneInfo describes provider "none".
var NoneInfo = Info{
	Name:     None,
	Protocol: protocol.Version,
	Terms:    protocol.Terms{Short: "PR", Long: "pull request", Ref: "#%d"},
}

// HasCRs reports whether the forge manages change requests at all.
func (f *Forge) HasCRs() bool { return f.Info.Name != None }

// Ref formats CR number n the way the host writes it, e.g. #12 or !12.
func (f *Forge) Ref(n int) string {
	format := f.Info.Terms.Ref
	if format == "" {
		format = "#%d"
	}
	return fmt.Sprintf(format, n)
}

// Short is the host's abbreviation for a CR, e.g. PR or MR.
func (f *Forge) Short() string { return orDefault(f.Info.Terms.Short, "PR") }

// Long is the host's name for a CR, e.g. pull request or merge request.
func (f *Forge) Long() string { return orDefault(f.Info.Terms.Long, "pull request") }

// HostName is a display name for the host, e.g. GitHub.
func (f *Forge) HostName() string {
	switch f.Info.Name {
	case "github":
		return "GitHub"
	case "gitlab":
		return "GitLab"
	case "bitbucket":
		return "Bitbucket"
	case None:
		return f.Repo.Host
	}
	return f.Info.Name
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// noneProvider answers lookups with "nothing found" and refuses changes, so
// read-only paths (view, sync) work without a host.
type noneProvider struct{}

func unsupported() error {
	return protocol.Errorf(protocol.CodeUnsupported, "the repository's host is not supported, so git-stack cannot manage pull requests; set the provider with --provider or `git config git-stack.provider`")
}

func (noneProvider) GetCR(int) (*CR, error)                 { return nil, unsupported() }
func (noneProvider) FindCRForBranch(string) (*CR, error)    { return nil, nil }
func (noneProvider) SearchCRs(string) ([]*CR, error)        { return nil, nil }
func (noneProvider) ListCRsTargeting(string) ([]*CR, error) { return nil, nil }
func (noneProvider) CreateCR(NewCR) (*CR, error)            { return nil, unsupported() }
func (noneProvider) UpdateCR(int, Update) (*CR, error)      { return nil, unsupported() }
func (noneProvider) MarkReady(int) (*CR, error)             { return nil, unsupported() }
func (noneProvider) CancelAutoMerge(int) error              { return unsupported() }
func (noneProvider) Merge(int, string, string) (*CR, error) { return nil, unsupported() }
func (noneProvider) GetBranch(string) (*Branch, error)      { return nil, unsupported() }
func (noneProvider) GetRepo() (*RepoSettings, error) {
	return &RepoSettings{DefaultMergeMethod: protocol.MethodMerge}, nil
}
func (noneProvider) SyncStack([]int) error { return nil }
func (noneProvider) Unstack([]int) error   { return nil }

// CR states.
const (
	StateOpen    = protocol.StateOpen
	StateClosed  = protocol.StateClosed
	StateMerged  = protocol.StateMerged
	StateMerging = protocol.StateMerging
)

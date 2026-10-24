package cli

import (
	"fmt"
	"path/filepath"

	"gitlab.com/malephex/git-stack/internal/exitcode"
	"gitlab.com/malephex/git-stack/internal/forge"
)

// forge returns the forge for remote: the provider from --provider, else
// `git config git-stack.provider`, else one detected from the remote URL.
// Forges are cached per remote for the life of the command.
func (a *App) forge(remote string) (*forge.Forge, error) {
	if f := a.forges[remote]; f != nil {
		a.lastForge = f
		return f, nil
	}
	f, err := a.newForge(remote)
	if err != nil {
		return nil, err
	}
	if a.forges == nil {
		a.forges = map[string]*forge.Forge{}
	}
	a.forges[remote] = f
	a.lastForge = f
	return f, nil
}

func (a *App) newForge(remote string) (*forge.Forge, error) {
	if a.NewForge != nil {
		return a.NewForge(remote)
	}
	raw, err := a.repo().Run("remote", "get-url", remote)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.InvalidArgs, fmt.Errorf("remote %q: %w", remote, err))
	}
	r := forge.Resolver{Finder: forge.DefaultFinder(), Explicit: a.providerName(), Warn: a.infof}
	if dir, err := a.repo().CommonDir(); err == nil {
		r.Cache = forge.FileCache{Path: filepath.Join(dir, "git-stack-hosts.json"), Now: a.Now}
	}
	f, err := r.Resolve(raw)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.ProviderFailure, err)
	}
	return f, nil
}

// providerName is the explicitly chosen provider, or "".
func (a *App) providerName() string {
	if a.Provider != "" {
		return a.Provider
	}
	name, _ := a.repo().Run("config", "--get", forge.ConfigKey)
	return name
}

// requireCRs fails unless f can manage change requests.
func requireCRs(f *forge.Forge, what string) error {
	if f.HasCRs() {
		return nil
	}
	return exitcode.New(exitcode.Unsupported, "%s needs pull requests, but no provider supports %s; choose one with --provider or `git config %s <name>`", what, hostOrRemote(f), forge.ConfigKey)
}

func hostOrRemote(f *forge.Forge) string {
	if f.Repo.Host != "" {
		return f.Repo.Host
	}
	return f.Repo.RemoteURL
}

func pickRemote(flag, stackRemote string) string {
	if flag != "" {
		return flag
	}
	if stackRemote != "" {
		return stackRemote
	}
	return "origin"
}

// terms is the forge of the last remote this command resolved, for wording
// messages in the host's own terms; nil until one is resolved.
func (a *App) terms() *forge.Forge {
	return a.lastForge
}

// ref formats CR number n as the host writes it, e.g. #12 or !12.
func (a *App) ref(n int) string {
	if f := a.terms(); f != nil {
		return f.Ref(n)
	}
	return fmt.Sprintf("#%d", n)
}

// cr is the host's abbreviation for a change request, e.g. PR or MR.
func (a *App) cr() string {
	if f := a.terms(); f != nil {
		return f.Short()
	}
	return "PR"
}

// host is the display name of the host, e.g. GitHub.
func (a *App) host() string {
	if f := a.terms(); f != nil && f.HasCRs() {
		return f.HostName()
	}
	return "the remote"
}

// long is the host's name for a change request, e.g. pull request.
func (a *App) long() string {
	if f := a.terms(); f != nil {
		return f.Long()
	}
	return "pull request"
}

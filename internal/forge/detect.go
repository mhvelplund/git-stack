package forge

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gitlab.com/malephex/git-stack/pkg/protocol"
)

// ConfigKey is the git config key that pins a repository's provider.
const ConfigKey = "git-stack.provider"

// knownHosts maps public hosts to their provider.
var knownHosts = map[string]string{
	"github.com": "github",
	"gitlab.com": "gitlab",
}

// GuessProvider names the provider for host from its name alone: a known
// public host, or a self-hosted one whose first label names the product
// (github.example.com, gitlab.example.com). It returns "" if it cannot tell.
func GuessProvider(host string) string {
	host = strings.ToLower(host)
	if p, ok := knownHosts[host]; ok {
		return p
	}
	first, _, _ := strings.Cut(host, ".")
	for _, p := range []string{"github", "gitlab"} {
		if first == p {
			return p
		}
	}
	return ""
}

// Finder locates provider plugins.
type Finder struct {
	// Dirs are searched before PATH, e.g. the directory of git-stack itself.
	Dirs []string
}

// DefaultFinder searches next to the running executable, then PATH.
func DefaultFinder() Finder {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	return Finder{Dirs: dirs}
}

func exeName(provider string) string {
	name := protocol.ExecutablePrefix + provider
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// Find returns the path of the plugin for provider.
func (f Finder) Find(provider string) (string, error) {
	for _, d := range f.Dirs {
		p := filepath.Join(d, exeName(provider))
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return exec.LookPath(protocol.ExecutablePrefix + provider)
}

// Installed lists the providers that have a plugin, sorted by name.
func (f Finder) Installed() []string {
	seen := map[string]bool{}
	dirs := append(append([]string{}, f.Dirs...), filepath.SplitList(os.Getenv("PATH"))...)
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if runtime.GOOS == "windows" {
				var ok bool
				if name, ok = strings.CutSuffix(strings.ToLower(name), ".exe"); !ok {
					continue
				}
			}
			if p, ok := strings.CutPrefix(name, protocol.ExecutablePrefix); ok && validName(p) {
				seen[p] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// validName reports whether p can be a provider name: lower-case letters,
// digits and dashes, and not "none".
func validName(p string) bool {
	if p == "" || p == None {
		return false
	}
	for _, r := range p {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// Resolver picks the provider for a repository.
type Resolver struct {
	Finder Finder
	// Explicit is the provider chosen by flag or git config; "" to detect.
	Explicit string
	// Warn reports a fallback to provider none.
	Warn func(format string, args ...any)
	// Transport overrides how plugins are run (tests).
	Transport func(path string) Transport
	// Cache remembers which provider claimed an unknown host ("none" if no
	// plugin did), so plugins are not asked on every command. It may be nil.
	Cache DetectCache
}

// DetectCache stores the outcome of asking plugins about a host.
type DetectCache interface {
	Get(host string) (provider string, ok bool)
	Set(host, provider string)
}

func (r Resolver) transport(path string) Transport {
	if r.Transport != nil {
		return r.Transport(path)
	}
	return ExecTransport(path)
}

// detectTimeout bounds asking plugins whether they own an unknown host.
const detectTimeout = 10 * time.Second

// Resolve returns the forge for remoteURL. An explicitly chosen provider must
// be installed; a detected one falls back to "none" with a warning.
func (r Resolver) Resolve(remoteURL string) (*Forge, error) {
	repo, perr := ParseRemoteURL(remoteURL)
	if perr == nil && !strings.HasPrefix(remoteURL, "http://") && !strings.HasPrefix(remoteURL, "https://") {
		repo.Host = SSHHostname(repo.Host) // ssh config aliases apply to ssh remotes only
	}
	repo.RemoteURL = remoteURL

	name := r.Explicit
	if name == None {
		return NewNone(repo), nil
	}
	if name != "" {
		if !validName(name) {
			return nil, protocol.Errorf(protocol.CodeUnsupported, "invalid provider name %q", name)
		}
		path, err := r.Finder.Find(name)
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeUnsupported, "provider %q is not installed: %s was not found next to git-stack or on PATH", name, exeName(name))
		}
		if perr != nil {
			return nil, protocol.Errorf(protocol.CodeFailure, "provider %s: %v", name, perr)
		}
		return r.open(path, repo)
	}
	if perr != nil {
		return NewNone(repo), nil // a local path or other remote without a host
	}
	if name = GuessProvider(repo.Host); name != "" {
		path, err := r.Finder.Find(name)
		if err != nil {
			r.warn("%s looks like %s, but %s is not installed; pull requests are disabled", repo.Host, name, exeName(name))
			return NewNone(repo), nil
		}
		return r.open(path, repo)
	}
	if r.Cache != nil {
		if p, ok := r.Cache.Get(repo.Host); ok {
			if p == None {
				return NewNone(repo), nil
			}
			if path, err := r.Finder.Find(p); err == nil {
				return r.open(path, repo)
			}
		}
	}
	for _, p := range r.Finder.Installed() {
		path, err := r.Finder.Find(p)
		if err != nil {
			continue
		}
		if detectWithTimeout(r.transport(path), repo) {
			r.remember(repo.Host, p)
			return r.open(path, repo)
		}
	}
	r.remember(repo.Host, None)
	return NewNone(repo), nil
}

func (r Resolver) remember(host, provider string) {
	if r.Cache != nil {
		r.Cache.Set(host, provider)
	}
}

func (r Resolver) open(path string, repo Repo) (*Forge, error) {
	t := r.transport(path)
	info, err := Describe(t)
	if err != nil {
		return nil, fmt.Errorf("provider %s: %w", path, err)
	}
	return New(t, *info, repo), nil
}

func (r Resolver) warn(format string, args ...any) {
	if r.Warn != nil {
		r.Warn(format, args...)
	}
}

func detectWithTimeout(t Transport, repo Repo) bool {
	ch := make(chan bool, 1)
	go func() { ch <- Detect(t, repo) }()
	select {
	case ok := <-ch:
		return ok
	case <-time.After(detectTimeout):
		return false
	}
}

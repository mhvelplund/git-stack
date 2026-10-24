package forge

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mhvelplund/git-stack/pkg/protocol"
)

// emptyPath leaves only an empty directory on PATH, so no real plugin is found.
func emptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestResolveFallsBackToNone(t *testing.T) {
	emptyPath(t)
	var warnings []string
	r := Resolver{Warn: func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) }}
	for _, url := range []string{`C:\work\origin.git`, "/srv/git/repo.git", "https://git.example/team/repo.git"} {
		f, err := r.Resolve(url)
		if err != nil || f.HasCRs() {
			t.Errorf("Resolve(%q) = %+v, %v; want provider none", url, f, err)
		}
	}
	if len(warnings) != 0 {
		t.Errorf("unknown hosts should not warn: %v", warnings)
	}
	f, err := r.Resolve("git@github.com:o/r.git")
	if err != nil || f.HasCRs() || len(warnings) != 1 || !strings.Contains(warnings[0], "git-stack-github") {
		t.Fatalf("missing plugin: %+v, %v, warnings %v", f, err, warnings)
	}
}

func TestResolveExplicit(t *testing.T) {
	emptyPath(t)
	f, err := Resolver{Explicit: None}.Resolve("git@github.com:o/r.git")
	if err != nil || f.HasCRs() || f.Repo.Path != "o/r" {
		t.Fatalf("explicit none = %+v, %v", f, err)
	}
	_, err = Resolver{Explicit: "github"}.Resolve("git@github.com:o/r.git")
	if protocol.CodeOf(err) != protocol.CodeUnsupported || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("explicit missing plugin: %v", err)
	}
	if _, err := (Resolver{Explicit: "../evil"}).Resolve("git@github.com:o/r.git"); err == nil {
		t.Fatal("an invalid provider name was accepted")
	}
}

// fakePlugin answers like a provider for host "forge.example".
func fakePlugin() protocol.Plugin {
	return protocol.Plugin{
		Info:   protocol.Info{Name: "fake", Terms: protocol.Terms{Short: "CR", Ref: "~%d"}},
		Detect: func(r protocol.Repo) bool { return r.Host == "forge.example" },
		Open: func(r protocol.Repo) (protocol.Provider, error) {
			return fakeProvider{repo: r}, nil
		},
	}
}

type fakeProvider struct {
	noneProvider
	repo protocol.Repo
}

func (p fakeProvider) GetCR(n int) (*CR, error) {
	if n == 404 {
		return nil, protocol.Errorf(protocol.CodeNotFound, "no ~%d", n)
	}
	return &CR{Number: n, Title: p.repo.Path, State: StateOpen}, nil
}

func TestResolveAsksInstalledPlugins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	name := "git-stack-fake"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	r := Resolver{Finder: Finder{}, Transport: func(string) Transport { return LocalTransport(fakePlugin()) }}
	f, err := r.Resolve("https://forge.example/team/repo")
	if err != nil || f.Info.Name != "fake" || f.Ref(3) != "~3" {
		t.Fatalf("Resolve = %+v, %v", f, err)
	}
	cr, err := f.GetCR(5)
	if err != nil || cr.Number != 5 || cr.Title != "team/repo" {
		t.Fatalf("GetCR over the protocol = %+v, %v", cr, err)
	}
	if _, err := f.GetCR(404); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("error code lost over the protocol: %v", err)
	}
	if f, _ := r.Resolve("https://other.example/team/repo"); f.HasCRs() {
		t.Fatalf("a plugin claimed a host it does not detect: %+v", f.Info)
	}
}

// TestExecPlugin builds the real GitLab plugin and talks to it over stdio.
func TestExecPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "git-stack-gitlab")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	build := exec.Command("go", "build", "-o", out, "github.com/mhvelplund/git-stack/cmd/git-stack-gitlab")
	if msg, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the plugin: %v\n%s", err, msg)
	}
	emptyPath(t)
	f, err := Resolver{Finder: Finder{Dirs: []string{dir}}}.Resolve("git@gitlab.com:group/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	if f.Info.Name != "gitlab" || f.Ref(7) != "!7" || f.Short() != "MR" {
		t.Fatalf("describe over exec = %+v", f.Info)
	}
	// glab is not on PATH, so the call fails, as a protocol error.
	if _, err := f.GetCR(1); err == nil || protocol.CodeOf(err) == "" {
		t.Fatalf("GetCR without glab: %v", err)
	}
	// Run by hand, the plugin says what it is.
	msg, _ := exec.Command(out).CombinedOutput()
	if !strings.Contains(string(msg), "git-stack provider plugin") {
		t.Fatalf("plugin run without an operation said %q", msg)
	}
}

type mapCache map[string]string

func (m mapCache) Get(h string) (string, bool) { p, ok := m[h]; return p, ok }
func (m mapCache) Set(h, p string)             { m[h] = p }

func TestResolveCachesDetection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	name := "git-stack-fake"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	asked := 0
	plugin := fakePlugin()
	detect := plugin.Detect
	plugin.Detect = func(r protocol.Repo) bool { asked++; return detect(r) }
	cache := mapCache{}
	r := Resolver{Cache: cache, Transport: func(string) Transport { return LocalTransport(plugin) }}
	for range 2 {
		if f, _ := r.Resolve("https://forge.example/a/b"); f.Info.Name != "fake" {
			t.Fatalf("Resolve = %+v", f.Info)
		}
		if f, _ := r.Resolve("https://other.example/a/b"); f.HasCRs() {
			t.Fatalf("Resolve(other) = %+v", f.Info)
		}
	}
	if asked != 2 || cache["forge.example"] != "fake" || cache["other.example"] != None {
		t.Fatalf("asked %d times, cache %v", asked, cache)
	}
}

func TestFileCacheExpires(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := FileCache{Path: filepath.Join(t.TempDir(), "hosts.json"), Now: func() time.Time { return now }}
	if _, ok := c.Get("h"); ok {
		t.Fatal("empty cache hit")
	}
	c.Set("h", "gitlab")
	if p, ok := c.Get("h"); !ok || p != "gitlab" {
		t.Fatalf("Get = %q, %v", p, ok)
	}
	now = now.Add(25 * time.Hour)
	if _, ok := c.Get("h"); ok {
		t.Fatal("stale entry used")
	}
}

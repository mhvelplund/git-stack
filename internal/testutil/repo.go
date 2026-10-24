// Package testutil builds throwaway git repositories for tests.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a working clone with a bare "origin" next to it.
type Repo struct {
	t      *testing.T
	Dir    string
	Origin string
}

// NewRepo creates a bare origin with one commit on main and clones it.
func NewRepo(t *testing.T) *Repo {
	t.Helper()
	root := t.TempDir()
	r := &Repo{t: t, Dir: filepath.Join(root, "work"), Origin: filepath.Join(root, "origin.git")}
	if isolatedConfig == "" {
		t.Fatal("testutil.NewRepo needs the package's TestMain to call testutil.Main")
	}
	run(t, root, "git", "init", "--quiet", "--bare", "-b", "main", r.Origin)
	run(t, root, "git", "clone", "--quiet", r.Origin, r.Dir)
	r.Commit("README.md", "hello\n", "initial")
	r.Git("push", "--quiet", "-u", "origin", "main")
	r.Git("remote", "set-head", "origin", "main")
	return r
}

// isolatedConfig is the global git config Main points git at.
var isolatedConfig string

// Main runs the package's tests with the user's git config kept out of every
// git process. It sets the environment once for the whole process, so tests
// that use NewRepo can call t.Parallel. Call it from TestMain.
func Main(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	dir, err := os.MkdirTemp("", "git-stack-test-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	isolatedConfig = filepath.Join(dir, "gitconfig")
	content := "[user]\n\tname = Test\n\temail = test@example.com\n[init]\n\tdefaultBranch = main\n[advice]\n\tdetachedHead = false\n"
	if err := os.WriteFile(isolatedConfig, []byte(content), 0o644); err != nil {
		panic(err)
	}
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   isolatedConfig,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TEMPLATE_DIR":    "",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic(err)
		}
	}
	return m.Run()
}

// Git runs git in the working clone and returns trimmed stdout.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	return run(r.t, r.Dir, "git", args...)
}

// Commit writes file and commits it on the current branch.
func (r *Repo) Commit(file, content, msg string) string {
	r.t.Helper()
	path := filepath.Join(r.Dir, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.Git("add", file)
	r.Git("commit", "--quiet", "-m", msg)
	return r.Git("rev-parse", "HEAD")
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimRight(string(out), "\n")
}

// PushFromElsewhere commits file on branch in a separate clone and pushes it,
// simulating someone else updating the remote.
func (r *Repo) PushFromElsewhere(branch, file, content, msg string) string {
	r.t.Helper()
	other := filepath.Join(r.t.TempDir(), "other")
	run(r.t, filepath.Dir(other), "git", "clone", "--quiet", "--branch", branch, r.Origin, other)
	o := &Repo{t: r.t, Dir: other, Origin: r.Origin}
	sha := o.Commit(file, content, msg)
	o.Git("push", "--quiet", "origin", branch)
	return sha
}

// Clone returns a fresh clone of the origin, e.g. to act as a teammate.
func (r *Repo) Clone() *Repo {
	r.t.Helper()
	other := filepath.Join(r.t.TempDir(), "clone")
	run(r.t, filepath.Dir(other), "git", "clone", "--quiet", r.Origin, other)
	return &Repo{t: r.t, Dir: other, Origin: r.Origin}
}

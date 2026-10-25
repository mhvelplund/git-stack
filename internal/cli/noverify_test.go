package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failingHook installs a hook that always rejects.
func failingHook(t *testing.T, h *harness, name string) {
	t.Helper()
	hook := filepath.Join(h.repo.Dir, ".git", "hooks", name)
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho "+name+" failed >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// --no-verify skips the pre-commit hook for that command only.
func TestNoVerifySkipsCommitHook(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.must("init", "a")
	h.repo.Commit("a.txt", "a\n", "a1")
	failingHook(t, h, "pre-commit")

	writeFile(t, h.repo.Dir+"/b.txt", "b\n")
	h.must("--no-verify", "add", "-A", "-m", "second layer", "b")
	if got := h.repo.Git("log", "-1", "--format=%s"); got != "second layer" {
		t.Fatalf("last commit = %q", got)
	}
	if cfg, err := os.ReadFile(filepath.Join(h.repo.Dir, ".git", "config")); err != nil || strings.Contains(string(cfg), "hooksPath") {
		t.Fatalf("config changed (%v): %s", err, cfg)
	}

	writeFile(t, h.repo.Dir+"/c.txt", "c\n")
	if r := h.run("add", "-A", "-m", "third layer", "c"); r.e == nil || !strings.Contains(r.err+r.e.Error(), "pre-commit failed") {
		t.Fatalf("hook did not run without --no-verify: %v %s", r.e, r.err)
	}
}

// --no-verify skips the pre-push hook.
func TestNoVerifySkipsPushHook(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.must("init", "a")
	h.repo.Commit("a.txt", "a\n", "a1")
	failingHook(t, h, "pre-push")

	if r := h.run("push"); r.e == nil {
		t.Fatal("push succeeded although the pre-push hook failed")
	}
	h.must("--no-verify", "push")
	if remote := h.repo.Git("rev-parse", "origin/a"); remote != h.sha("a") {
		t.Fatalf("origin/a = %s, want %s", remote, h.sha("a"))
	}
}

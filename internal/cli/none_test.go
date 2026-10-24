package cli

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhvelplund/git-stack/internal/exitcode"
)

// gitOnly returns three layers in a repository whose origin is a local path:
// no provider applies, so git-stack works with plain git.
func gitOnly(t *testing.T) *harness {
	h := threeLayers(t)
	h.resolve = true
	return h
}

func TestSubmitWithoutProviderOnlyPushes(t *testing.T) {
	h := gitOnly(t)
	r := h.must("submit", "--auto")
	for _, b := range []string{"a", "b", "c"} {
		if !strings.HasPrefix(h.remoteSHA(b), h.sha(b)) {
			t.Errorf("%s was not pushed", b)
		}
	}
	if !strings.Contains(r.err, "no pull requests were opened") {
		t.Fatalf("missing notice:\n%s", r.err)
	}
	for _, b := range h.file().Stacks[0].Branches {
		if b.CR != nil {
			t.Fatalf("%s got a CR without a provider: %+v", b.Name, b.CR)
		}
	}
	if len(h.gl.Calls) != 0 {
		t.Fatalf("the fake GitLab was called: %v", h.gl.Calls)
	}
}

func TestMergeAndLinkNeedAProvider(t *testing.T) {
	h := gitOnly(t)
	for _, args := range [][]string{{"merge", "--yes"}, {"link", "a", "b"}} {
		r := h.run(args...)
		if exitcode.Of(r.e) != exitcode.Unsupported || !strings.Contains(r.e.Error(), "--provider") {
			t.Errorf("%v: exit %d, %v", args, exitcode.Of(r.e), r.e)
		}
	}
	if r := h.run("checkout", "12"); exitcode.Of(r.e) != exitcode.Unsupported && exitcode.Of(r.e) != exitcode.NotInStack {
		t.Errorf("checkout 12: exit %d, %v", exitcode.Of(r.e), r.e)
	}
}

func TestSyncWithoutProviderDetectsMergedLayers(t *testing.T) {
	h := gitOnly(t)
	h.must("push")
	// a lands on the trunk with its commits intact (fast-forward merge).
	h.repo.Git("push", "--quiet", "origin", "a:main")
	h.repo.PushFromElsewhere("main", "up.txt", "up\n", "upstream")

	r := h.must("sync")
	s := h.file().Stacks[0]
	if !s.Branches[0].Merged || s.Branches[1].Merged {
		t.Fatalf("merged flags = %v %v\n%s", s.Branches[0].Merged, s.Branches[1].Merged, r.err)
	}
	if !strings.Contains(r.err, "a is merged into main") {
		t.Fatalf("missing report:\n%s", r.err)
	}
	h.assertStacked("main", "b", "2")
	h.assertStacked("b", "c", "1")
	if !strings.HasPrefix(h.remoteSHA("c"), h.sha("c")) {
		t.Fatal("restacked c not pushed")
	}
}

func TestSyncWithoutProviderKeepsUnmergedLayers(t *testing.T) {
	h := gitOnly(t)
	h.must("push")
	h.repo.PushFromElsewhere("main", "up.txt", "up\n", "upstream")
	h.must("sync")
	for _, b := range h.file().Stacks[0].Branches {
		if b.Merged {
			t.Fatalf("%s marked merged", b.Name)
		}
	}
	h.assertStacked("main", "a", "1")
}

func TestExplicitProviderMustBeInstalled(t *testing.T) {
	h := gitOnly(t)
	t.Run("missing plugin", func(t *testing.T) {
		// Only git on PATH, so an installed git-stack-github cannot be found.
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", filepath.Dir(git))
		r := h.run("--provider", "github", "submit", "--auto")
		if exitcode.Of(r.e) != exitcode.ProviderFailure || !strings.Contains(r.e.Error(), "git-stack-github") {
			t.Fatalf("exit %d: %v", exitcode.Of(r.e), r.e)
		}
	})
	h.repo.Git("config", "git-stack.provider", "none")
	h.must("submit", "--auto") // git config picks provider none
}

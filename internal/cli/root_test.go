package cli

import (
	"testing"

	"gitlab.com/malephex/git-stack/internal/exitcode"
	"gitlab.com/malephex/git-stack/internal/stack"
)

func TestBadFlagIsInvalidArgs(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if got := exitcode.Of(h.run("--no-such-flag").e); got != exitcode.InvalidArgs {
		t.Fatalf("exit code = %d, want %d", got, exitcode.InvalidArgs)
	}
}

func TestMutationRespectsLock(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	unlock, err := stack.Store{Dir: h.repo.Dir + "/.git"}.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if got := exitcode.Of(h.run("init", "a").e); got != exitcode.Locked {
		t.Fatalf("exit code = %d, want %d", got, exitcode.Locked)
	}
}

func TestUsageErrorsAreInvalidArgs(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, args := range [][]string{{"no-such-command"}, {"view", "extra"}, {"up", "1", "2"}} {
		if got := exitcode.Of(h.run(args...).e); got != exitcode.InvalidArgs {
			t.Errorf("%v: exit %d, want %d", args, got, exitcode.InvalidArgs)
		}
	}
}

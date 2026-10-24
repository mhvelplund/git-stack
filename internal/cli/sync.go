package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitlab.com/malephex/git-stack/internal/engine"
	"gitlab.com/malephex/git-stack/internal/exitcode"
	"gitlab.com/malephex/git-stack/internal/forge"
	"gitlab.com/malephex/git-stack/internal/git"
	"gitlab.com/malephex/git-stack/internal/stack"
)

type syncOptions struct {
	remote string
	prune  bool
}

func newSyncCmd(app *App) *cobra.Command {
	var o syncOptions
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync the current stack with the remote",
		Long: `Fetch, rebase, push, and sync pull request state for the current stack.

  1. Fetches the latest changes from the remote
  2. Reconciles the stack on the host (the stack block in the PR descriptions)
     with your local stack: layers added on the host are pulled down
     automatically; if the two have diverged you are asked what to do
     (outside a terminal, sync aborts)
  3. Fast-forwards the trunk to match the remote
  4. Cascade-rebases the stack if the trunk moved; on conflict every branch
     is restored and you are told to run "git stack rebase"
  5. Pushes all unmerged branches with --force-with-lease
  6. Syncs PR state, retargeting PRs whose lower layer was merged
  7. Updates the stack block in every PR when two or more PRs exist

Sync never opens pull requests; use "git stack submit" for that.
With --prune, local branches of merged PRs are deleted without asking.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return app.runSync(o)
		},
	}
	cmd.Flags().StringVar(&o.remote, "remote", "", "remote to sync with (default: the stack's remote)")
	cmd.Flags().BoolVar(&o.prune, "prune", false, "delete local branches of merged PRs without asking")
	return cmd
}

func (a *App) runSync(o syncOptions) error {
	return a.mutate(func(f *stack.File) error {
		s, cur, err := a.currentStack(f)
		if err != nil {
			return err
		}
		remote := pickRemote(o.remote, s.Remote)
		c, err := a.forge(remote)
		if err != nil {
			return err
		}
		g := a.repo()
		if err := g.Fetch(remote); err != nil {
			return err
		}
		known, err := engine.SyncMRs(c, g, s)
		if err != nil {
			return err
		}
		if err := a.reconcile(f, s, c, known, remote); err != nil {
			return err
		}

		st, err := a.store()
		if err != nil {
			return err
		}
		r := &engine.Rebaser{Repo: g, Store: st, Log: a.infof}
		trunkSHA, err := r.FastForwardFromRemote(s, remote) // fetched above
		if err != nil {
			return err
		}
		if !c.HasCRs() {
			for _, name := range engine.MarkMergedByAncestry(g, s, trunkSHA) {
				a.infof("%s is merged into %s", name, s.Trunk)
			}
		}
		if bottom := s.Bottom(); bottom != nil && !g.IsAncestor(trunkSHA, git.Ref(bottom.Name)) {
			if err := r.Start(s, engine.RebaseOptions{Anchor: cur, Remote: remote, AbortOnConflict: true, TrunkSHA: trunkSHA}); err != nil {
				return err
			}
		}
		// Branches may have been rewritten: keep their new bases even if a later
		// step (push, the host) fails.
		if err := a.saveNow(f); err != nil {
			return err
		}

		if active := branchNames(s.Active()); len(active) > 0 {
			if err := engine.Push(g, remote, active); err != nil {
				return err
			}
			a.infof("Pushed %d branch(es) to %s", len(active), remote)
		}

		if err := a.retargetAndReport(c, s, known); err != nil {
			return err
		}
		withMR := 0
		for _, b := range s.Branches {
			if b.CR != nil && known[b.Name] != nil && known[b.Name].Open() {
				withMR++
			}
		}
		// A block that disagrees with the local stack (e.g. after choosing to
		// keep the local stack) is rewritten even for a single open PR, or the
		// next sync would find the stacks diverged again.
		stale := false
		if info := engine.RemoteStack(s, known); info != nil {
			stale = engine.Compare(s, info) != engine.InSync
		}
		if withMR >= 2 || stale {
			if err := engine.UpdateBlocks(c, s, known); err != nil {
				return err
			}
			a.infof("Stack synced")
		} else {
			a.infof("Branches synced")
		}
		return a.pruneMerged(c, s, known, remote, o.prune)
	})
}

// reconcile brings the local stack in line with the stack recorded on the host.
func (a *App) reconcile(f *stack.File, s *stack.Stack, c *forge.Forge, known map[string]*forge.CR, remote string) error {
	info := engine.RemoteStack(s, known)
	switch engine.Compare(s, info) {
	case engine.RemoteAhead:
		have := 0
		for _, b := range s.Branches {
			if b.CR != nil {
				have++
			}
		}
		for _, item := range info.Items[have:] {
			if err := a.appendRemoteLayer(f, s, c, known, item, remote); err != nil {
				return err
			}
			a.infof("Pulled %s (%s) from %s", item.Branch, a.ref(item.Number), a.host())
		}
	case engine.Diverged:
		if !a.Interactive {
			return exitcode.New(exitcode.Generic, "the local stack and the stack on %s have diverged; run sync in a terminal to resolve it", a.host())
		}
		choice, err := a.choose(fmt.Sprintf("The local stack and the stack on %s have diverged.", a.host()), []string{
			fmt.Sprintf("Use the stack on %s (local layers not on %s leave the stack)", a.host(), a.host()),
			fmt.Sprintf("Keep the local stack (%s is updated to match)", a.host()),
			"Cancel",
		})
		if err != nil {
			return err
		}
		switch choice {
		case 0:
			return a.adoptRemote(f, s, c, known, info, remote)
		case 1:
			return a.detachRemoteOnly(s, c, info)
		default:
			return exitcode.New(exitcode.Generic, "sync cancelled")
		}
	}
	return nil
}

// appendRemoteLayer adds a layer that exists on the host on top of s, creating
// or fast-forwarding its local branch like checkout does. A merged layer whose
// branch is gone is recorded without one.
func (a *App) appendRemoteLayer(f *stack.File, s *stack.Stack, c *forge.Forge, known map[string]*forge.CR, item forge.StackItem, remote string) error {
	if other := f.Find(item.Branch); other != nil && other != s {
		return exitcode.New(exitcode.Ambiguous, "%s is already part of another local stack", item.Branch)
	}
	b := &stack.Branch{Name: item.Branch, CR: &stack.CR{Number: item.Number}}
	if mr, err := c.GetCR(item.Number); err == nil {
		known[item.Branch] = mr
		b.CR.URL = mr.URL
		b.Merged = mr.Merged()
	}
	if err := a.syncLocalBranch(remote, item.Branch, b.Merged); err != nil {
		return err
	}
	below := git.Ref(s.Trunk)
	if n := len(s.Branches); n > 0 {
		last := s.Branches[n-1]
		mr := known[last.Name]
		if mr == nil && last.CR != nil && !a.repo().BranchExists(last.Name) {
			mr, _ = c.GetCR(last.CR.Number) // a merged layer whose branch is gone
		}
		below = layerTip(a.repo(), last.Name, mr)
	}
	b.Base = a.forkBase(below, item.Branch)
	s.Branches = append(s.Branches, b)
	return nil
}

// adoptRemote replaces the local layer list with the host's.
func (a *App) adoptRemote(f *stack.File, s *stack.Stack, c *forge.Forge, known map[string]*forge.CR, info *forge.StackInfo, remote string) error {
	if clean, err := a.repo().IsClean(); err != nil || !clean {
		return exitcode.New(exitcode.Generic, "commit or stash your changes before adopting the stack from %s", a.host())
	}
	old := s.Branches
	s.Branches = nil
	for _, item := range info.Items {
		var b *stack.Branch
		for _, ob := range old {
			if ob.Name == item.Branch {
				b = ob
			}
		}
		if b == nil {
			if err := a.appendRemoteLayer(f, s, c, known, item, remote); err != nil {
				return err
			}
			continue
		}
		s.Branches = append(s.Branches, b)
	}
	a.infof("Local stack now matches %s: %v", a.host(), branchNames(s.Branches))
	return nil
}

// detachRemoteOnly removes the stack block from open PRs the host lists but the local stack does not.
func (a *App) detachRemoteOnly(s *stack.Stack, c *forge.Forge, info *forge.StackInfo) error {
	var iids []int
	for _, item := range info.Items {
		if b := s.Get(item.Branch); b != nil && b.CR != nil && b.CR.Number == item.Number {
			continue
		}
		iids = append(iids, item.Number)
	}
	return a.stripBlocks(c, iids)
}

// retargetAndReport points each open PR at the layer below it and prints PR state.
func (a *App) retargetAndReport(c *forge.Forge, s *stack.Stack, known map[string]*forge.CR) error {
	for _, b := range s.Branches {
		mr := known[b.Name]
		switch {
		case mr == nil:
			if !b.Merged {
				a.infof("  %s: no %s (run `git stack submit`)", b.Name, a.cr())
			}
			continue
		case mr.Merged():
			a.infof("  %s %s: merged", a.ref(mr.Number), b.Name)
			continue
		case !mr.Open():
			a.infof("  %s %s: %s", a.ref(mr.Number), b.Name, mr.State)
			continue
		}
		if target := s.Parent(b.Name); mr.Base != target {
			updated, err := c.UpdateCR(mr.Number, forge.Update{Base: &target})
			if err != nil {
				return err
			}
			known[b.Name], mr = updated, updated
			a.infof("Retargeted %s to %s", a.ref(mr.Number), target)
		}
		state := "open"
		if mr.Draft {
			state = "draft"
		}
		a.infof("  %s %s: %s", a.ref(mr.Number), b.Name, state)
	}
	return nil
}

// pruneMerged deletes local branches of merged layers, asking first unless
// force. A branch holding commits that the host never merged is kept.
func (a *App) pruneMerged(c *forge.Forge, s *stack.Stack, known map[string]*forge.CR, remote string, force bool) error {
	if !force && !a.Interactive {
		return nil
	}
	g := a.repo()
	var doomed []string
	for _, b := range s.Branches {
		if !b.Merged || !g.BranchExists(b.Name) {
			continue
		}
		if !a.fullyMerged(c, b, known, remote) {
			a.infof("Kept %s: it has commits that were not merged", b.Name)
			continue
		}
		doomed = append(doomed, b.Name)
	}
	if len(doomed) == 0 {
		return nil
	}
	if !force {
		ok, err := a.confirm(fmt.Sprintf("Delete %d local branch(es) of merged %ss (%v)?", len(doomed), a.cr(), doomed), false)
		if err != nil || !ok {
			return err
		}
	}
	cur, err := g.CurrentBranch()
	if err != nil {
		return err
	}
	for _, name := range doomed {
		owner, err := a.foreignOwner(name)
		if err != nil {
			return err
		}
		if owner != "" {
			a.infof("Kept %s: it is checked out in %s", name, owner)
			continue
		}
		if name == cur {
			dest := a.freeBranch(s)
			if dest == "" {
				a.infof("Kept %s: it is checked out here, and the stack's other branches are checked out elsewhere", name)
				continue
			}
			if err := g.Checkout(dest); err != nil {
				return err
			}
			a.infof("Switched to %s", dest)
			cur = dest
		}
		if _, err := g.Run("branch", "-D", name); err != nil {
			return err
		}
		a.infof("Deleted %s", name)
	}
	return nil
}

// fullyMerged reports whether b's local tip is contained in what reached
// The host: its remote branch, or the last commit of its merged PR.
func (a *App) fullyMerged(c *forge.Forge, b *stack.Branch, known map[string]*forge.CR, remote string) bool {
	g := a.repo()
	tip := git.Ref(b.Name)
	if sha, err := g.RevParse("refs/remotes/" + remote + "/" + b.Name); err == nil && g.IsAncestor(tip, sha) {
		return true
	}
	mr := known[b.Name]
	if mr == nil && b.CR != nil {
		mr, _ = c.GetCR(b.CR.Number)
	}
	return mr != nil && mr.HeadSHA != "" && g.IsAncestor(tip, mr.HeadSHA)
}

// freeBranch returns the bottom unmerged layer, else the trunk, unless it is
// checked out in another worktree; "" if neither is free.
func (a *App) freeBranch(s *stack.Stack) string {
	var candidates []string
	if bottom := s.Bottom(); bottom != nil {
		candidates = append(candidates, bottom.Name)
	}
	for _, name := range append(candidates, s.Trunk) {
		if owner, err := a.foreignOwner(name); err == nil && owner == "" {
			return name
		}
	}
	return ""
}

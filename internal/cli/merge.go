package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"gitlab.com/malephex/git-stack/internal/engine"
	"gitlab.com/malephex/git-stack/internal/exitcode"
	"gitlab.com/malephex/git-stack/internal/forge"
	"gitlab.com/malephex/git-stack/internal/stack"
	"gitlab.com/malephex/git-stack/pkg/protocol"
)

const (
	mergeMethodKey = "git-stack.merge-method"
	pollInterval   = 5 * time.Second
)

type mergeOptions struct {
	method                string
	merge, squash, rebase bool
	yes                   bool
	timeout               time.Duration
}

func newMergeCmd(app *App) *cobra.Command {
	var o mergeOptions
	cmd := &cobra.Command{
		Use:   "merge [<pr-number>]",
		Short: "Merge the stack's pull requests, bottom up",
		Long: `Merge the stack's pull requests from the bottom up to and including the
chosen one (by default the top of the stack).

PRs are merged one at a time: after each merge, the next layer is rebased onto
the updated trunk, pushed, retargeted and merged. If a merge fails, git-stack
stops and reports how far it got; the PRs merged so far stay merged.

Each merge is requested as an auto-merge where the host supports it: the host
merges at once when nothing is pending, or once checks pass (on GitLab, a
pipeline or merge train). git-stack waits for each merge to finish (see
--timeout).

Only basic state is checked up front (open and not a draft). The host enforces
approvals, checks and other rules when merging, and reports failures.

--squash squashes each PR, --rebase replays its commits onto the trunk and
--merge creates a merge commit. On GitLab, --merge and --rebase both keep the
commits and the project's merge method decides between them. Without a method
flag, the last one used is reused, else the repository's default.

Merging needs a provider; with provider "none" it fails.

In a terminal you can pick how far up to merge and the method, then confirm.
With --yes (or outside a terminal) the merge starts without prompting.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return app.runMerge(o, args)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.method, "merge-method", "", "merge method: merge, squash or rebase")
	f.BoolVar(&o.merge, "merge", false, "create a merge commit (on GitLab: keep commits, per project setting)")
	f.BoolVar(&o.squash, "squash", false, "squash each PR")
	f.BoolVar(&o.rebase, "rebase", false, "keep commits on top of the trunk")
	f.BoolVarP(&o.yes, "yes", "y", false, "merge without prompting")
	f.DurationVar(&o.timeout, "timeout", time.Hour, "how long to wait for each PR to merge (pipelines, merge trains)")
	return cmd
}

func (o mergeOptions) methodFlag() (string, error) {
	var set []string
	for name, on := range map[string]bool{"merge": o.merge, "squash": o.squash, "rebase": o.rebase} {
		if on {
			set = append(set, name)
		}
	}
	if o.method != "" {
		switch o.method {
		case "merge", "squash", "rebase":
			set = append(set, o.method)
		default:
			return "", exitcode.New(exitcode.InvalidArgs, "invalid --merge-method %q (want merge, squash or rebase)", o.method)
		}
	}
	switch {
	case len(set) == 0:
		return "", nil
	case len(set) == 2 && set[0] == set[1]:
		return set[0], nil
	case len(set) > 1:
		return "", exitcode.New(exitcode.InvalidArgs, "choose one merge method")
	}
	return set[0], nil
}

func (a *App) runMerge(o mergeOptions, args []string) error {
	method, err := o.methodFlag()
	if err != nil {
		return err
	}
	return a.mutate(func(f *stack.File) error {
		s, targetIID, err := a.mergeTarget(f, args)
		if err != nil {
			return err
		}
		remote := pickRemote("", s.Remote)
		c, err := a.forge(remote)
		if err != nil {
			return err
		}
		if err := requireCRs(c, "merge"); err != nil {
			return err
		}
		known, err := engine.SyncMRs(c, a.repo(), s)
		if err != nil {
			return err
		}
		candidates, err := a.mergeCandidates(s, known, targetIID)
		if err != nil {
			return err
		}
		if method == "" {
			method = a.defaultMergeMethod(c)
		}
		if a.Interactive && !o.yes {
			if candidates, method, err = a.mergeWizard(s, candidates, method); err != nil {
				return err
			}
		}
		_, _ = a.repo().Run("config", mergeMethodKey, method)

		st, err := a.store()
		if err != nil {
			return err
		}
		m := &merger{app: a, s: s, c: c, known: known, remote: remote, method: method, timeout: o.timeout,
			r: &engine.Rebaser{Repo: a.repo(), Store: st, Log: a.infof}}
		if err := m.r.Preflight(branchNames(s.Active())); err != nil {
			return err
		}
		if err := m.run(candidates); err != nil {
			if serr := a.saveNow(f); serr != nil {
				a.infof("Warning: could not save stack metadata: %v", serr)
			}
			return err
		}
		return nil
	})
}

// mergeTarget finds the stack and the PR to merge up to (0 = top of the stack).
func (a *App) mergeTarget(f *stack.File, args []string) (*stack.Stack, int, error) {
	if len(args) == 0 {
		s, _, err := a.currentStack(f)
		return s, 0, err
	}
	ref, ok := parseMRRef(args[0])
	if !ok {
		return nil, 0, exitcode.New(exitcode.InvalidArgs, "expected an %s number, got %q", a.cr(), args[0])
	}
	iid := ref.iid
	if ref.project == "" {
		if s, _ := findLocalMR(f, iid); s != nil {
			return s, iid, nil
		}
	}
	c, err := a.forge(a.defaultRemote())
	if err != nil {
		return nil, 0, err
	}
	if err := ref.check(c); err != nil {
		return nil, 0, err
	}
	if s, _ := findLocalMR(f, iid); s != nil {
		return s, iid, nil
	}
	// A stack that is only on the host: set it up locally without switching
	// branches. Only a stack recorded in a stack block: merging a chain merely
	// inferred from target branches could merge PRs that are not a stack.
	mr, err := c.GetCR(iid)
	if err != nil {
		if protocol.CodeOf(err) == protocol.CodeNotFound { // other errors stay provider failures
			return nil, 0, exitcode.Wrap(exitcode.NotInStack, fmt.Errorf("%s: %w", a.ref(iid), err))
		}
		return nil, 0, err
	}
	info, ok := forge.ParseBlock(mr.Body)
	if !ok {
		return nil, 0, exitcode.New(exitcode.NotInStack, "%s is not part of a stack; check it out with `git stack checkout %d` first if it is", a.ref(iid), iid)
	}
	s, err := a.setupRemoteStack(f, c, info, a.defaultRemote())
	return s, iid, err
}

// mergeCandidates returns the unmerged layers from the bottom up to targetIID
// (or the top), checking that each has an open, non-draft PR.
func (a *App) mergeCandidates(s *stack.Stack, known map[string]*forge.CR, targetIID int) ([]*stack.Branch, error) {
	var out []*stack.Branch
	for _, b := range s.Active() {
		mr := known[b.Name]
		switch {
		case mr == nil:
			return nil, exitcode.New(exitcode.Generic, "%s has no %s; run `git stack submit` first", b.Name, a.long())
		case !mr.Open():
			return nil, exitcode.New(exitcode.Generic, "%s (%s) is %s", a.ref(mr.Number), b.Name, mr.State)
		case mr.Draft:
			return nil, exitcode.New(exitcode.Generic, "%s (%s) is a draft; mark it ready first (e.g. `git stack submit --open`)", a.ref(mr.Number), b.Name)
		}
		out = append(out, b)
		if mr.Number == targetIID {
			return out, nil
		}
	}
	if targetIID != 0 {
		return nil, exitcode.New(exitcode.Generic, "%s is already merged", a.ref(targetIID))
	}
	if len(out) == 0 {
		return nil, exitcode.New(exitcode.Generic, "every %s in the stack is already merged", a.cr())
	}
	return out, nil
}

func (a *App) defaultMergeMethod(c *forge.Forge) string {
	if m, err := a.repo().Run("config", "--get", mergeMethodKey); err == nil && m != "" {
		return m
	}
	if p, err := c.GetRepo(); err == nil && p.DefaultMergeMethod != "" {
		return p.DefaultMergeMethod
	}
	return "merge"
}

func (a *App) mergeWizard(s *stack.Stack, candidates []*stack.Branch, method string) ([]*stack.Branch, string, error) {
	if len(candidates) > 1 {
		var opts []string
		for i := len(candidates) - 1; i >= 0; i-- {
			opts = append(opts, fmt.Sprintf("%s %s", a.ref(candidates[i].CR.Number), candidates[i].Name))
		}
		choice, err := a.choose(fmt.Sprintf("Merge up to and including which %s? (everything below it is merged too)", a.cr()), opts)
		if err != nil {
			return nil, "", err
		}
		candidates = candidates[:len(candidates)-choice]
	}
	methods := []string{"merge", "squash", "rebase"}
	for i, m := range methods {
		if m == method {
			methods[0], methods[i] = methods[i], methods[0]
		}
	}
	choice, err := a.choose("Merge method:", methods)
	if err != nil {
		return nil, "", err
	}
	method = methods[choice]
	var list []string
	for _, b := range candidates {
		list = append(list, a.ref(b.CR.Number))
	}
	ok, err := a.confirm(fmt.Sprintf("Merge %s into %s (%s)?", strings.Join(list, ", "), s.Trunk, method), true)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", exitcode.New(exitcode.Generic, "merge cancelled")
	}
	return candidates, method, nil
}

type merger struct {
	app     *App
	s       *stack.Stack
	c       *forge.Forge
	r       *engine.Rebaser
	known   map[string]*forge.CR
	remote  string
	method  string
	timeout time.Duration
}

func (m *merger) run(candidates []*stack.Branch) error {
	var merged []string
	pendingDelete := ""
	fail := func(err error) error {
		if len(merged) > 0 {
			m.app.infof("Merged so far: %s. The rest of the stack is unchanged.", strings.Join(merged, ", "))
		}
		return err
	}
	for i, b := range candidates {
		// After a merge the next layer is restacked onto the trunk. The first
		// one too if its PR still targets a merged layer (an earlier merge run
		// stopped, or a layer was merged on the host): the host would merge it into
		// that stale branch instead of the trunk.
		if i > 0 || m.known[b.Name].Base != m.s.Parent(b.Name) {
			if err := m.restack(b); err != nil {
				return fail(err)
			}
		}
		if pendingDelete != "" {
			m.deleteRemoteBranch(pendingDelete)
			pendingDelete = ""
		}
		mr, err := m.mergeOne(b)
		if err != nil {
			return fail(err)
		}
		b.Merged = true
		merged = append(merged, m.app.ref(mr.Number))
		m.app.infof("Merged %s (%s) into %s", m.app.ref(mr.Number), b.Name, m.s.Trunk)
		if m.known[b.Name].DeleteHead {
			pendingDelete = b.Name
		}
	}
	// Restack whatever is left above the merged layers. The descriptions are
	// read again: the merges may have taken long enough for them to be edited.
	if rest := m.s.Bottom(); rest != nil {
		if err := m.restack(rest); err != nil {
			return fail(err)
		}
		if err := engine.UpdateBlocks(m.c, m.s, map[string]*forge.CR{}); err != nil {
			return fail(err)
		}
	} else if _, err := m.r.UpdateFromRemote(m.s, m.remote); err != nil {
		return fail(err)
	}
	if pendingDelete != "" {
		m.deleteRemoteBranch(pendingDelete)
	}
	m.app.infof("Merged %s into %s", strings.Join(merged, ", "), m.s.Trunk)
	return nil
}

// restack rebases b and everything above it onto the trunk, pushes, and
// points b's PR at the trunk.
func (m *merger) restack(b *stack.Branch) error {
	if err := m.r.Start(m.s, engine.RebaseOptions{Anchor: b.Name, Upstack: true, Remote: m.remote, AbortOnConflict: true}); err != nil {
		return err
	}
	if err := engine.Push(m.app.repo(), m.remote, branchNames(m.s.Active())); err != nil {
		return err
	}
	mr := m.known[b.Name]
	if target := m.s.Parent(b.Name); mr != nil && mr.Base != target {
		updated, err := m.c.UpdateCR(mr.Number, forge.Update{Base: &target})
		if err != nil {
			return err
		}
		m.known[b.Name] = updated
		m.app.infof("Retargeted %s to %s", m.app.ref(mr.Number), target)
	}
	return nil
}

// mergeOne merges b's PR at the local branch tip and waits until it is merged.
func (m *merger) mergeOne(b *stack.Branch) (*forge.CR, error) {
	iid := m.known[b.Name].Number
	sha, err := m.app.repo().RevParse("refs/heads/" + b.Name)
	if err != nil {
		return nil, err
	}
	deadline := m.app.Now().Add(m.timeout)
	mr, err := m.c.GetCR(iid)
	for err == nil && mr.Checking && m.app.Now().Before(deadline) {
		m.app.Sleep(pollInterval)
		mr, err = m.c.GetCR(iid)
	}
	if err != nil {
		return nil, err
	}

	m.app.infof("Merging %s (%s)...", m.app.ref(iid), b.Name)
	if mr, err = m.c.Merge(iid, m.method, sha); err != nil {
		return nil, fmt.Errorf("%s could not be merged: %w", m.app.ref(iid), err)
	}
	waiting := false
	for !mr.Merged() {
		locked := mr.State == forge.StateMerging // the host is performing the merge
		switch {
		case !locked && mr.State != forge.StateOpen:
			return nil, exitcode.New(exitcode.ProviderFailure, "%s was %s instead of merged", m.app.ref(iid), mr.State)
		case !locked && !mr.AutoMerge:
			reason := mr.MergeError
			if reason == "" {
				reason = "auto-merge was cancelled"
			}
			return nil, exitcode.New(exitcode.ProviderFailure, "%s did not merge: %s", m.app.ref(iid), reason)
		case !m.app.Now().Before(deadline):
			return nil, exitcode.New(exitcode.ProviderFailure, "timed out waiting for %s to merge; it is still set to auto-merge", m.app.ref(iid))
		}
		if !waiting {
			m.app.infof("Waiting for %s to merge (pipeline or merge train)...", m.app.ref(iid))
			waiting = true
		}
		m.app.Sleep(pollInterval)
		if mr, err = m.c.GetCR(iid); err != nil {
			return nil, err
		}
	}
	return mr, nil
}

func (m *merger) deleteRemoteBranch(branch string) {
	if _, err := m.app.repo().Run("push", "--quiet", m.remote, "--delete", branch); err != nil {
		m.app.infof("Warning: could not delete %s on %s: %v", branch, m.remote, err)
		return
	}
	m.app.infof("Deleted %s on %s", branch, m.remote)
}

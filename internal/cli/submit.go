package cli

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/mhvelplund/git-stack/internal/engine"
	"github.com/mhvelplund/git-stack/internal/exitcode"
	"github.com/mhvelplund/git-stack/internal/forge"
	"github.com/mhvelplund/git-stack/internal/stack"
	"github.com/mhvelplund/git-stack/internal/tui"
)

type submitOptions struct {
	auto, open bool
	remote     string
}

func newSubmitCmd(app *App) *cobra.Command {
	var o submitOptions
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Push the stack and create or update its pull requests",
		Long: `Push every unmerged branch and create or update one pull request per
branch, each targeting the branch below it. Every PR in the stack gets a
block in its description that lists the whole stack.

Steps:
  1. Pushes all branches to the remote
  2. Creates PRs for branches that have none
  3. Fixes target branches of existing PRs
  4. Updates the stack block in every PR description

In a terminal, a full-screen editor opens first: choose which new branches get
an PR (skipped ones are still pushed), and edit each title, description and
ready/draft state; ^s submits, q cancels without pushing. New PRs default to
ready in the editor.

With --auto (or outside a terminal), titles come from the commits and new PRs
are created as drafts unless --open is passed. --open also marks existing
draft PRs as ready.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return app.runSubmit(o)
		},
	}
	cmd.Flags().BoolVar(&o.auto, "auto", false, "use generated titles without prompting")
	cmd.Flags().BoolVar(&o.open, "open", false, "mark new and existing PRs as ready for review")
	cmd.Flags().StringVar(&o.remote, "remote", "", "remote to push to (default: the stack's remote)")
	return cmd
}

func (a *App) runSubmit(o submitOptions) error {
	return a.mutate(func(f *stack.File) error {
		s, _, err := a.currentStack(f)
		if err != nil {
			return err
		}
		remote := pickRemote(o.remote, s.Remote)
		c, err := a.forge(remote)
		if err != nil {
			return err
		}
		known, err := engine.SyncMRs(c, a.repo(), s)
		if err != nil {
			return err
		}
		pruneFullyMerged(s)
		active := s.Active()
		if len(active) == 0 {
			a.infof("All branches are merged; nothing to submit")
			return nil
		}
		g := a.repo()
		var plan map[string]*tui.SubmitNode
		if a.Interactive && !o.auto && c.HasCRs() {
			if plan, err = a.submitEditor(s, known); err != nil {
				return err
			}
		}
		_ = g.Fetch(remote) // refresh tracking refs for --force-with-lease; may fail for new remotes
		if err := engine.Push(g, remote, branchNames(active)); err != nil {
			return err
		}
		a.infof("Pushed %d branch(es) to %s", len(active), remote)
		if !c.HasCRs() {
			a.infof("No provider supports %s, so no pull requests were opened (see --provider)", hostOrRemote(c))
			return nil
		}

		var errs []error
		for _, b := range active {
			if err := a.ensureMR(c, s, b, known, o, plan); err != nil {
				a.infof("Warning: %s: %v", b.Name, err)
				errs = append(errs, err)
			}
		}
		if err := engine.UpdateBlocks(c, s, known); err != nil {
			errs = append(errs, err)
		}
		if len(errs) > 0 {
			// Keep what was created: save the metadata, then report failure.
			if serr := a.saveNow(f); serr != nil {
				errs = append(errs, serr)
			}
			return exitcode.Wrap(exitcode.ProviderFailure, errors.Join(errs...))
		}
		return nil
	})
}

// pruneFullyMerged drops merged layers once no PR in the stack is still open,
// so new layers start a fresh stack on the trunk.
func pruneFullyMerged(s *stack.Stack) {
	for _, b := range s.Active() {
		if b.CR != nil {
			return
		}
	}
	s.Branches = s.Active()
}

func (a *App) ensureMR(c *forge.Forge, s *stack.Stack, b *stack.Branch, known map[string]*forge.CR, o submitOptions, plan map[string]*tui.SubmitNode) error {
	target := s.Parent(b.Name)
	mr := known[b.Name]
	if mr == nil || !mr.Open() {
		if plan != nil {
			n := plan[b.Name]
			if n == nil || !n.Include {
				a.infof("Pushed %s without an %s", b.Name, a.cr())
				return nil
			}
			return a.createMR(c, b, target, known, n.Title, n.Body, n.Draft)
		}
		title, body := a.defaultMRText(target, b.Name)
		return a.createMR(c, b, target, known, title, body, !o.open)
	}
	if mr.AutoMerge {
		if err := c.CancelAutoMerge(mr.Number); err != nil {
			return err
		}
		a.infof("Disabled auto-merge on %s (incompatible with stacked %ss)", a.ref(mr.Number), a.cr())
	}
	if mr.Base != target {
		updated, err := c.UpdateCR(mr.Number, forge.Update{Base: &target})
		if err != nil {
			return err
		}
		known[b.Name] = updated
		a.infof("Retargeted %s to %s", a.ref(mr.Number), target)
	} else {
		a.infof("%s for %s is up to date", a.ref(mr.Number), b.Name)
	}
	if o.open && known[b.Name].Draft {
		updated, err := c.MarkReady(known[b.Name].Number)
		if err != nil {
			return err
		}
		known[b.Name] = updated
		a.infof("Marked %s as ready", a.ref(mr.Number))
	}
	return nil
}

func (a *App) createMR(c *forge.Forge, b *stack.Branch, target string, known map[string]*forge.CR, title, body string, draft bool) error {
	mr, err := c.CreateCR(forge.NewCR{Head: b.Name, Base: target, Title: title, Body: body, Draft: draft})
	if err != nil {
		return err
	}
	known[b.Name] = mr
	b.CR = &stack.CR{Number: mr.Number, URL: mr.URL}
	a.infof("Created %s for %s: %s", a.ref(mr.Number), b.Name, mr.URL)
	return nil
}

// defaultMRText derives a title from the commits and uses the project's PR
// template, if any, as the description.
func (a *App) defaultMRText(target, branch string) (string, string) {
	title, body := engine.DefaultTitleBody(a.repo(), target, branch)
	if tmpl := a.mrTemplate(); tmpl != "" {
		body = tmpl
	}
	return title, body
}

// submitEditor opens the full-screen editor and returns the plan for new PRs,
// keyed by branch. Nothing has been pushed yet, so cancelling changes nothing.
func (a *App) submitEditor(s *stack.Stack, known map[string]*forge.CR) (map[string]*tui.SubmitNode, error) {
	var nodes []*tui.SubmitNode
	for _, b := range s.Active() {
		n := &tui.SubmitNode{Branch: b.Name, Target: s.Parent(b.Name)}
		if mr := known[b.Name]; mr != nil && mr.Open() {
			n.Number, n.URL, n.State = mr.Number, mr.URL, "open"
			if mr.Draft {
				n.State = "draft"
			}
		} else {
			n.Include = true
			n.Title, n.Body = a.defaultMRText(n.Target, b.Name)
		}
		nodes = append(nodes, n)
	}
	e := tui.NewSubmitEditor(nodes)
	e.OpenURL = a.openURL
	e.Ref, e.CR = a.ref, a.cr()
	if _, err := a.runTUI(e); err != nil {
		return nil, err
	}
	if !e.Submitted {
		return nil, exitcode.New(exitcode.Generic, "submit cancelled; nothing was pushed")
	}
	plan := map[string]*tui.SubmitNode{}
	for _, n := range e.Nodes {
		if e.LinkOnly {
			n.Include = false
		}
		plan[n.Branch] = n
	}
	return plan, nil
}

// mrTemplate returns the project's default pull request template, if any.
func (a *App) mrTemplate() string {
	top, err := a.repo().TopLevel()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(top, ".gitlab", "merge_request_templates", "Default.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

// saveNow persists f mid-command, for partial successes that still return an error.
func (a *App) saveNow(f *stack.File) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	return st.Save(f)
}

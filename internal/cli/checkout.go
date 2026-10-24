package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mhvelplund/git-stack/internal/exitcode"
	"github.com/mhvelplund/git-stack/internal/forge"
	"github.com/mhvelplund/git-stack/internal/stack"
	"github.com/mhvelplund/git-stack/internal/tui"
)

func newCheckoutCmd(app *App) *cobra.Command {
	var printPath bool
	cmd := &cobra.Command{
		Use:   "checkout [<pr-number> | <pr-url> | <branch>]",
		Short: "Check out a stack by PR number, PR URL, or branch name",
		Long: `Check out a stack by pull request number (123 or !123), PR URL, or branch name.

A number is looked up in the local stacks first, then on the host. A stack found
on the host (from the stack block in the PR description, or by following target
branches) is set up locally: missing branches are created from the remote and
existing ones fast-forwarded. A branch name is looked up locally first, then
through its open PR on the host.

Without arguments in a terminal, pick from the local stacks and the stacks
found on the host.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}
			return app.runCheckout(arg, printPath)
		},
	}
	cmd.Flags().BoolVar(&printPath, "print-path", false, "print the path of the worktree that has the target branch checked out")
	return cmd
}

func (a *App) runCheckout(arg string, printPath bool) error {
	var target string
	err := a.mutate(func(f *stack.File) error {
		var err error
		target, err = a.resolveCheckout(f, arg)
		return err
	})
	if err != nil {
		return err
	}
	return a.goTo(target, printPath)
}

// resolveCheckout returns the branch to check out, setting up a remote stack if needed.
func (a *App) resolveCheckout(f *stack.File, arg string) (string, error) {
	if arg == "" {
		return a.pickStack(f)
	}
	if ref, ok := parseMRRef(arg); ok {
		if ref.project == "" {
			if _, b := findLocalMR(f, ref.iid); b != nil {
				return b.Name, nil
			}
		}
		c, err := a.forge(a.defaultRemote())
		if err != nil {
			return "", err
		}
		if err := ref.check(c); err != nil {
			return "", err
		}
		if _, b := findLocalMR(f, ref.iid); b != nil {
			return b.Name, nil
		}
		mr, err := c.GetCR(ref.iid)
		if err == nil {
			return a.checkoutRemote(f, c, mr)
		}
		if a.repo().ValidBranchName(arg) && !strings.HasPrefix(arg, "!") {
			return a.checkoutBranch(f, arg) // e.g. a branch named "123"
		}
		return "", err
	}
	return a.checkoutBranch(f, arg)
}

func (a *App) checkoutBranch(f *stack.File, branch string) (string, error) {
	if f.Find(branch) != nil {
		return branch, nil
	}
	c, err := a.forge(a.defaultRemote())
	if err != nil {
		return "", err
	}
	mr, err := c.FindCRForBranch(branch)
	if err != nil {
		return "", err
	}
	if mr == nil || !mr.Open() {
		return "", exitcode.New(exitcode.NotInStack, "%s is not in a local stack and has no open %s on %s", branch, a.cr(), a.host())
	}
	return a.checkoutRemote(f, c, mr)
}

func (a *App) checkoutRemote(f *stack.File, c *forge.Forge, mr *forge.CR) (string, error) {
	info, err := remoteStackFor(c, mr)
	if err != nil {
		return "", err
	}
	if _, err := a.setupRemoteStack(f, c, info, a.defaultRemote()); err != nil {
		return "", err
	}
	return mr.Head, nil
}

func (a *App) defaultRemote() string {
	r, err := a.repo().DefaultRemote()
	if err != nil {
		return "origin"
	}
	return r
}

// stackChoice is one entry in the stack picker.
type stackChoice struct {
	label  string
	local  *stack.Stack
	remote *forge.StackInfo
}

// stackChoices lists local stacks and open stacks found on the host, without
// duplicates and without fully merged stacks.
func (a *App) stackChoices(f *stack.File) ([]stackChoice, *forge.Forge) {
	var out []stackChoice
	tracked := map[int]bool{}
	for _, s := range f.Stacks {
		if s.Top() == nil {
			continue
		}
		for _, b := range s.Branches {
			if b.CR != nil {
				tracked[b.CR.Number] = true
			}
		}
		out = append(out, stackChoice{label: fmt.Sprintf("%s ← %s", s.Trunk, strings.Join(branchNames(s.Active()), " ← ")), local: s})
	}
	c, err := a.forge(a.defaultRemote())
	if err != nil {
		return out, nil
	}
	mrs, err := searchStacks(c)
	if err != nil {
		a.infof("Warning: could not list stacks on %s: %v", a.host(), err)
		return out, c
	}
	for _, mr := range mrs {
		info, ok := forge.ParseBlock(mr.Body)
		if !ok || len(info.Items) == 0 || tracked[info.Items[0].Number] {
			continue
		}
		var parts []string
		for _, m := range info.Items {
			tracked[m.Number] = true
			parts = append(parts, fmt.Sprintf("%s %s", a.ref(m.Number), m.Branch))
		}
		out = append(out, stackChoice{label: fmt.Sprintf("%s ← %s", info.Trunk, strings.Join(parts, " ← ")), remote: info})
	}
	return out, c
}

func (a *App) pickStack(f *stack.File) (string, error) {
	if !a.Interactive {
		return "", exitcode.New(exitcode.InvalidArgs, "pass a %s number, %s URL or branch name", a.cr(), a.cr())
	}
	choices, c := a.stackChoices(f)
	if len(choices) == 0 {
		return "", exitcode.New(exitcode.NotInStack, "no stacks found locally or on %s", a.host())
	}
	items := make([]tui.Item, len(choices))
	for i, ch := range choices {
		tab := 1
		if ch.remote != nil {
			tab = 2
		}
		items[i] = tui.Item{Title: ch.label, Tab: tab}
	}
	p := tui.NewPicker("Check out a stack", items)
	p.Tabs = []string{"All", "Local", "Remote"}
	if _, err := a.runTUI(p); err != nil {
		return "", err
	}
	if p.Chosen < 0 {
		return "", exitcode.New(exitcode.Generic, "checkout cancelled")
	}
	ch := choices[p.Chosen]
	if ch.local != nil {
		return ch.local.Top().Name, nil
	}
	s, err := a.setupRemoteStack(f, c, ch.remote, a.defaultRemote())
	if err != nil {
		return "", err
	}
	if top := s.Top(); top != nil {
		return top.Name, nil
	}
	return s.Trunk, nil
}

// searchStacks returns the open CRs that carry a stack block, including
// blocks written by glab-stax.
func searchStacks(c *forge.Forge) ([]*forge.CR, error) {
	var out []*forge.CR
	seen := map[int]bool{}
	for _, marker := range []string{forge.Marker, forge.LegacyMarker} {
		crs, err := c.SearchCRs(marker)
		if err != nil {
			return nil, err
		}
		for _, cr := range crs {
			if !seen[cr.Number] {
				seen[cr.Number] = true
				out = append(out, cr)
			}
		}
	}
	return out, nil
}

package cli

import (
	"github.com/spf13/cobra"

	"github.com/mhvelplund/git-stack/internal/exitcode"
	"github.com/mhvelplund/git-stack/internal/forge"
	"github.com/mhvelplund/git-stack/internal/stack"
)

func newUnstackCmd(app *App) *cobra.Command {
	var local bool
	cmd := &cobra.Command{
		Use:     "unstack [<pr-number>]",
		Aliases: []string{"delete"},
		Short:   "Remove a stack locally and on the host",
		Long: `Remove a stack from local tracking and remove the stack block from its
pull requests on the host. Branches and PRs themselves are kept.

With no argument, the current stack is unstacked. With an PR number, the stack
containing that PR is unstacked on the host, and locally too if it is tracked.
--local only removes local tracking.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return app.runUnstack(args, local)
		},
	}
	cmd.Flags().BoolVar(&local, "local", false, "only remove local tracking")
	return cmd
}

func (a *App) runUnstack(args []string, localOnly bool) error {
	return a.mutate(func(f *stack.File) error {
		var s *stack.Stack
		var iids []int
		if len(args) == 1 {
			ref, ok := parseMRRef(args[0])
			if !ok {
				return exitcode.New(exitcode.InvalidArgs, "expected an %s number, got %q", a.cr(), args[0])
			}
			iid := ref.iid
			if ref.project != "" {
				c, err := a.forge(a.defaultRemote())
				if err != nil {
					return err
				}
				if err := ref.check(c); err != nil {
					return err
				}
			}
			s, _ = findLocalMR(f, iid)
			if s == nil && localOnly {
				return exitcode.New(exitcode.NotInStack, "%s is not part of a local stack", a.ref(iid))
			}
			var c *forge.Forge
			if !localOnly {
				var err error
				if c, err = a.forge(a.defaultRemote()); err != nil {
					return err
				}
				if !c.HasCRs() && s == nil {
					return requireCRs(c, "unstacking a stack that is not tracked locally")
				}
			}
			if c != nil && c.HasCRs() {
				mr, err := c.GetCR(iid)
				if err != nil {
					return err
				}
				info, ok := forge.ParseBlock(mr.Body)
				if !ok && s == nil {
					return exitcode.New(exitcode.NotInStack, "%s is not part of a stack", a.ref(iid))
				}
				if ok {
					for _, m := range info.Items {
						iids = append(iids, m.Number)
					}
				}
			}
		} else {
			var err error
			if s, _, err = a.currentStack(f); err != nil {
				return err
			}
		}
		if s != nil {
			for _, b := range s.Branches {
				if b.CR != nil {
					iids = append(iids, b.CR.Number)
				}
			}
		}
		if !localOnly && len(iids) > 0 {
			remote := a.defaultRemote()
			if s != nil {
				remote = pickRemote("", s.Remote)
			}
			c, err := a.forge(remote)
			if err != nil {
				return err
			}
			// Without a provider there is nothing on the host to clean up.
			if c.HasCRs() {
				if err := a.stripBlocks(c, iids); err != nil {
					return err
				}
			}
		}
		if s != nil {
			f.Remove(s)
			a.infof("Removed local tracking for the stack on %s", s.Trunk)
		}
		return nil
	})
}

// stripBlocks removes the stack block from each open PR.
func (a *App) stripBlocks(c *forge.Forge, iids []int) error {
	seen := map[int]bool{}
	for _, iid := range iids {
		if seen[iid] {
			continue
		}
		seen[iid] = true
		mr, err := c.GetCR(iid)
		if err != nil {
			return err
		}
		if !mr.Open() {
			continue
		}
		if desc := forge.StripBlock(mr.Body); desc != mr.Body {
			if _, err := c.UpdateCR(iid, forge.Update{Body: &desc}); err != nil {
				return err
			}
			a.infof("Removed the stack block from %s", a.ref(iid))
		}
	}
	if c.Info.NativeStacks {
		return c.Unstack(iids)
	}
	return nil
}

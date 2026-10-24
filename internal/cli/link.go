package cli

import (
	"github.com/spf13/cobra"

	"github.com/mhvelplund/git-stack/internal/engine"
	"github.com/mhvelplund/git-stack/internal/exitcode"
	"github.com/mhvelplund/git-stack/internal/forge"
)

type linkOptions struct {
	base, remote string
	open         bool
}

func newLinkCmd(app *App) *cobra.Command {
	var o linkOptions
	cmd := &cobra.Command{
		Use:   "link <branch-or-pr> [...]",
		Short: "Link branches or PRs into a stack on the host",
		Long: `Create or extend a stack on the host from branches or PR numbers/URLs, given
bottom to top. Local stack tracking is not read or changed.

Branches are pushed first. Branches without an PR get one, targeting the item
below. Existing PRs with the wrong target branch are retargeted. Linking only
adds: PRs already in the stack are never removed from it.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return app.runLink(o, args)
		},
	}
	cmd.Flags().StringVar(&o.base, "base", "", "target branch of the bottom item (default: the remote's default branch)")
	cmd.Flags().BoolVar(&o.open, "open", false, "create new PRs as ready for review and mark drafts ready")
	cmd.Flags().StringVar(&o.remote, "remote", "", "remote to push to (default: auto-detected)")
	return cmd
}

func (a *App) runLink(o linkOptions, args []string) error {
	g := a.repo()
	remote := o.remote
	if remote == "" {
		remote = a.defaultRemote()
	}
	c, err := a.forge(remote)
	if err != nil {
		return err
	}
	if err := requireCRs(c, "link"); err != nil {
		return err
	}

	// Resolve every argument to a branch, and an PR when one exists.
	type item struct {
		branch string
		mr     *forge.CR
	}
	var items []item
	var toPush []string
	for _, arg := range args {
		if ref, ok := parseMRRef(arg); ok && !g.BranchExists(arg) {
			if err := ref.check(c); err != nil {
				return err
			}
			mr, err := c.GetCR(ref.iid)
			if err != nil {
				return err
			}
			items = append(items, item{mr.Head, mr})
			continue
		}
		if !g.BranchExists(arg) {
			return exitcode.New(exitcode.InvalidArgs, "%q is neither a local branch nor an %s", arg, a.cr())
		}
		mr, err := c.FindCRForBranch(arg)
		if err != nil {
			return err
		}
		items = append(items, item{branch: arg, mr: mr})
		toPush = append(toPush, arg)
	}

	// Merge into any existing stack: keep its PRs, insert ours in order.
	var info forge.StackInfo
	existing := false
	for _, it := range items {
		if it.mr == nil {
			continue
		}
		if b, ok := forge.ParseBlock(it.mr.Body); ok {
			info, existing = *b, true
			break
		}
	}
	// The bottom item targets --base. Without it, an item already in the stack
	// keeps the layer below it, and a new one goes on the stack's trunk, or
	// the default branch for a new stack.
	base := o.base
	if base == "" && existing {
		base = info.Trunk
		if first := items[0].mr; first != nil {
			for j, m := range info.Items {
				if m.Number == first.Number && j > 0 {
					base = info.Items[j-1].Branch
				}
			}
		}
	}
	if base == "" {
		if base, err = g.DefaultBranch(remote); err != nil {
			return exitcode.Wrap(exitcode.InvalidArgs, err)
		}
	}

	if err := engine.Push(g, remote, toPush); err != nil {
		return err
	}

	target := base
	for i := range items {
		it := &items[i]
		switch {
		case it.mr == nil || !it.mr.Open():
			title, body := engine.DefaultTitleBody(g, target, it.branch)
			if it.mr, err = c.CreateCR(forge.NewCR{Head: it.branch, Base: target, Title: title, Body: body, Draft: !o.open}); err != nil {
				return err
			}
			a.infof("Created %s for %s", a.ref(it.mr.Number), it.branch)
		case it.mr.Base != target:
			if it.mr, err = c.UpdateCR(it.mr.Number, forge.Update{Base: &target}); err != nil {
				return err
			}
			a.infof("Retargeted %s to %s", a.ref(it.mr.Number), target)
		}
		if o.open && it.mr.Draft {
			if it.mr, err = c.MarkReady(it.mr.Number); err != nil {
				return err
			}
		}
		target = it.branch
	}

	if !existing {
		info = forge.StackInfo{Trunk: base}
	}
	pos := -1
	for _, it := range items {
		idx := -1
		for j, m := range info.Items {
			if m.Number == it.mr.Number {
				idx = j
			}
		}
		if idx < 0 {
			idx = pos + 1
			entry := forge.StackItem{Number: it.mr.Number, Branch: it.branch}
			info.Items = append(info.Items[:idx], append([]forge.StackItem{entry}, info.Items[idx:]...)...)
		}
		pos = idx
	}
	if len(info.Items) > 0 && info.Items[0].Number == items[0].mr.Number {
		info.Trunk = base // the bottom item now targets base
	}
	// The PRs handled above are current; only the rest are fetched.
	current := map[int]*forge.CR{}
	for _, it := range items {
		current[it.mr.Number] = it.mr
	}
	for _, m := range info.Items {
		if current[m.Number] == nil {
			if current[m.Number], err = c.GetCR(m.Number); err != nil {
				return err
			}
		}
	}
	urls := map[int]string{}
	for n, mr := range current {
		urls[n] = mr.URL
	}
	blk := engine.Block(c, urls)
	for _, m := range info.Items {
		mr := current[m.Number]
		if !mr.Open() {
			continue
		}
		desc, err := engine.SetBody(c, blk, info, mr.Body, mr.Number)
		if err != nil {
			return err
		}
		if desc != mr.Body {
			if _, err := c.UpdateCR(mr.Number, forge.Update{Body: &desc}); err != nil {
				return err
			}
		}
	}
	a.infof("Linked %d %s(s) into a stack on %s", len(info.Items), a.cr(), info.Trunk)
	return nil
}

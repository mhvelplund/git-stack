package cli

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/mhvelplund/git-stack/internal/engine"
	"github.com/mhvelplund/git-stack/internal/exitcode"
	"github.com/mhvelplund/git-stack/internal/forge"
	"github.com/mhvelplund/git-stack/internal/git"
	"github.com/mhvelplund/git-stack/internal/stack"
)

const maxChain = 50

// crURLRe matches the path of a CR's web page on the host (/-/merge_requests/N),
// GitHub (/pull/N), Azure DevOps (/pullrequest/N) and Bitbucket
// (/pull-requests/N); what comes before is the repository path.
var crURLRe = regexp.MustCompile(`^/(.*?)/(?:-/merge_requests|pull|pullrequest|pull-requests)/(\d+)(?:/.*)?$`)

// mrRef is a parsed CR reference.
type mrRef struct {
	iid     int
	project string // repository path of a CR URL; "" for "123", "#123" and "!123"
}

// parseMRRef recognises "123", "#123", "!123" and CR URLs.
func parseMRRef(arg string) (mrRef, bool) {
	if u, err := url.Parse(arg); err == nil && u.Host != "" {
		m := crURLRe.FindStringSubmatch(u.Path)
		if m == nil {
			return mrRef{}, false
		}
		n, err := strconv.Atoi(m[2])
		return mrRef{iid: n, project: m[1]}, err == nil && n > 0
	}
	n, err := strconv.Atoi(strings.TrimLeft(arg, "!#"))
	return mrRef{iid: n}, err == nil && n > 0
}

// check fails if the reference is a URL of another repository than c's: its
// number would otherwise be applied to a different CR of this repository.
func (r mrRef) check(c *forge.Forge) error {
	if r.project != "" && c.Repo.Path != "" && !strings.EqualFold(r.project, c.Repo.Path) {
		return exitcode.New(exitcode.InvalidArgs, "that %s belongs to %s, but this repository is %s", c.Short(), r.project, c.Repo.Path)
	}
	return nil
}

// findLocalMR returns the local stack and branch that track PR iid.
func findLocalMR(f *stack.File, iid int) (*stack.Stack, *stack.Branch) {
	for _, s := range f.Stacks {
		for _, b := range s.Branches {
			if b.CR != nil && b.CR.Number == iid {
				return s, b
			}
		}
	}
	return nil, nil
}

// remoteStackFor returns the stack containing mr, from its description block
// or, failing that, by following target branches through open PRs. The walk
// down stops at a protected or default branch: a branch like main with its
// own release PR is a trunk, not a layer.
func remoteStackFor(c *forge.Forge, mr *forge.CR) (*forge.StackInfo, error) {
	if info, ok := forge.ParseBlock(mr.Body); ok {
		return info, nil
	}
	chain := []*forge.CR{mr}
	seen := map[int]bool{mr.Number: true}
	for len(chain) < maxChain {
		target := chain[0].Base
		if b, err := c.GetBranch(target); err == nil && (b.Protected || b.Default) {
			break
		}
		below, err := c.FindCRForBranch(target)
		if err != nil {
			return nil, err
		}
		if below == nil || !below.Open() || seen[below.Number] {
			break
		}
		seen[below.Number] = true
		chain = append([]*forge.CR{below}, chain...)
	}
	for len(chain) < maxChain {
		above, err := c.ListCRsTargeting(chain[len(chain)-1].Head)
		if err != nil {
			return nil, err
		}
		if len(above) != 1 || seen[above[0].Number] {
			break // none, several we cannot order, or a loop
		}
		seen[above[0].Number] = true
		chain = append(chain, above[0])
	}
	info := &forge.StackInfo{Trunk: chain[0].Base}
	for _, m := range chain {
		info.Items = append(info.Items, forge.StackItem{Number: m.Number, Branch: m.Head})
	}
	return info, nil
}

// setupRemoteStack makes a local stack for info, creating branches from the
// remote as needed. An existing local stack with the same layers is reused.
func (a *App) setupRemoteStack(f *stack.File, c *forge.Forge, info *forge.StackInfo, remote string) (*stack.Stack, error) {
	g := a.repo()
	if err := g.Fetch(remote); err != nil {
		return nil, err
	}
	var existing *stack.Stack
	for _, item := range info.Items {
		if s := f.Find(item.Branch); s != nil {
			if existing != nil && existing != s {
				return nil, exitcode.New(exitcode.Ambiguous, "the stack's branches belong to several local stacks; run `git stack unstack --local` first")
			}
			existing = s
		}
	}
	if existing != nil {
		if rel := engine.Compare(existing, info); rel == engine.InSync || rel == engine.LocalAhead {
			return existing, nil
		}
		if !a.Interactive {
			return nil, exitcode.New(exitcode.Ambiguous, "a local stack with some of these branches differs from the stack on %s; run in a terminal to replace it", a.host())
		}
		ok, err := a.confirm(fmt.Sprintf("A local stack with some of these branches differs from the one on %s. Replace it with %s's?", a.host(), a.host()), false)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, exitcode.New(exitcode.Ambiguous, "checkout cancelled")
		}
		f.Remove(existing)
	}
	if err := a.ensureLocalBranch(remote, info.Trunk); err != nil {
		return nil, err
	}
	s := &stack.Stack{Trunk: info.Trunk, Remote: remote}
	below := git.Ref(info.Trunk) // where the next layer's own commits start
	for _, item := range info.Items {
		mr, err := c.GetCR(item.Number)
		if err != nil {
			return nil, err
		}
		b := &stack.Branch{Name: item.Branch, CR: &stack.CR{Number: mr.Number, URL: mr.URL}, Merged: mr.Merged()}
		if err := a.syncLocalBranch(remote, item.Branch, b.Merged); err != nil {
			return nil, err
		}
		b.Base = a.forkBase(below, item.Branch)
		below = layerTip(g, item.Branch, mr)
		s.Branches = append(s.Branches, b)
	}
	f.Stacks = append(f.Stacks, s)
	if err := g.EnableRerere(); err != nil {
		return nil, err
	}
	a.infof("Set up stack on %s: %s", s.Trunk, strings.Join(branchNames(s.Branches), " → "))
	return s, nil
}

// forkBase returns where branch forks from below (a revision), or "" if
// either is unknown locally.
func (a *App) forkBase(below, branch string) string {
	g := a.repo()
	if below == "" || !g.BranchExists(branch) {
		return ""
	}
	base, _ := g.MergeBase(below, git.Ref(branch))
	return base
}

// layerTip returns a revision for a layer's tip: its local branch or, for a
// merged layer whose branch is gone, the commit its PR ended on. The layer
// above starts there, and rebases need that cut point.
func layerTip(g *git.Repo, branch string, mr *forge.CR) string {
	if g.BranchExists(branch) {
		return git.Ref(branch)
	}
	if mr != nil && mr.HeadSHA != "" {
		return mr.HeadSHA
	}
	return ""
}

// syncLocalBranch creates branch from the remote, or fast-forwards it. A
// diverged local branch is kept as is. Merged branches deleted upstream are skipped.
func (a *App) syncLocalBranch(remote, branch string, merged bool) error {
	g := a.repo()
	remoteSHA, err := g.RevParse("refs/remotes/" + remote + "/" + branch)
	if err != nil {
		if merged || g.BranchExists(branch) {
			return nil
		}
		return fmt.Errorf("branch %s does not exist on %s", branch, remote)
	}
	if !g.BranchExists(branch) {
		_, err := g.Run("branch", "--track", branch, remote+"/"+branch)
		return err
	}
	local, err := g.RevParse(git.Ref(branch))
	if err != nil || local == remoteSHA {
		return err
	}
	if !g.IsAncestor(local, remoteSHA) {
		a.infof("Warning: local %s has diverged from %s; keeping the local branch", branch, remote)
		return nil
	}
	if g.Includes(branch, remoteSHA) {
		return nil // moved back from the remote tip on purpose
	}
	owner, err := g.WorktreeFor(branch)
	if err != nil {
		return err
	}
	if owner != "" {
		_, err = g.At(owner).Run("merge", "--ff-only", "--quiet", remoteSHA)
	} else {
		_, err = g.Run("update-ref", git.Ref(branch), remoteSHA, local)
	}
	return err
}

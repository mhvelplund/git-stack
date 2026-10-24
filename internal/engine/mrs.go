package engine

import (
	"errors"
	"fmt"
	"strings"

	"gitlab.com/malephex/git-stack/internal/exitcode"
	"gitlab.com/malephex/git-stack/internal/forge"
	"gitlab.com/malephex/git-stack/internal/git"
	"gitlab.com/malephex/git-stack/internal/stack"
)

// SyncMRs refreshes each unmerged layer's PR from the host and marks merged
// layers. It returns the PRs found, keyed by branch. Merged layers are not
// fetched again: merged is final on the host.
//
// An PR found by branch name alone (no PR recorded, or the recorded one was
// closed) is only taken if it is open, or if it merged exactly what the local
// branch holds: an old merged PR whose source branch merely had the same name
// must not make a new layer count as merged.
func SyncMRs(c *forge.Forge, g *git.Repo, s *stack.Stack) (map[string]*forge.CR, error) {
	found := map[string]*forge.CR{}
	var errs []error
	for _, b := range s.Branches {
		if b.Merged {
			continue
		}
		var mr *forge.CR
		var err error
		if b.CR != nil {
			mr, err = c.GetCR(b.CR.Number)
			// An PR of another branch: the Number was recorded in another project
			// (e.g. one reached through --remote).
			if err == nil && mr.Head != b.Name {
				mr = nil
			}
			// A closed PR may have been replaced by a newer one for the same branch.
			if err == nil && (mr == nil || mr.State == forge.StateClosed) {
				if newer, ferr := c.FindCRForBranch(b.Name); ferr == nil && newer != nil && belongs(g, b.Name, newer) {
					mr = newer
				}
			}
		} else {
			mr, err = c.FindCRForBranch(b.Name)
			if err == nil && mr != nil && !belongs(g, b.Name, mr) {
				mr = nil
			}
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if mr == nil {
			continue
		}
		found[b.Name] = mr
		b.CR = &stack.CR{Number: mr.Number, URL: mr.URL}
		if mr.Merged() {
			b.Merged = true
		}
	}
	return found, errors.Join(errs...)
}

// belongs reports whether mr, found by source branch name, is the PR of the
// local branch: an open PR is, a merged or closed one only if the local tip is
// contained in the commit it ended on.
func belongs(g *git.Repo, branch string, mr *forge.CR) bool {
	if mr.Open() {
		return true
	}
	return mr.HeadSHA != "" && g.IsAncestor(git.Ref(branch), mr.HeadSHA)
}

// Push pushes branches to remote in one `git push`. Each branch carries an
// explicit --force-with-lease on the remote-tracking value, and is only pushed
// if that value was integrated locally (git.Repo.Includes): a commit someone
// else pushed, such as an applied review suggestion, is refused rather than
// overwritten, even right after a fetch has refreshed the tracking refs.
func Push(g *git.Repo, remote string, branches []string) error {
	if len(branches) == 0 {
		return nil
	}
	args := []string{"push", "--quiet", "--porcelain"}
	var refspecs, foreign []string
	for _, b := range branches {
		lease := ""
		if sha, err := g.RevParse("refs/remotes/" + remote + "/" + b); err == nil {
			if !g.Includes(b, sha) {
				foreign = append(foreign, b)
				continue
			}
			lease = sha
		}
		args = append(args, "--force-with-lease="+git.Ref(b)+":"+lease)
		refspecs = append(refspecs, git.Ref(b)+":"+git.Ref(b))
	}
	if len(foreign) > 0 {
		return exitcode.New(exitcode.Generic, "nothing was pushed: %s on %s holds commits that your local branch never had; integrate them (e.g. `git pull --rebase`), then try again",
			strings.Join(foreign, ", "), remote)
	}
	args = append(append(args, remote), refspecs...)
	// RunEnv keeps stdout in the error: --porcelain reports rejections there.
	if _, err := g.RunEnv(nil, args...); err != nil {
		return fmt.Errorf("push to %s failed: %w", remote, err)
	}
	return nil
}

// StackInfo builds the description-block data for s from the layers that have PRs.
func StackInfo(s *stack.Stack) forge.StackInfo {
	info := forge.StackInfo{Trunk: s.Trunk}
	for _, b := range s.Branches {
		if b.CR != nil {
			info.Items = append(info.Items, forge.StackItem{Number: b.CR.Number, Branch: b.Name})
		}
	}
	return info
}

// Block returns c's block renderer; urls gives CR web URLs, for hosts
// without a short reference syntax.
func Block(c *forge.Forge, urls map[int]string) forge.Block {
	return c.Block(func(n int) string { return urls[n] })
}

// SetBody returns body with the stack block for CR self, checking the
// host's description size limit.
func SetBody(c *forge.Forge, blk forge.Block, info forge.StackInfo, body string, self int) (string, error) {
	desc := forge.SetBlock(body, blk.Render(info, self))
	if max := c.Info.MaxBody; max > 0 && len([]rune(desc)) > max {
		return "", exitcode.New(exitcode.ProviderFailure, "the description of %s would be %d characters with the stack block, over %s's limit of %d; shorten it", c.Ref(self), len([]rune(desc)), c.HostName(), max)
	}
	return desc, nil
}

// UpdateBlocks rewrites the stack block in every open CR of s that needs it,
// and syncs the host's native stack where there is one.
func UpdateBlocks(c *forge.Forge, s *stack.Stack, known map[string]*forge.CR) error {
	info := StackInfo(s)
	urls := map[int]string{}
	for _, b := range s.Branches {
		if b.CR != nil {
			urls[b.CR.Number] = b.CR.URL
		}
	}
	blk := Block(c, urls)
	var errs []error
	for _, b := range s.Branches {
		if b.CR == nil || b.Merged {
			continue // merged PRs are no longer open
		}
		mr := known[b.Name]
		if mr == nil || mr.Number != b.CR.Number {
			var err error
			if mr, err = c.GetCR(b.CR.Number); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if !mr.Open() || mr.Head != b.Name {
			continue // closed, or another branch's PR (see SyncMRs)
		}
		desc, err := SetBody(c, blk, info, mr.Body, mr.Number)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if desc == mr.Body {
			continue
		}
		updated, err := c.UpdateCR(mr.Number, forge.Update{Body: &desc})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		known[b.Name] = updated
	}
	if c.Info.NativeStacks {
		var open []int
		for _, b := range s.Branches {
			if b.CR != nil && !b.Merged {
				open = append(open, b.CR.Number)
			}
		}
		if err := c.SyncStack(open); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DefaultTitleBody derives an PR title and description from a layer's commits:
// a single commit gives its subject and body, otherwise the humanized branch name.
func DefaultTitleBody(g *git.Repo, parent, branch string) (string, string) {
	out, err := g.Run("log", "--reverse", "--format=%s%x00%b%x01", git.Ref(parent)+".."+git.Ref(branch))
	if err == nil {
		commits := strings.Split(strings.TrimSuffix(strings.TrimSpace(out), "\x01"), "\x01")
		if len(commits) == 1 && commits[0] != "" {
			subject, body, _ := strings.Cut(strings.TrimSpace(commits[0]), "\x00")
			return subject, strings.TrimSpace(body)
		}
	}
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' {
			return ' '
		}
		return r
	}, branch), ""
}

// MarkMergedByAncestry marks layers merged when there is no host to ask: a
// layer with commits of its own whose tip is contained in trunkSHA. Squash
// merges rewrite the commits and cannot be told apart from unmerged work.
// Layers are marked from the bottom up and the walk stops at the first one
// that is not merged. It returns the names of the layers it marked.
func MarkMergedByAncestry(g *git.Repo, s *stack.Stack, trunkSHA string) []string {
	var marked []string
	for _, b := range s.Active() {
		tip, err := g.RevParse(git.Ref(b.Name))
		if err != nil || b.Base == "" || tip == b.Base || !g.IsAncestor(tip, trunkSHA) {
			break
		}
		b.Merged = true
		marked = append(marked, b.Name)
	}
	return marked
}

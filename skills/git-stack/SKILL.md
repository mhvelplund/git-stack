---
name: git-stack
description: Use when working with stacked pull requests (or GitLab merge requests) via `git stack` (git-stack) — creating a stack of branches, restacking after changes, submitting PRs, syncing after merges, or merging a stack.
---

# git stack

`git stack` manages a stack of branches on top of a trunk (usually `main`). Each layer is a branch with any number of
commits and, on GitHub, GitLab or Bitbucket Cloud, one pull request (merge request) targeting the layer below it. On other hosts it
manages the branches with plain git and opens no PRs.

## Everyday flow

```sh
git stack init first-layer            # start a stack on the default branch
git commit -am "..."                  # commit as usual on the current layer
git stack add -Am "Next step"         # new layer on top, committing all changes to it
git stack submit --auto               # push and create/update one draft PR per layer
git stack sync                        # after trunk moves or a PR merges: restack, push, retarget
git stack merge --yes --squash        # merge bottom-up, restacking between merges
```

## Rules for agents

- Always pass `--auto` to `submit` and `--yes` to `merge`; the interactive editors need a human at a terminal.
- `modify` and `switch` are interactive only. Use `up`, `down`, `top`, `bottom`, `checkout <branch>` to move.
- `add` must run from the top layer. Check with `git stack view --json` (`current`, `branches[].isCurrent`).
- After editing a lower layer (`git commit --amend`, new commits), run `git stack rebase` to restack the layers above,
  then `git stack push` or `submit`.
- Exit code 3 from `rebase` or `modify` means the operation is paused on a conflict: resolve the files, `git add`
  them, then run `git stack rebase --continue` (or `modify --continue`), or `--abort` to restore every branch.
- Exit code 3 from `sync` or `merge` means nothing is paused: every branch was already restored. Run
  `git stack rebase` to reproduce the conflict and resolve it as above.
- `push`, `submit` and `sync` refuse to push a layer whose remote branch has commits the local branch never had (for
  example an applied review suggestion). Integrate them with `git pull --rebase` on that layer, then retry.
- Never edit the block between `git-stack:begin` and `git-stack:end` in PR descriptions; git-stack rewrites it.
- For a self-hosted GitHub or GitLab that is not detected, pass `--provider github` or `--provider gitlab`, or set
  `git config git-stack.provider <name>` once. Exit code 9 means the command needs a provider and none applies.
- Use `git stack -h` or `git stack help <command>`, not `git stack --help` (git turns that into a man page lookup).

## Exit codes

0 success · 1 error · 2 not in a stack · 3 rebase conflict · 4 provider (host) failure · 5 invalid arguments ·
6 ambiguous branch · 7 operation already in progress · 8 stack locked by another process · 9 not supported by the
provider.

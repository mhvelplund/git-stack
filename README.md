# git stack

<img src=".img/slop-small.jpg" alt="For robots, by robots. Vibed without love or skill." height="24" />

Stacked pull requests for any Git host, where each layer of the stack is a branch that can hold **any number of
commits**.

`git stack` is a Git subcommand: put the `git-stack` binary on your `PATH` and Git runs it as `git stack`. Everything
that touches branches (creating layers, navigating, cascading rebases, pushing, restructuring) is plain `git`, so it
works the same against any Git server. Pull requests are handled by **provider plugins**. git-stack picks the plugin
from the remote URL. GitHub, GitLab, Bitbucket Cloud and Azure DevOps are supported. When no provider
applies, git-stack still manages the branches and simply opens no pull requests.

`git stack` is a **derivative work of [gh-stack](https://github.com/github/gh-stack)** by GitHub, Inc., released under
the MIT License. It reproduces the command-line interface of **gh-stack v0.2.1** and contains code from it. It is not
affiliated with or endorsed by GitHub, GitLab or any other Git host.

## Status

Work in progress. Available today:

| Command                                        | Description                                                                                                                                                                                         |
|------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `init [branches...]`                           | Start a stack (`-b/--base` sets the trunk)                                                                                                                                                          |
| `add [branch]`                                 | Add a layer on top (`-m`, `-A`, `-u`)                                                                                                                                                               |
| `view`                                         | Show the stack (`--short`, `--json`)                                                                                                                                                                |
| `up [n]`, `down [n]`, `top`, `bottom`, `trunk` | Navigate the stack (`--print-path`)                                                                                                                                                                 |
| `push`                                         | Push all unmerged branches with `--force-with-lease` (`--remote`)                                                                                                                                   |
| `submit`                                       | Push and create/update one PR per branch, each targeting the branch below; in a terminal, edit titles, descriptions and draft state in a full-screen editor first (`--auto`, `--open`, `--remote`) |
| `sync`                                         | Fetch, reconcile with the stack on the host, rebase if the trunk moved, push, retarget PRs (`--prune`, `--remote`)                                                                                 |
| `merge [<pr>]`                                 | Merge PRs bottom-up to the chosen one, restacking and retargeting between merges (`--squash`, `--merge`, `--rebase`, `--merge-method`, `--yes`, `--timeout`)                                        |
| `switch`                                       | Pick a branch of the current stack in a full-screen picker (`--print-path`)                                                                                                                         |
| `checkout [<pr> \| <url> \| <branch>]`         | Check out a stack, setting it up from the host if needed; without arguments, pick from local and remote stacks (`--print-path`)                                                                     |
| `modify`                                       | Restructure the stack in a full-screen editor: drop, fold, insert, reorder, rename, undo (`--continue`, `--abort`)                                                                                  |
| `link <branch-or-pr>...`                       | Create or extend a stack on the host without local tracking (`--base`, `--open`, `--remote`)                                                                                                        |
| `unstack [<pr>]`                               | Remove a stack locally and on the host (`--local`; alias `delete`)                                                                                                                                  |
| `feedback [title]`                             | Open an issue for git-stack in the browser                                                                                                                                                          |
| `rebase [branch]`                              | Cascade-rebase the stack onto the trunk (`--downstack`, `--upstack`, `--no-trunk`, `--continue`, `--abort`, `--remote`, `--preserve-dates`)                                                         |

This covers every command of gh-stack v0.2.1. Every command takes `--provider` (see [Providers](#providers)).

## Requirements

- Git 2.36 or later, on Windows, Linux or macOS
- For GitHub: [`gh`](https://cli.github.com), logged in with `gh auth login`
- For GitLab: [`glab`](https://gitlab.com/gitlab-org/cli), logged in with `glab auth login`
- For Bitbucket Cloud: credentials in `BITBUCKET_TOKEN` (a workspace, project or repository access token), or in
  `BITBUCKET_EMAIL` and `BITBUCKET_API_TOKEN` (an Atlassian API token with Bitbucket scopes), or a `bitbucket.org`
  credential stored in git's credential helper
- For Azure DevOps: the [Azure CLI](https://learn.microsoft.com/cli/azure/) (`az`), logged in with `az login` or
  `az devops login`; its `azure-devops` extension is installed on first use. Retargeting PRs and rebase merges also
  need `AZURE_DEVOPS_EXT_PAT` unless the organization accepts the `az login` identity (see [Providers](#providers))

## Installation

git-stack is five binaries: `git-stack` itself and one plugin per provider, `git-stack-github`, `git-stack-gitlab`,
`git-stack-bitbucket` and `git-stack-ado`. Install the providers you need next to `git-stack` or anywhere on your `PATH`.

1. Install the binaries (requires Go, see [Development](#development)):

   ```sh
   go install github.com/mhvelplund/git-stack/cmd/git-stack@latest
   go install github.com/mhvelplund/git-stack/cmd/git-stack-github@latest
   go install github.com/mhvelplund/git-stack/cmd/git-stack-gitlab@latest
   go install github.com/mhvelplund/git-stack/cmd/git-stack-bitbucket@latest
   go install github.com/mhvelplund/git-stack/cmd/git-stack-ado@latest
   ```

   Make sure `$(go env GOPATH)/bin` is on your `PATH`.

2. Check that Git finds it:

   ```sh
   git stack -h
   ```

   Use `git stack -h` or `git stack help <command>` for help. `git stack --help` does not work, because Git turns
   `--help` into a man page lookup for `git-stack`.

3. Optionally, add a shorter alias:

   ```sh
   git config --global alias.st stack   # git st view, git st up, ...
   ```

## Usage

```sh
git stack init auth-model          # start a stack on the default branch
git commit -am "Add user model"    # commit as much as you like on each layer
git stack add -Am "Add login API"  # new layer named MM-DD-add_login_api, with a commit
git stack view                     # show the stack
git stack down                     # move toward the trunk
git commit --amend                 # rewrite a lower layer...
git stack rebase                   # ...and restack everything above it
git stack submit                   # push and open one PR per layer
git stack sync --prune             # after the bottom PR merges: restack, retarget, clean up
```

`git stack submit --auto` creates draft PRs titled from the commits. Without `--auto`, a full-screen editor lets you
pick which branches get PRs and edit each title, description and draft state. Every PR gets a block at the end of its
description that lists the whole stack, so reviewers can move between layers. git-stack keeps that block up to date;
text outside it is never touched.

If a rebase hits a conflict, resolve it, `git add` the files and run `git stack rebase --continue`. To undo the whole
operation, run `git stack rebase --abort`.

Stack metadata lives in `.git/git-stack` (shared by all worktrees) and is never committed.

## Providers

| Provider    | Detected for                                                      | Uses                    | Stack on the host                                       |
|-------------|-------------------------------------------------------------------|-------------------------|---------------------------------------------------------|
| `github`    | `github.com`, `github.*` hosts, hosts `gh` knows                  | `gh`'s login and config | GitHub's native stacked PRs, plus the description block |
| `gitlab`    | `gitlab.com`, `gitlab.*` hosts, hosts `glab` knows                | `glab`                  | The description block                                   |
| `bitbucket` | `bitbucket.org` (Bitbucket Cloud only)                            | the REST API, see below | The description block                                   |
| `ado`       | `dev.azure.com`, `*.visualstudio.com`, any `.../_git/<repo>` path | `az`, see below         | The description block                                   |
| `none`      | anything else, e.g. a local path or unknown server                | nothing                 | none: branches only, no PRs                             |

The provider is chosen in this order:

1. the `--provider <name>` flag;
2. `git config git-stack.provider <name>`, set once per repository (or globally);
3. the remote's host: `github.com`, `gitlab.com`, `bitbucket.org`, `dev.azure.com` and `*.visualstudio.com`, and
   self-hosted servers whose name starts with `github.` or `gitlab.`;
4. any installed plugin that recognises the host (`git-stack-github` and `git-stack-gitlab` claim hosts that `gh` or
   `glab` is logged in to, and `git-stack-ado` claims Azure DevOps Server remotes, whose paths contain `/_git/`); the
   answer is remembered for a day in `.git/git-stack-hosts.json`;
5. `none`.

For a self-hosted GitHub Enterprise Server or GitLab that is not detected, run once in the repository:

```sh
git config git-stack.provider gitlab
```

With provider `none`, `submit` only pushes, `sync` recognises a layer as merged when its commits are on the trunk
(merge commits and fast-forwards; squash merges cannot be detected), and `merge`, `link` and checking out by PR
number fail with exit code 9.

On GitHub, git-stack also keeps GitHub's native stack in sync. If stacked PRs are not enabled for the repository (or
on older GitHub Enterprise Server versions), it falls back to the description block alone.

Bitbucket has no CLI like `gh` or `glab` that runs on every platform, so its provider calls the Bitbucket Cloud API
itself. It takes the first credential it finds: `BITBUCKET_TOKEN` (sent as a Bearer token), `BITBUCKET_EMAIL` with
`BITBUCKET_API_TOKEN`, or the `bitbucket.org` credential from git's credential helpers (never prompting). PRs are
linked by URL, and the stack block is hidden as a Markdown link reference. Bitbucket Cloud has no auto-merge, so
`merge` merges each PR at once and fails if checks or approvals are missing. Bitbucket Data Center is not supported.

On Azure DevOps, git-stack works through `az` and its `azure-devops` extension, with `az`'s login. `az` cannot change
a PR's target branch or complete it with a rebase, so for those two operations the provider calls the REST API
directly. It authenticates with `AZURE_DEVOPS_EXT_PAT` (a personal access token with Code read and write scope) if
set, and otherwise with an access token for the `az login` identity, which organizations backed by a Microsoft
account may refuse. Every stack of two or more PRs needs retargeting when its bottom PR merges, so set the variable
if `sync` or `merge` report an authentication error. When policies (reviewers, builds) block completion, `merge`
sets the PR to auto-complete and waits. PRs are referenced as `!12`, and descriptions are limited to 4000 characters.

### Writing a provider

A provider is an executable named `git-stack-<name>` (`git-stack-<name>.exe` on Windows). git-stack runs it once per
operation as `git-stack-<name> <operation>`. The request is a JSON object on stdin and the answer is a JSON object on
stdout. [`pkg/protocol`](pkg/protocol) defines the operations, the request and response types, and a `Serve` helper.
A Go provider implements `protocol.Provider` and calls `protocol.Serve`; see
[`cmd/git-stack-gitlab`](cmd/git-stack-gitlab/main.go) for a complete example. Its `describe` answer sets the host's
terms (`PR` or `MR`, `#12` or `!12`), how the stack block is hidden in descriptions, and the description size limit.
Providers in other languages only need to speak the same JSON.

### Differences from gh-stack

- The stack is recorded in a managed block at the end of each PR description on every host, and additionally in
  GitHub's native stack on GitHub.
- Renaming a layer in `modify` cannot rename its PR's head branch, so the next `submit` opens a new PR; close the old
  one.
- `push`, `submit` and `sync` refuse to overwrite commits that someone else pushed to a layer (for example an applied
  review suggestion) instead of force-pushing over them.
- `merge <pr>` for a stack you have not checked out needs the stack block in the PR description; plain PRs are not
  guessed into a stack.
- `merge` merges PRs one at a time, bottom up. It stops at the first failure, leaving earlier PRs merged.
- Stack blocks written by glab-stax are recognised and replaced by git-stack's own, so `checkout` and `sync` pick up
  stacks published with it.

### Environment variables

| Variable                       | Effect                                                            |
|--------------------------------|-------------------------------------------------------------------|
| `GIT_STACK_NO_UPDATE_NOTIFIER` | Any non-empty value disables the daily check for new releases.    |
| `GIT_STACK_HYPERLINKS`         | `1` or `0` forces clickable PR links in `view` on or off.         |
| `GIT_PAGER`, `PAGER`           | Pager for `view` (default `less -R`).                             |
| `VISUAL`, `EDITOR`             | Editor for PR descriptions in the `submit` editor (`^e`).         |
| `GH_TOKEN`, `GH_ENTERPRISE_TOKEN` | Read by the GitHub provider, as by `gh`.                       |
| `BITBUCKET_TOKEN`, `BITBUCKET_EMAIL`, `BITBUCKET_API_TOKEN` | Credentials of the Bitbucket provider (see [Providers](#providers)). |
| `AZURE_DEVOPS_EXT_PAT` | Personal access token for the Azure DevOps provider, as for `az devops` (see [Providers](#providers)). |

### Agent skill

[`skills/git-stack/SKILL.md`](skills/git-stack/SKILL.md) teaches coding agents how to drive `git stack`
non-interactively.

### Exit codes

| Code | Meaning                                   |
|------|-------------------------------------------|
| 0    | Success                                   |
| 1    | Generic error                             |
| 2    | Not in a stack / stack not found          |
| 3    | Rebase conflict                           |
| 4    | Provider (Git host) failure               |
| 5    | Invalid arguments or flags                |
| 6    | Disambiguation required                   |
| 7    | Operation already in progress             |
| 8    | Stack locked by another process           |
| 9    | Not supported by the provider (e.g. none) |

## Development

All development tools (Go, golangci-lint, gh, glab) are managed with [mise](https://mise.jdx.dev) and pinned in
`mise.toml`.

```sh
mise install       # install the toolchain
mise run build     # build git-stack and the plugins into bin/
mise run test      # run the tests
mise run lint      # go vet + golangci-lint
mise run check     # all of the above
mise run dist      # cross-compile for Windows, Linux and macOS into dist/
```

To try a build, put `bin/` first on your `PATH` and run `git stack` in any repository.

## License

MIT. See [LICENSE](LICENSE). Contains work derived from gh-stack, Copyright GitHub, Inc.

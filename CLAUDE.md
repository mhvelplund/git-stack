# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`git-stack` is a Go Git subcommand (`git stack <cmd>`) for stacked PRs, a derivative of gh-stack v0.2.1 (CLI
reproduced; the core was originally ported from glab-stax). Branch operations are plain git; PR operations go
through external provider plugins (`git-stack-github`, `git-stack-gitlab`, `git-stack-bitbucket`,
`git-stack-ado`). Must work on Windows, Linux and macOS. User-facing docs: README.md. Agent usage of the CLI:
`skills/git-stack/SKILL.md`.

## Commands

All tools (Go 1.26, golangci-lint v2, pre-commit) are pinned in `mise.toml`.

```sh
mise install        # toolchain; also runs `pre-commit install`
mise run build      # go build -ldflags=<version> -o bin/ ./cmd/...
mise run test       # go test ./...
mise run lint       # go vet ./... && golangci-lint run ./...
mise run check      # build + lint + test (what CI runs)
mise run dist       # cross-compile into dist/ via scripts/dist
```

- Single test: `go test -run TestName ./internal/cli/`.
- `internal/cli` tests are slow (they spawn many git processes); prefer running the affected package or `-run`
  slices.
- Formatting: gofmt + goimports via `golangci-lint fmt`. `.gitattributes` forces LF.
- To try a build manually, put `bin/` first on PATH and run `git stack` in any repo.

## Committing

- Use [Conventional Commits](https://www.conventionalcommits.org) messages, e.g. `fix(bitbucket): ...`,
  `feat: ...`.
- When pre-commit hooks fail, fix the reported problems. Never bypass the hooks (`--no-verify`, `SKIP=...`,
  uninstalling them) unless the user approves it.

## Architecture

- `cmd/git-stack` – core binary. `cmd/git-stack-<provider>` – plugins, each just `protocol.Serve(...)`.
- `pkg/protocol` – the PUBLIC plugin contract: `Provider` interface, request/response types, op names,
  `Serve`/`Dispatch`. Each op runs as `git-stack-<name> <op>` with JSON on stdin/stdout. Errors carry codes
  (`unsupported`, `not_found`, `auth`, `failure`) that `internal/exitcode` maps to exit codes 9 / 4. Changes here
  affect third-party plugins.
- `internal/forge` – core side of the plugin boundary. `Forge` wraps a `protocol.Provider`; a `Transport` is either
  `ExecTransport` (spawns the plugin binary) or `LocalTransport` (in-process, used by tests). Also: provider
  resolution (`detect.go`: `--provider` → `git config git-stack.provider` → host table → plugin `detect`, cached 24h
  in `.git/git-stack-hosts.json` → `none`), remote URL parsing, and the stack block. Plugins are looked up next to
  the git-stack executable first, then on PATH.
- The **stack block** (the list of the stack appended to each PR description) is owned by the core
  (`internal/forge/block.go`), not by providers. Styles: `html-comment` and `link-ref` (Bitbucket). It also reads
  legacy glab-stax blocks. Text outside the block is never touched.
- `describe` returns host terms (`PR`/`MR`, `#%d`/`!%d`), block style, `max_body` and `native_stacks`. Runtime
  messages must use `a.ref(n)`, `a.cr()`, `a.long()`, `a.host()` (`internal/cli/forge.go`) rather than hard-coding
  "PR"/"#"; static help text stays neutral ("PR", "the host").
- Provider `none`: `submit` only pushes; `sync` detects merges by ancestry (`engine.MarkMergedByAncestry`);
  `merge`, `link` and by-number lookups fail with exit code 9.
- `internal/provider/{github,gitlab,bitbucket,ado}` – implementations. GitHub's `github.go`/`merge_async.go` are
  copied verbatim from gh-stack (`provider.go` is the adapter; it also syncs GitHub's native Stacks API, falling back to
  block-only on 404). GitLab wraps `glab`; `glabtest/` is a fake GitLab API. Bitbucket Cloud calls the REST API
  directly with `net/http`. Azure DevOps uses `az` (`devops invoke` for reads, `repos pr create/update` for writes,
  text passed as `@file`); `az devops invoke` cannot write PRs and `az repos` cannot retarget or rebase-merge, so
  those two go to REST (`ado/rest.go`).
- `internal/cli` – cobra commands; `App` (`app.go`) holds repo/store/forge, and `mutate` does locked metadata edits.
- `internal/engine` – rebase, modify and sync-reconcile logic.
- `internal/stack` – metadata in `.git/git-stack` (shared across worktrees, never committed), with file locking
  (`lock_{unix,windows}.go`), atomic writes (`atomic_*.go`), and journals for paused `rebase`/`modify` operations
  (`--continue`/`--abort`).
- `internal/tui` – Bubble Tea full-screen editors and pickers (`submit`, `modify`, `switch`, `checkout`).
- Internal names are host-neutral: a PR/MR is a `CR` (`stack.CR`, `Branch.CR`, `forge.CR` = `protocol.CR`).

## Testing

- Packages that create repos must call `testutil.Main(m)` from `TestMain`; it isolates git from the user's config.
  `testutil.NewRepo` makes a clone with a bare local `origin`.
- CLI tests use the `harness` in `internal/cli/helpers_test.go`: the real GitLab provider in-process over
  `LocalTransport` and the `glabtest` fake. `harness.resolve = true` uses real resolution, which yields provider
  `none` for the local origin (see `none_test.go`). `harness.keys` scripts TUI input.
- `internal/forge` `TestExecPlugin` builds the real `git-stack-gitlab` binary and talks to it over stdio.
- Bitbucket tests use an `httptest` fake; ADO tests fake `az` plus an `httptest` TLS server for the REST calls.

## Gotchas

- `jsonEscape` in `block.go` deliberately builds `\uXXXX` at runtime instead of writing the literal; keep it that
  way, and check backslash escapes survive after editing files.
- Windows paths like `C:\path` can parse as scp-style remotes; `ParseRemoteURL` requires the colon index > 1.
- Tests that change PATH (e.g. to hide plugins) affect the whole process: don't run them in parallel, and scope the
  change to a subtest.
- `git stack --help` doesn't work (Git turns it into a man page lookup); use `git stack -h`.
- The `glab-stax` entry in `mise.toml` is a Windows-only asset kept on purpose; CI therefore installs only `go` and
  `golangci-lint`.

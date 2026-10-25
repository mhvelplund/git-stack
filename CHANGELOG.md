## v1.0.2 (2026-10-25)

### Fix

- **engine**: push over a remote branch the host restacked itself

## v1.0.1 (2026-10-25)

### Fix

- **github**: merge PRs in a native stack through the async endpoint

## v1.0.0 (2026-10-24)

### Feat

- **ado**: add Azure DevOps provider plugin (git-stack-ado)

### Fix

- **ado**: require the azure-devops extension instead of installing it

## v0.2.0 (2026-10-24)

### Fix

- **bitbucket**: report a merge task that does not succeed
- **bitbucket**: follow the Location header when a merge returns 202
- **bitbucket**: find merged pull requests for a branch
- **bitbucket**: send x-token-auth git credentials as Bearer
- **forge**: name Bitbucket properly in user-facing messages
- **bitbucket**: stop waiting on a hung git credential helper

### Perf

- **bitbucket**: match branch restriction globs without regexps

## v0.1.0 (2026-10-24)

# gt

A small Go CLI for bare-repo + worktree git workflows. Clone, manage worktrees, prune orphans, wire up direnv per-repo.

> v0.1.0 is in progress. Install via GitHub Releases once the first release is published.

## Install

```sh
go install github.com/pedromvgomes/gt/cmd/gt@latest
```

The release installer will be added before `v0.1.0`:

```sh
curl -fsSL https://raw.githubusercontent.com/pedromvgomes/gt/main/install.sh | sh
```

## Quick start

```sh
gt clone git@github.com:pedromvgomes/foo.git
cd foo
```

Worktree management, scratch worktrees, direnv authentication, and the release installer are being added in follow-up PRs.

## Commands

### `gt clone <url> [folder]`

Clone a repository into a bare repo plus worktree layout:

- bare repository at `<folder>/.bare`
- `.git` pointer at `<folder>/.git`
- default branch worktree at `<folder>/<default-branch>/`
- worktree type directories from config (`feature`, `fix`, `chore` by default)

## Configuration

`gt` bootstraps YAML config on first command run at `$XDG_CONFIG_HOME/gt/config.yaml`, or `~/.config/gt/config.yaml` when `XDG_CONFIG_HOME` is unset.

```yaml
worktree_types:
  - feature
  - fix
  - chore

ssh:
  host_aliases: {}
```

Per-repo overrides live at `<gt-managed-root>/.gt.yaml` and override global config per key.

## Development

```sh
go test ./tests/... -coverpkg=./internal/... -coverprofile=coverage.out
go vet ./...
golangci-lint run
```

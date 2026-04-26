# gt

A small Go CLI for bare-repo + worktree git workflows. Clone, manage worktrees, prune orphans, wire up direnv per-repo.

> v0.1.0 is in progress. Release archives are published from tags via GoReleaser.

## Install

Primary installer:

```sh
curl -fsSL https://raw.githubusercontent.com/pedromvgomes/gt/main/install.sh | sh
```

Fallback with Go:

```sh
go install github.com/pedromvgomes/gt/cmd/gt@latest
```

If macOS blocks the downloaded binary on first run, remove the quarantine attribute:

```sh
xattr -d com.apple.quarantine "$(which gt)"
```

## Quick start

```sh
gt clone git@github.com:pedromvgomes/foo.git
cd foo
gt wt add feature/new-thing
cd feature/new-thing
# ... do work ...
gt wt rm new-thing --branch
```

`gt clone https://...` prompts to use SSH by default, then runs the direnv authentication setup unless `--no-setup-auth` is passed.

## Commands

### `gt clone <url> [folder]`

Clone a repository into a bare repo plus worktree layout:

- bare repository at `<folder>/.bare`
- `.git` pointer at `<folder>/.git`
- default branch worktree at `<folder>/<default-branch>/`
- worktree type directories from config (`feature`, `fix`, `chore` by default)

Flags:

- `--ssh` converts HTTPS URLs to SSH without prompting.
- `--no-ssh` keeps HTTPS URLs without prompting.
- `--no-setup-auth` skips post-clone direnv setup.
- `--user <name>` chooses the GitHub user for post-clone direnv setup.

### `gt wt add <type/name> [--from <branch>]`

Create a typed worktree and branch, for example `gt wt add feature/new-thing --from main`.

### `gt wt rm [--branch] <name>`

Remove a worktree by its short name. With `--branch`/`-b`, also delete the matching local branch.

### `gt wt list`

List typed worktrees and the scratch worktree, including the latest commit summary for each.

### `gt wt nuke [--branches]`

Remove all typed worktrees. With `--branches`/`-b`, force-delete the corresponding local branches.

### `gt wt prune-branches [--dry-run]`

Delete local branches that do not have an active worktree, while preserving `main`, `master`, the default branch, and `scratch`.

### `gt scratch [--reset|--delete]`

Create or manage the top-level scratch worktree. `gt scratch` creates it when missing or prints its current commit when it already exists, `--reset` checks it out from a new source branch, and `--delete` removes it plus the local `scratch` branch.

### `gt set-auth [--user <name>]`

Write an idempotent `.envrc` at the gt-managed root that exports `GH_TOKEN` from `gh auth token --user <name>`, runs `direnv allow`, and prints shell-hook instructions if direnv is not active in new shells.

## Configuration

`gt` bootstraps YAML config on first command run at `$XDG_CONFIG_HOME/gt/config.yaml`, or `~/.config/gt/config.yaml` when `XDG_CONFIG_HOME` is unset.

```yaml
worktree_types:
  - feature
  - fix
  - chore

ssh:
  host_aliases:
    github.com: github-personal
```

Per-repo overrides live at `<gt-managed-root>/.gt.yaml` and override global config per key.

## Shell completions

The installer prompts to install completions for zsh, bash, or fish when run interactively. You can regenerate them manually:

```sh
gt completion zsh > ~/.zsh/completions/_gt
gt completion bash > ~/.local/share/bash-completion/completions/gt
gt completion fish > ~/.config/fish/completions/gt.fish
```

## Development

```sh
go test ./tests/... -coverpkg=./internal/... -coverprofile=coverage.out
go vet ./...
golangci-lint run
```

Releases are cut by pushing a semver tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The release workflow runs GoReleaser and publishes four `tar.gz` archives plus `checksums.txt`.

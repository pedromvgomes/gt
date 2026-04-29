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
- `--setup <a,b,c>` runs only the named setup templates (overrides match-based selection).
- `--no-setup` skips post-clone setup templates entirely.
- `--yes` skips the setup confirmation prompt.
- `--show-setup` prints full template bodies before prompting.
- `--dry-run-setup` prints the setup plan without executing it.

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

### `gt setup [--setup a,b,c] [--no-setup] [--yes] [--from <name>] [--show] [--dry-run]`

Run the configured setup templates against the current repository. Works inside any git repository (gt-managed or plain). Reads the `origin` remote URL and matches it against each template's `match` patterns; every matching template runs in config-file order. With `--setup`, runs exactly those templates in the given order. `--from <name>` resumes a previously-failed run from that template. `--show` jumps straight to the detailed plan; `--dry-run` prints the plan without running anything.

### `gt config <path|show|validate|edit>`

- `gt config path` — print the absolute path of the global config file.
- `gt config show` — print the current contents.
- `gt config validate` — parse and validate, surfacing any errors.
- `gt config edit` — open the config in `$VISUAL`/`$EDITOR`. On save, the file is parsed and validated; if invalid, you are prompted to re-open the editor and the original file is left untouched until a valid edit is saved.

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

setup:
  templates:
    - name: agentic-toolkit
      match:
        - "github.com:pedromvgomes/*"
        - "github.com/pedromvgomes/*"
      run: |
        git clone git@github.com:pedromvgomes/agentic-toolkit.git "${GT_WORKDIR}/.agentic"
        ln -sfn "${GT_WORKDIR}/.agentic/CLAUDE.md" "${GT_WORKDIR}/CLAUDE.md"
    - name: golang-extras
      match: ["*"]
      script: ${HOME}/.config/gt/setup-scripts/golang-extras.sh
```

Per-repo overrides live at `<gt-managed-root>/.gt.yaml` and override global config per key. Per-repo configs **may not** declare setup templates — only the global config can. This keeps the trust model simple: setup commands live where you put them.

### Setup templates

Templates run after `gt clone` and on demand via `gt setup`. They are simple shell scripts; gt does not introspect them.

- Each template needs `name` and exactly one of `run` (inline shell) or `script` (path to an executable file). `${VAR}` references in `script` paths are expanded.
- `match` is a list of glob patterns checked as substrings against the clone URL. `*` matches any sequence; `?` matches a single character. `match: ["*"]` runs the template for every repo. An empty/missing `match` makes the template only runnable via `--setup <name>`.
- Templates execute in the order they appear in the file. Every template whose `match` matches the URL runs.
- Available env vars (also substituted into `script:` paths):
  - `GT_ROOT` — gt-managed root, or git repo root when not gt-managed.
  - `GT_WORKDIR` — `${GT_ROOT}/${GT_DEFAULT_BRANCH}` in bare layout, `${GT_ROOT}` in plain.
  - `GT_LAYOUT` — `bare` or `plain`.
  - `GT_DEFAULT_BRANCH`, `GT_REPO_OWNER`, `GT_REPO_NAME`, `GT_REPO_URL`.
- Before running, gt prints the list of templates that will execute and prompts `[Y/n/d]` (`d` shows the full bodies). The prompt is skipped when `--yes` is passed or stdin is not a TTY.
- On the first non-zero exit gt stops and tells you to resume with `gt setup --from <template>`.

### Security model

Templates live in your global config and run as you with your environment. Treat editing this file the same way you treat editing your shell profile. Per-repo configs cannot inject templates, and `gt setup` will not silently run anything you have not added to your config.

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

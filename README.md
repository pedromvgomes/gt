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

### `gt wt rm [--branch] [--force] <name>`

Remove a worktree by its short name. With `--branch`/`-b`, also delete the matching local branch.

Before removing, gt checks whether the worktree has uncommitted changes (any `git status --porcelain` output — staged, unstaged, or untracked):

- A **clean** worktree is removed without prompting.
- A **dirty** worktree prompts for confirmation in an interactive terminal (`Force delete and lose them? [y/N]`); answering no aborts with a non-zero exit and leaves the worktree untouched.
- In a non-interactive session (no TTY) a dirty worktree aborts with an error instead of prompting — pass `--force`/`-f` to remove it anyway.

`--force`/`-f` skips the dirty check entirely and removes without prompting (use it in scripts and CI). A confirmed or forced removal also covers `--branch` deletion of an unmerged branch, so you are asked at most once.

### `gt wt list`

List typed worktrees and the scratch worktree, including the latest commit summary for each.

### `gt wt nuke [--branches] [--force]`

Remove all typed worktrees. Worktrees with uncommitted changes are skipped with a warning unless `--force`/`-f` is given, which removes them and discards those changes. With `--branches`/`-b`, force-delete the corresponding local branches.

### `gt wt prune-branches [--dry-run]`

Delete local branches that do not have an active worktree, while preserving `main`, `master`, the default branch, and `scratch`.

### `gt scratch [--reset|--delete]`

Create or manage the top-level scratch worktree. `gt scratch` creates it when missing or prints its current commit when it already exists, `--reset` checks it out from a new source branch, and `--delete` removes it plus the local `scratch` branch.

### `gt set-auth [--user <name>]`

Write an idempotent `.envrc` at the gt-managed root that exports `GH_TOKEN` from `gh auth token --user <name>`, runs `direnv allow`, and prints shell-hook instructions if direnv is not active in new shells.

### `gt setup [--setup a,b,c] [--no-setup] [--yes] [--from <name>] [--show] [--dry-run]`

Run the configured setup templates against the current repository. Works inside any git repository (gt-managed or plain). Reads the `origin` remote URL and matches it against each template's `match` patterns; every matching template runs in config-file order. With `--setup`, runs exactly those templates in the given order. `--from <name>` resumes a previously-failed run from that template. `--show` jumps straight to the detailed plan; `--dry-run` prints the plan without running anything.

### `gt repo <init|check|sync|settings|fleet>`

Keep repositories structurally consistent: Dependabot config, PR gating, conventional commits, and Dependabot auto-merge, all declared in one committed `.gt-repo.yaml`.

- `gt repo init` — detect Dependabot ecosystems from the files on disk and seed `checks.required` from the check names your existing `pull_request` workflows already produce. Writes `.gt-repo.yaml` for you to review. `--dry-run` prints without writing.
- `gt repo check` — render the spec, diff it against the working tree, and lint the workflow triggers the gate depends on. Non-zero exit on any problem, so it works as a PR check. `--json` for machine-readable output.
- `gt repo sync` — write the files that have drifted. `--dry-run`, `--yes`, and `--skip-workflows`. A repository with no `.gt-repo.yaml` is not governed; sync says so and exits 0, which makes it safe to run from a post-clone setup template.
- `gt repo settings diff|apply` — branch protection, merge methods, and the single required status check, through your existing `gh` credentials.
- `gt repo fleet merge-pending --owner <name>` — list (or `--merge`) the Dependabot PRs the in-repo auto-merge cannot touch.

See [Repository governance](#repository-governance) for the full model.

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

Per-repo overrides live at `<gt-managed-root>/.gt.yaml` and override global config per key. A per-repo config **may** declare its own `setup.templates`; they are merged on top of the global templates by name — a per-repo template that reuses a global template's `name` replaces it in place, and new names are appended after the global ones. This file lives at your gt-managed root (not committed inside the repo), so its templates are as trusted as the ones in your global config.

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

Templates in your global config and in a per-repo `<gt-managed-root>/.gt.yaml` run as you with your environment. Both files live on your machine where you put them, so treat editing them the same way you treat editing your shell profile. Templates a repository ships in its own committed `.gt.yaml` are different: they are untrusted, gated behind an explicit confirmation, and never auto-run without a TTY unless you pass `--yes`. `gt setup` will not silently run anything you have not added to a config you control.

## Repository governance

Keeping many repositories structurally consistent is the problem `gt repo` solves. A GitHub template repo cannot: templates copy once and never propagate, so a change to shared policy means editing every repository by hand.

A repository opts in by committing a **`.gt-repo.yaml`**. That one file drives three different propagation mechanisms:

| Layer | How it reaches the repo |
|---|---|
| Gate logic (aggregation, conventional commits, drift check) | A thin caller pinned to gt's moving major tag — logic changes need **no file change at all** |
| Files that must exist in-repo (`dependabot.yml`, the callers, CODEOWNERS) | `gt repo sync`, locally or from the weekly in-repo job |
| GitHub API state (branch protection, merge methods) | `gt repo settings apply`, using your `gh` credentials |

```yaml
# .gt-repo.yaml
dependabot:
  - ecosystem: cargo
    directory: /source/daemon
  - ecosystem: npm
    directory: /source
    note: |
      Workspace ROOT: a single yarn.lock covers every member. A per-member
      entry leaves the lockfile stale and fails `yarn install --immutable`.

checks:
  timeout_minutes: 30
  required: ["All checks passed"]   # gt's gate waits on these
  optional: ["E2E Tests"]           # allowed to be absent

conventional_commits:
  enabled: true
  scope: pr_title # pr_title | commits | both

dependabot_auto_merge:
  enabled: true
  max_bump: minor
```

Shared policy — Dependabot cooldown days, commit-message prefixes, the weekly sync schedule — deliberately lives in gt's templates rather than this file, so changing it everywhere is one gt release.

### One required status check, forever

Branch protection requires exactly one check: **`PR / Gate`**. gt generates it, and it validates everything listed in `checks`. Never list it in `checks` — it is the aggregator, and `gt repo check` rejects a spec that does.

Reusable workflows report as `<caller job> / <called job>`, which is where the name comes from. A plain job reports under its own name alone, so a job named `All checks passed` is the check `All checks passed`, with no workflow prefix.

Because the name never changes, branch protection is configured once. Rename a CI job and you update `.gt-repo.yaml`, not the protection rule.

### Why a check that never appears is the dangerous case

The gate polls the PR's checks. `success`, `skipped` and `neutral` pass; `failure`, `cancelled` and `timed_out` fail. Skipped passing is deliberate — it avoids the trap where a legitimately skipped job never reports and blocks the PR forever.

But a check that *never appears* is indistinguishable at runtime from one that has not started yet. So `gt repo check` catches it statically instead, failing when:

- a name in `checks.required` is one no workflow can produce (a typo, or a renamed job);
- the workflow producing it carries a top-level `paths:` / `paths-ignore:` filter, so it may not run at all;
- the name still contains a `${{ }}` expression, which a matrix job expands before reporting.

The fix for a filtered workflow is to trigger it unconditionally and gate its jobs with job-level `if:`, so every job still reports a conclusion. Use `checks.optional` for checks that may genuinely be absent.

### What CI cannot do, and why

GitHub blocks `GITHUB_TOKEN` from creating or updating anything under `.github/workflows/`, so a compromised action cannot rewrite a repository's own automation. Three consequences, all handled the same way:

- The weekly sync repairs everything *except* workflow files, and reports the ones it had to leave.
- Dependabot PRs touching `.github/workflows/**` can never self-merge — every repo with the `github-actions` ecosystem produces these routinely.
- Updating a caller file itself needs the same escalation.

In each case the in-repo job does what its token allows, and the rest escalates to a local `gt repo fleet …` run using your own credentials. No long-lived token is stored anywhere. Pinning the callers to a moving major tag is what keeps this rare: gate logic changes touch no file.

### Getting started

```sh
gt repo init          # detect and seed .gt-repo.yaml
$EDITOR .gt-repo.yaml # prune what detection over-found, add notes
gt repo sync          # render the governance files
gt repo settings diff # preview branch protection changes
gt repo settings apply
```

Then add a setup template so new clones are governed automatically:

```yaml
setup:
  templates:
    - name: repo-governance
      match: ["github.com:pedromvgomes/*", "github.com/pedromvgomes/*"]
      run: gt repo sync --yes
```

## Coding-agent integration

This repo ships an `agentic/` directory that lets coding agents (Claude Code, Cursor, etc.) discover and follow the bare-repo + worktree workflow without you having to re-explain it every session.

```
agentic/
  skills/use-gt/
    SKILL.md                    # how to drive gt from an agent
    scripts/ensure-gt.sh        # idempotent installer the skill calls
  rules/
    worktree-per-session.md     # short "rules of the road" for agents
```

- **`agentic/skills/use-gt/`** is a Claude Code skill (with the standard `name` + `description` frontmatter). Point your agent at it once and it will reach for `gt clone` / `gt wt add` / `gt wt rm` instead of raw `git`, install `gt` on demand via the helper script, and ask the right pre-clone questions (which `gh` user to authenticate as, SSH vs HTTPS, etc.).
- **`agentic/rules/worktree-per-session.md`** captures the non-negotiables: always use the bare-repo layout, sessions start at the gt-managed root, and every session must `gt wt add` its own worktree before doing any work.

Why bother:

- **Consistency across repos.** Same auth wiring, same typed worktrees, same branch naming, regardless of which repo the agent is dropped into.
- **Parallel-safe sessions.** One worktree per session means multiple agent runs can work side-by-side without stepping on each other or on your default-branch checkout.
- **No bypassed validation.** Raw `git worktree add` skips the configured worktree-types check; routing through `gt` keeps everything inside the layout you've set up.
- **Self-bootstrapping.** The skill's `ensure-gt.sh` installs `gt` from the official release script if it's missing, so a fresh agent environment becomes productive on the first command.

## Shell completions

The installer prompts to install completions for zsh, bash, or fish when run interactively. You can regenerate them manually:

```sh
gt completion zsh > "$(brew --prefix)/share/zsh/site-functions/_gt"   # Homebrew zsh, already on $fpath
gt completion bash > ~/.local/share/bash-completion/completions/gt
gt completion fish > ~/.config/fish/completions/gt.fish
```

If you don't use Homebrew zsh, pick a directory on your `$fpath` (check with `echo $fpath | tr ' ' '\n'`) or add your own:

```sh
mkdir -p ~/.zsh/completions
gt completion zsh > ~/.zsh/completions/_gt
# then in ~/.zshrc, before `compinit`:
#   fpath=(~/.zsh/completions $fpath)
```

Restart your shell (or `exec zsh`) to pick up new completions. If oh-my-zsh has cached an older `compinit`, also run `rm -f ~/.zcompdump`.

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

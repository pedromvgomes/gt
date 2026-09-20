# Worktrees, auth and setup

The half of gt that runs on a developer's machine: `clone`, `wt`, `scratch`,
`set-auth`, `set-ssh`, `setup`, `config`.

## The layout

`gt clone` produces a **gt-managed root**, not a checkout:

```
<root>/
  .bare/                 # the bare repository
  .git                   # a file pointing at .bare
  <default-branch>/      # the default-branch worktree
  feature/ fix/ chore/   # typed worktree directories, from config.worktree_types
  scratch/               # the single scratch worktree, the user's own
  .envrc                 # written by set-auth; direnv exports GH_TOKEN and the profile
  .gt.yaml               # optional per-repo config, NOT committed inside the repo
```

Two facts hold the layout together and are easy to break:

- **`clone.Run` sets `core.bare=false` on `.bare` before any worktree exists.**
  Every worktree in the layout depends on that one-time fix-up.
- **Every worktree/scratch git call passes `dir=""`** and works only because
  `git.FindRepoRoot` returns an absolute root, which makes `--git-dir`
  absolute. A relative root silently breaks every one of them.

Both are recorded in the memory store; read `agtk memory show
clone-flips-core-bare-false` and `bare-git-calls-rely-on-absolute-root` before
touching `internal/clone` or `internal/worktree`.

`worktree.ParseSpec` is what validates `<type>/<name>` against
`config.worktree_types`. Raw `git worktree add` bypasses it, the branch-naming
rules, and the direnv wiring — which is what `agentic/rules/worktree-per-session.md`
tells agents, and what the `use-gt` skill exists to make automatic.

The default-branch worktree is never touched by `wt list`/`wt nuke` **only
because it lives outside `cfg.WorktreeTypes`** — no code protects it by name.
Adding the default branch to the type list would make `nuke` delete it.

## Auth and environment profiles

`gt set-auth` writes an idempotent `.envrc` at the managed root exporting
`GH_TOKEN` from `gh auth token --user <name>`, runs `direnv allow`, and appends
the matching profile's variables to the same file.

The problem profiles solve: a coding-agent CLI picks its credential store from
an environment variable — `CLAUDE_CONFIG_DIR`, `CODEX_HOME` — and inherits
whatever the launching shell exported, so the same command in the same worktree
authenticates as a different account depending on which terminal started it.

- gt attaches **no meaning** to the names or values. A CLI it has never heard
  of needs no gt release, just another entry under `env`.
- `match` is checked against the `origin` URL, **first match wins**, so order
  matters. A profile with no `match` never applies on its own.
- **Nothing is opt-out by default.** With no profile matching, the `.envrc` is
  byte-identical to what gt wrote before profiles existed, so a sweep over
  existing clones rewrites none of them. `envrc-bytes-must-not-drift` in the
  memory store is this invariant, and `EnvrcContent` is where it lives.
- Values get `~/` or `$HOME` expanded and are then emitted literally, with `"`,
  `$`, `` ` `` and `\` escaped.
- **There is no prompt.** `gt clone` and `set-auth` run from clone hooks and
  CI, where a question is a hang.

Without `--user`, gt reuses the user already recorded in a managed `.envrc`,
which is what makes a bare `gt set-auth` the way to retrofit an existing clone.

## Setup templates

Templates run after `gt clone` and on demand via `gt setup`. They are plain
shell; gt does not introspect them.

- `name` plus exactly one of `run` (inline) or `script` (an executable path).
- `match` is glob-as-substring against the clone URL. An empty `match` makes
  the template runnable only via `--setup <name>`.
- Environment: `GT_ROOT`, `GT_WORKDIR`, `GT_LAYOUT` (`bare` or `plain`),
  `GT_DEFAULT_BRANCH`, `GT_REPO_OWNER`, `GT_REPO_NAME`, `GT_REPO_URL`, and
  `GT_SETUP_PHASE` (`clone`, `worktree` or `manual`).
- gt prints the plan and prompts `[Y/n/d]`, skipped with `--yes` or no TTY.
- On the first non-zero exit gt stops and tells you to resume with
  `gt setup --from <template>`.

### The security model, which is not negotiable

Templates in your **global config** and in a **per-repo `<root>/.gt.yaml`** run
as you, with your environment. Both live on your machine where you put them.
Templates a **repository ships in its own committed `.gt.yaml`** are untrusted:
gated behind an explicit confirmation, and never auto-run without a TTY unless
`--yes` is passed. The untrusted gate is **plan-wide**, not per-template — see
`setup-untrusted-gate-is-plan-wide` in the memory store. Anything that would
let a committed template run unprompted is a security regression, not a
convenience.

## Staleness

`gt wt add` fetches origin and branches from `origin/<default>`, so a new
worktree is current. **Nothing does that for the long-lived default-branch
checkout.** Before reporting what is on `main`, what a tag points at, or what
the latest release is, `git fetch origin --tags --force` and read `origin/main`.
gt force-moves its major alias on every release, so `v1` is the ref most likely
to be stale — and a stale ref does not fail, it answers.

# Dependabot and auto-merge

`.gt-repo.yaml` declares the ecosystems; gt renders `.github/dependabot.yml`
from them, with the cooldown and the commit-message prefixes coming from gt's
templates rather than the spec, so changing them everywhere is one release.

`gt repo init` detects ecosystems from the files on disk (`repogov/detect.go`,
which collapses npm and cargo workspaces to their root). Detection over-finds
on purpose; pruning it is a human step.

## The merge executor

The cooldown in `.github/dependabot.yml` is the supply-chain guard: by the time
a PR exists the upstream release has been in the wild long enough to surface
yanks and compromised publishers. `reusable-dependabot-auto-merge.yml` is only
the merge executor on top of that.

Policy comes from `gt repo config --json`, **never from yq** — `//` is the
alternative operator, so it treats an explicit `false` exactly like an absent
key, and `delete_branch: false` once read back as true.

```yaml
dependabot_auto_merge:
  enabled: true
  max_bump: minor        # patch | minor | major — above this, a human reviews
  delete_branch: true
  github_app: false
```

Three details in that workflow are load-bearing and easy to undo:

- **Process substitution, not a pipe**, feeding the `while read` loop, so
  per-PR state (`failed`) survives past `done` under `set -e`.
- **`--limit 200`** on `gh pr list`. gh defaults to 30 and silently drops the
  oldest bumps once a repo has more open PRs than that.
- **Mergeability is computed lazily.** The first read returns `UNKNOWN` and
  starts the calculation, so a freshly opened PR is almost always `UNKNOWN` on
  the first look. The loop re-reads until it settles and treats a still-unknown
  answer as not mergeable, which is the safe direction.

`UNSTABLE` is eligible: required checks passed and only optional ones failed,
and branch protection already enforces the required set.

## Title parsing

Dependabot's format is stable — `bump <pkg> from <old> to <new>`, optionally
` in /path` — but two variants have each broken classification once:

- **A bare major.** `bump actions/checkout from 4 to 5` when the action is
  pinned to a moving tag. Demanding a full semver skipped precisely the PRs
  that touch `.github/workflows/**`, which is the whole reason `merge-pending`
  exists.
- **A requirement operator.** `update cargo-nextest requirement from =0.9.143
  to =0.9.144` — cargo's exact-version operator, Ruby's `~>`, npm's `^`. A
  repository pinning its toolchain in cargo manifests opens mostly these.

Both are handled by `dependabotTitle` in `fleet.go` and by the equivalent bash
regex in the workflow; **the two must be changed together.** A compound range
(`from >=1.0,<2.0`) deliberately does **not** match: its halves reduce to no
single old/new pair, and a wrong answer here merges a bump nobody classified.
No match means the PR is reported as needing a human.

In bash the pattern is held in a variable, not written inline: bash parses `(`,
`)`, `|` and `<` in an unquoted `=~` pattern as shell metacharacters, but never
inside a variable's expansion.

## Merging behind strict required checks

Both the workflow and `MergePending` merge with `gh pr merge --squash`, adding
`--delete-branch` — the workflow when `dependabot_auto_merge.delete_branch` is
true (the default), `MergePending` always. Neither asks GitHub anything about
the base branch first: gt applies no merge queue, and a queue is the one thing
that makes gh refuse `--delete-branch` outright, before merging.

What does shape the merge is strict required checks, which every governed
repository with a gate has. Each merge to the base leaves every other open
Dependabot PR with `mergeStateStatus` `BEHIND`, and both paths merge only
`CLEAN`, `HAS_HOOKS` and `UNSTABLE`. A `BEHIND` PR is reported as "behind the
base branch" and skipped, and the path asks Dependabot to rebase it by
commenting exactly `@dependabot rebase`: the workflow once per `BEHIND` PR per
run (a failed comment does not fail the job; it needs `pull-requests: write`,
which the job holds), `merge-pending` only with `--merge`
(`repogov.RequestRebase`). Only Dependabot PRs are ever listed, so no other
author's PR is commented on.

## The workflow-scope gap

GitHub blocks `GITHUB_TOKEN` from writing anything under `.github/workflows/`,
and no `permissions:` key grants the scope. So:

- The weekly sync repairs everything *except* workflow files and reports what
  it left.
- Dependabot PRs touching workflows can never self-merge — every repo with the
  `github-actions` ecosystem produces these routinely.

Two escapes, and no long-lived token anywhere:

- `dependabot_auto_merge.github_app: true` mints a token from a GitHub App
  (`APP_CLIENT_ID` / `APP_PRIVATE_KEY`). Opting in without the secret **fails
  the run** rather than degrading — falling back to `GITHUB_TOKEN` would look
  green while doing exactly what the opt-in was meant to stop.
- `gt repo fleet merge-pending --owner <name> [--merge]` runs locally with your
  own credentials, applying the same eligibility gates as the in-repo job so
  the escalation path cannot be used to bypass them.

Secrets are **declared, not inherited**: `secrets: inherit` is documented as
working within one organization or enterprise, and gt lives under a different
owner than the repositories calling it. The lydite stage lost its Codecov
token to exactly that, silently.

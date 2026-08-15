# Centrally-owned CI pipeline with repo extension points

Status: **design, not implemented.** Replaces the polling gate currently in
`internal/repogov`.

## Why

The gate as built aggregates a repository's checks by polling the Checks API,
because a reusable workflow cannot `needs:` into the consumer's own `ci.yml`.
That polling is the most fragile part of the system and the source of a
disproportionate share of review findings: a timeout to tune, an
absent-versus-not-started ambiguity no runtime signal can resolve, and a static
trigger lint that exists purely to compensate for that ambiguity.

If gt owns the pipeline's entry point instead, every stage is a job in one
workflow, so aggregation goes back to `needs:` and `needs.*.result` — free,
immediate, unambiguous. The polling loop, its timeout, and the trigger lint all
stop existing.

The same move fixes double-running tests: the orchestrator sequences tests
before bulwark and hands the coverage artifact across, so bulwark never re-runs
a suite the repo already ran.

## Two constraints that shape the design

**Subdirectories are not allowed.** `.github/workflows` may not contain
subdirectories — [not for triggered workflows and not for reusable
ones](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows).
Grouping therefore comes from a filename prefix: the pipeline families are
`ci-*` and `cd-*`, so each sorts together and reads as one set.

**Local `uses:` resolution is not reliably documented.** Whether `uses: ./…`
inside a called workflow resolves against gt or against the consumer is
[described inconsistently](https://github.com/orgs/community/discussions/18601),
mostly for composite actions rather than workflow-to-workflow calls. So the
orchestrator is **rendered into each consumer repository** rather than living in
gt as a reusable workflow; there, `./.github/workflows/ci-build.yml` is
unambiguously the consumer's own file.

If that resolution turns out to hold for workflow calls, the orchestrator could
collapse back into a thin caller. Cheap to test once `v0` exists — not something
to assume now.

## Shape

| File | Owner | Sync behaviour |
|---|---|---|
| `.github/workflows/ci-orchestration.yml` | gt | **managed** — always overwritten |
| `.github/workflows/ci-preflight.yml` | repo | **scaffold** — created once, never overwritten |
| `.github/workflows/ci-build.yml` | repo | **scaffold** |
| `.github/workflows/ci-test.yml` | repo | **scaffold** |
| `.github/workflows/ci-end2end.yml` | repo | **scaffold** |
| `.github/workflows/cd-orchestration.yml` | gt | **managed** |
| `.github/workflows/cd-preflight.yml` | repo | **scaffold** |
| `.github/workflows/cd-publish.yml` | repo | **scaffold** |
| `.github/workflows/cd-deploy.yml` | repo | **scaffold** |
| `.github/workflows/cd-verify.yml` | repo | **scaffold** |
| `reusable-*.yml` in gt | gt | referenced by the pinned `@v0` tag |

Publishing is a `cd-` stage, not a `ci-` one. An earlier draft scaffolded
`ci-publish.yml` that nothing called, which was the tell.

`ci-orchestration.yml` is deliberately not `ci.yml`: every repo already has a `ci.yml`
with real jobs, and a managed file would overwrite it on the first sync, before
anyone had moved those jobs into the stages.

## The orchestrator

```yaml
name: PR
on:
  # No `branches:` filter, so PRs stacked onto a feature branch run too.
  pull_request:
    types: [opened, synchronize, reopened, edited, ready_for_review]
  # Push to the default branch, so an already-validated tree can skip.
  push:
    branches: [main]
  # merge_group only when pipeline.ci.merge_queue is set; without it a queued
  # PR waits forever for a required check that never reports.
  workflow_dispatch:

permissions:
  contents: read

jobs:
  # On a push, reports whether this exact tree already carries a passing
  # attestation from the PR that produced it. On a PR, a passthrough.
  attest:
    uses: pedromvgomes/gt/.github/workflows/reusable-attest.yml@v0

  preflight:
    needs: attest
    if: needs.attest.outputs.validated != 'true'
    uses: ./.github/workflows/ci-preflight.yml
    secrets: inherit

  build:
    needs: [attest, preflight]
    if: needs.attest.outputs.validated != 'true' && needs.preflight.outputs.run-build != 'false'
    uses: ./.github/workflows/ci-build.yml
    secrets: inherit

  test:
    needs: [attest, preflight, build]
    if: needs.attest.outputs.validated != 'true' && needs.preflight.outputs.run-test != 'false'
    uses: ./.github/workflows/ci-test.yml
    secrets: inherit

  end2end:
    needs: [attest, preflight, build]
    if: needs.attest.outputs.validated != 'true' && needs.preflight.outputs.run-end2end != 'false'
    uses: ./.github/workflows/ci-end2end.yml
    secrets: inherit

  bulwark:
    needs: [attest, test]
    if: needs.attest.outputs.validated != 'true'
    uses: pedromvgomes/gt/.github/workflows/reusable-bulwark.yml@v0
    secrets: inherit

  conventional-commits:
    uses: pedromvgomes/gt/.github/workflows/reusable-conventional-commits.yml@v0

  governance:
    uses: pedromvgomes/gt/.github/workflows/reusable-governance.yml@v0

  ci-gate:
    name: ci-gate
    needs: [attest, preflight, build, test, end2end, bulwark, conventional-commits, governance]
    if: always()
    runs-on: ubuntu-latest
    steps:
      - name: Verify every stage succeeded or was legitimately skipped
        env:
          RESULTS: ${{ toJSON(needs.*.result) }}
        run: |
          set -euo pipefail
          echo "$RESULTS"
          if [[ "${{ contains(needs.*.result, 'failure') }}" == "true" ]]; then
            echo "::error::a required stage failed"; exit 1
          fi
          if [[ "${{ contains(needs.*.result, 'cancelled') }}" == "true" ]]; then
            echo "::error::a required stage was cancelled"; exit 1
          fi
```

Fixed stages stay in gt's reusable workflows, so their logic still changes
without touching any repo. Only the stage list lives in the rendered file, and
that changes rarely.

## Continuous delivery

`cd-orchestration.yml` mirrors the CI shape, so there is one pipeline idea to
learn rather than two:

```yaml
name: CD
on:
  push:
    tags: ["v*.*.*"]        # from pipeline.cd.tags — repos ship on different patterns
  workflow_dispatch:

permissions:
  contents: read

jobs:
  # Refuses to ship a tree that never passed the gate.
  verify-attestation:
    uses: pedromvgomes/gt/.github/workflows/reusable-attest.yml@v0
    with:
      require: true

  preflight:
    needs: verify-attestation
    uses: ./.github/workflows/cd-preflight.yml
    secrets: inherit

  publish:
    needs: preflight
    if: needs.preflight.outputs.run-publish != 'false'
    uses: ./.github/workflows/cd-publish.yml
    secrets: inherit

  deploy:
    needs: [preflight, publish]
    if: needs.preflight.outputs.run-deploy != 'false'
    uses: ./.github/workflows/cd-deploy.yml
    secrets: inherit

  verify:
    needs: deploy
    if: needs.preflight.outputs.run-verify != 'false'
    uses: ./.github/workflows/cd-verify.yml
    secrets: inherit

  cd-gate:
    name: cd-gate
    needs: [verify-attestation, preflight, publish, deploy, verify]
    if: always()
    runs-on: ubuntu-latest
    steps: …same failure/cancelled check as ci-gate…
```

`cd-gate` is not a branch-protection check — nothing merges here. It exists so a
release has one status that means "the whole delivery succeeded", rather than
requiring someone to read four job results.

Tag patterns are per repo (`pipeline.cd.tags`): gt ships on `v*.*.*`, while
wardnet-cloud releases several independently-versioned components. Both
orchestrations also carry `workflow_dispatch`, so either can be run against a
chosen branch from the Actions UI.

## Validated-tree attestation

The question "was this PR tested against the code it is landing on?" can be
answered as a fact rather than enforced as a policy, and that is strictly
better.

For `pull_request` events GitHub does not test the PR branch — it tests
`refs/pull/N/merge`, the *merged result* of the PR into its base as it stood at
the time. A squash merge produces a commit whose tree is that same merge
result. So the tree `ci-gate` validated and the tree that lands on the default
branch are directly comparable.

On success `ci-gate` records what it validated, as a commit status on the PR
head:

```
context:     gt/validated-tree
description: <tree SHA of refs/pull/N/merge>
state:       success
```

Anything downstream then compares that against the tree actually in hand.

### This replaces `require_up_to_date` and the freshness job

Both existed only to make "tested against current base" *true by construction*.
The attestation makes it *checkable*, which is better on every axis:

- No one has to rebase, so a merge never turns other open PRs red.
- It is content-based. If the base moved in a way that changes the merged tree,
  the trees differ and CI runs. If it somehow does not, nothing was missed.
- It fails safe: no attestation, or a mismatch, means run CI. The check can only
  ever be conservative.
- It works everywhere, including `pedromvgomes/*`, where merge queues cannot be
  enabled at all.

`settings apply` therefore leaves `require_up_to_date` false, and there is no
CI/CD coupling rule to validate — the guarantee is carried by evidence rather
than configuration.

### Skipping CI on the default branch

`ci-orchestration` also runs on push to the default branch, where an `attest`
job asks whether the pushed commit's tree already carries a passing
attestation from the PR that produced it:

- **Match** — every stage skips. The identical tree already passed; re-running
  proves nothing.
- **No match** — the stages run as normal, and on success `attest` writes
  `gt/validated-tree` onto the pushed commit itself.

Either way the default-branch commit ends up carrying an attestation, which is
what makes the next part possible.

### CD verifies rather than trusts

`cd-orchestration` does not re-run the CI gauntlet, and no longer needs to
trust that it was run. It checks that the tagged commit carries a passing
`gt/validated-tree` matching its own tree, and refuses to publish otherwise.

That is a stronger guarantee than wardnet's release-time re-run: a re-run tells
you the code passes *now*, on a runner, again; the attestation tells you this
exact tree passed the full gate, and costs nothing.

Its one real limit is worth stating: an attestation is only as good as the
suite that produced it. A flaky test that happened to pass on this tree
attests just as confidently as a solid one.

### Stacked PRs

`ci-orchestration.yml` carries **no `branches:` filter** on its `pull_request`
trigger. Filtering to `[main]` would mean a PR stacked onto a feature branch
ran no CI at all — the worst outcome, since a stack's intermediate steps are
exactly where mistakes hide.

Attestation needs no special handling for stacks. Each PR in a stack is tested
against its own base and attests its own merged tree; only the tree that
finally lands on the default branch is ever compared against.

### Merge queues

A merge queue remains available and is now orthogonal rather than an
alternative. It tests each PR against the base tip plus those ahead of it, so
it prevents semantic conflicts between PRs that pass individually — something
attestation does not do, since it only reports what *was* tested.

It cannot be a default: merge queue requires an organization-owned
repository — public, or private on Enterprise Cloud — so `pedromvgomes/gt`,
`agentic-toolkit` and `boma` can never have one.

With `pipeline.ci.merge_queue: true`, `ci-orchestration.yml` also triggers on
`merge_group`; without that trigger a queued PR waits forever for a required
check that never reports. Attestation continues to work unchanged, recording
whatever tree the queue actually validated.


## The preflight contract

wardnet gates each leaf on a `detect-changes` preflight so untouched areas skip
rather than rebuild. That generalises if preflight emits one conventional
output per stage:

```
run-build, run-test, run-end2end   # "false" skips the stage; anything else runs
```

The orchestrator gates on `!= 'false'`, so the scaffolded stub — which emits
nothing — runs everything. A repo opts into change detection by filling in
`ci-preflight.yml`; it never has to opt out.

Skipped stages pass `ci-gate`, matching wardnet's `all-checks-passed`. That is
the point of change detection: a skipped leaf is a legitimate outcome, not a
missing one.

## `ci-gate` is a job, not a file

It is tempting to give the gate its own `ci-gate.yml`, since it serves a
different purpose from the stages: it is the one check branch protection names.
It cannot be a separate file, because **`needs:` does not cross workflows.** A
standalone gate workflow could only learn the stages' results by polling the
Checks API — reintroducing the timeout, the ambiguity and the lint this design
exists to delete.

This is what wardnet already does: `all-checks-passed` is a job at
`pr.yml:220`, alongside `preflight` and the build leaves, not a workflow of its
own.

So `ci-orchestration.yml` holds the stages *and* the `ci-gate` job. That still
gives a single gt-managed required check that accounts for which stages ran —
the `if: always()` job fails only on `failure` or `cancelled`, so a stage
preflight skipped passes. Only the file boundary is different, and keeping the
`ci-` prefix on the job name means the required check still reads as part of
the same family.

## The required check becomes `ci-gate`

`ci-gate` is a plain job in a workflow the repository owns, so it reports under
its own name with no prefix — `ci-gate`, not `PR / Gate`.

Branch protection has to move in the same window as the merge, or PRs block on a
check that can never report. `gt repo settings apply` handles it; the ordering
is not optional.

## Coverage contract

`ci-test.yml` uploads an artifact named `gt-coverage` containing whatever
profiles it produced. The bulwark stage downloads it when present, and falls
back to running the suite itself when absent.

That is the whole double-run fix, and it is a convention rather than
configuration: nothing to wire beyond uploading under the agreed name.

It also settles the `.bulwark.yml` question. `report` becomes the normal path
and `run` the fallback, decided by the orchestrator rather than per repo — so
gt's spec keeps only `bulwark.enabled` and `bulwark.dir` (the scan root, which
bulwark must know before it can read `.bulwark.yml` at that root).

## Scaffold files are a genuine addition

`uses:` cannot be conditional on a file existing, so every stage the
orchestrator names must exist. gt creates the five `ci-*` stubs and then **never
touches them again** — they are where each repo's real work goes.

That breaks two current invariants:

- `Diff` treats any difference from rendered content as drift. Scaffolds must be
  compared for *existence* only.
- Orphan detection deletes managed files the spec no longer wants. It must never
  delete a scaffold, which by definition holds work gt did not write.

Concretely: `File` grows a `Mode` (`managed` / `scaffold`), `Diff` skips content
comparison for scaffolds, `Write` creates them only when missing, and orphan
detection ignores them.

## Configuration

```yaml
pipeline:
  ci:
    enabled: true
    stages: [preflight, build, test, end2end]
    merge_queue: false      # org-owned repos only; adds the merge_group trigger
  cd:
    enabled: true
    stages: [preflight, publish, deploy, verify]
    tags: ["v*.*.*"]        # wardnet-cloud ships several independent patterns
```

`uses:` cannot be computed at runtime, so the stage list is baked into the
rendered orchestrator. Adding a stage is a re-render — a workflow-file change,
so a local `gt repo fleet sync`. Rare by design.

Ordering is fixed: preflight → build → (test, end2end) → bulwark. Deliberately
not configurable; a central pipeline every repo can rearrange is not a central
pipeline.

## Migration

Per repo: move build and test jobs out of the existing `ci.yml` into
`ci-build.yml` and `ci-test.yml`, have the test stage upload `gt-coverage`, and
delete what the orchestrator now owns. Real work per repository — considerably
more than the current gate, which sits beside the existing pipeline rather than
replacing it.

wardnet is no longer the outlier it was: `ci-preflight.yml` is exactly its
`detect-changes` job, and its build leaves map onto `ci-build.yml`. Its
per-area fan-out (daemon, site, admin-web, admin-app, user-app) still has to
collapse into one `ci-build.yml` that branches internally, which is the main
open question for that repo.

Push-to-`main` CI is out of scope: the orchestrator is PR-only. Repos keep what
they run on push until a `ci-main.yml` covers it.

## What this deletes

- The Checks API polling loop and `checks.timeout_minutes`
- `require_up_to_date` as a load-bearing setting, and the freshness job that
  backed it up — replaced by an attestation that is checked rather than assumed
- The absent-versus-not-started ambiguity
- `internal/repogov/lint.go` in full — the trigger lint exists only to make that
  ambiguity statically detectable
- `checks.required` / `checks.optional`, now `needs:` edges

A large net simplification, removing the code reviews have found most problems
in.

## Open questions

1. Does `uses: ./…` inside a called workflow resolve against the caller? If so
   the orchestrator can be a thin caller again. Cheap to test once `v0` exists.
2. `ci-publish.yml` is scaffolded but nothing calls it until a release
   orchestrator exists. Harmless — an uncalled `workflow_call` file never runs —
   but it is a loose end.
3. Does `end2end` belong in the PR gate or on a schedule? wardnet runs it per
   PR; tumika has none.

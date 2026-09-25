# The pipeline

See [`docs/pipeline-design.md`](../../docs/pipeline-design.md) for the long-form
rationale. This is what an agent needs before touching `repogov/pipeline.go` or
an orchestrator template.

## Shape

gt owns `ci-orchestration.yml` and `cd-orchestration.yml`. They wire stages
together and nothing else:

```
attest
  ├─ ci-preflight
  │    └─ ci-build
  │         ├─ ci-test
  │         └─ ci-end2end
  ├─ lydite
  └─ lydite-baseline   ← push to the default branch only, and outside the gate
conventional-commits, governance
  └─ ci-gate     ← the required check that aggregates every job above
```

The `lydite` job hangs off `attest` alone, not off `ci-test`: it forwards to
lydite's own reusable pipeline, which discovers and runs each unit's suite
itself. There is no artifact hand-off between `ci-test` and `lydite` — see
Coverage below.

`lydite-baseline` records the coverage baseline the pull-request gate compares
against, forwarding to gt's own `reusable-lydite-baseline.yml`. Two things set
it apart from every other job here: it waits on `attest` rather than on
`lydite` — a referral verdict has no bearing on what the baseline says — and it
is the one job **not** in `ci-gate`'s `needs:`. It runs only on a push to the
default branch, where there is nothing left to merge and the gate is
informational, so a baseline that failed to record is worth a red job and not a
red gate on a commit already on the branch. It is also the only job granted
`contents: write`, because recording a baseline writes back.

The `ci-*` and `cd-*` stage files are the **repository's**. gt writes each one
once as an empty stub and never touches it again — not to update it, not to
delete it when the stage is dropped. That is where a repo's build, tests and
deployment go.

`ci-preflight` decides what runs: it emits `run-build`, `run-test` or
`run-end2end` as `"false"` to skip a stage. The stub emits nothing, so
everything runs until a repository says otherwise. **A skipped stage passes the
gate** — see the note of the same name in the memory store.

## The gate, and the second required check

Branch protection always requires **`ci-gate`**. It is a plain job in a
workflow the repository owns, so its check name is just the job name, and
because every stage is a job in that same workflow it aggregates them with
`needs:` — no polling, no timeout, and no way to confuse "absent" with "not
started yet". Renaming a CI job means editing `.gt-repo.yaml`, never the
protection rule.

`gateNeeds` in `pipeline.go` builds that `needs:` list. It must name every
stage the spec enabled plus the fixed jobs, or the gate goes green on work that
never ran. `lydite` is one of those fixed jobs wherever `spec.Lydite.Enabled`
— but a referral verdict never fails the job, so `ci-gate` going green does not
mean lydite cleared the tree. `lydite-baseline` is deliberately not on that
list.

Wherever lydite is enabled, `settings.go`'s `desiredRuleset` also requires a
second, unrelated context: **`lydite/referral`** (`repospec.LyditeReferralContext`),
the commit status lydite's referral step publishes directly rather than a job
in this workflow. That is what actually blocks a referred pull request from
merging — see [`merge-queue.md`](merge-queue.md) for how required checks are
assembled, and the comment on `LyditeReferralContext` in `repospec/spec.go` for
why it has to be kept in sync with `lydite/lydite`'s `internal/clearance.Context`
by hand.

Clearing that status is `/lydite clear`, posted as a PR comment — answered by
`gt-lydite-clearance.yml`, a workflow of its own, wired to `issue_comment` rather
than folded into `ci-orchestration.yml`. An `issue_comment` run always executes
the *default branch's* copy of every workflow file, never the pull request's,
which is exactly the property a job that holds `statuses: write` needs; putting
it in `ci-orchestration.yml` would also mean every other job there re-running
on each comment. It forwards to gt's own `reusable-lydite-clearance.yml`, the
same indirection `lydite` itself uses, rendered wherever `spec.Lydite.Enabled`
— a repo with lydite off has no referral to clear.

## The attestation

For a `pull_request` event GitHub tests `refs/pull/N/merge` — the merged result
— and a squash merge produces a commit with that same tree. So `ci-gate`
records the tree it validated as a `gt/validated-tree` commit status
(`AttestContext`), and later runs compare against it:

- **push to the default branch** — a tree already carrying a passing
  attestation skips every stage. The identical tree passed; re-running proves
  nothing.
- **tag push** — `cd-orchestration` refuses to publish a tree carrying no
  passing attestation. Stronger than re-running CI, which only says the code
  passes now, again.

`attestGuard` is the expression every stage is gated on. It fails **safe in
every direction**: a missing, unreadable or mismatched attestation means run
the pipeline. Preserve that property in any change here — an attestation check
that fails closed on an unreadable status would block every release on a blip.

## Coverage

There is no artifact hand-off between `ci-test` and `lydite`. Testing, review
and publishing all live inside lydite's own commands (`lydite test`, `lydite
review`, `lydite publish`), which discover and run each unit's suite
themselves; `lydite` never reads `ci-test`'s output. `.bulwark.yml` is gone —
gt scaffolds no coverage configuration at all now, only the `lydite.enabled`,
`lydite.dir` and `lydite.relay` knobs in the spec. Everything about what gets
scanned, gated and reported is lydite's own config, read from the scan root
once lydite runs there. See [`docs/pipeline-design.md`](../../docs/pipeline-design.md)
for the full rationale.

The `lydite.relay` spec knob renders into the committed orchestrator; it is
not the same mechanism as the `GT_LYDITE_RELAY` Actions variable `gt repo
settings apply` sets on every `lydite.enabled` repo (`repogov/settings.go`),
which the reusable lydite workflows fall back to absent an explicit `relay`
input. See [ADR 0001](../../docs/adr/0001-lydite-relay-defaults-via-a-live-actions-variable.md).

## Permissions

`mergePermissions` merges per-stage `permissions:` maps, ranked
`none < read < write`. A stage gets the highest rank anything asked for. Widening
a default here widens it for every governed repository, so it is an "ask first"
change.

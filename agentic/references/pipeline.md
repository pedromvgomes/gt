# The pipeline

See [`docs/pipeline-design.md`](../../docs/pipeline-design.md) for the long-form
rationale. This is what an agent needs before touching `repogov/pipeline.go` or
an orchestrator template.

## Shape

gt owns `ci-orchestration.yml` and `cd-orchestration.yml`. They wire stages
together and nothing else:

```
attest
  └─ ci-preflight
       └─ ci-build
            ├─ ci-test ─── bulwark
            └─ ci-end2end
conventional-commits, governance
  └─ ci-gate     ← the one required check
```

The `ci-*` and `cd-*` stage files are the **repository's**. gt writes each one
once as an empty stub and never touches it again — not to update it, not to
delete it when the stage is dropped. That is where a repo's build, tests and
deployment go.

`ci-preflight` decides what runs: it emits `run-build`, `run-test` or
`run-end2end` as `"false"` to skip a stage. The stub emits nothing, so
everything runs until a repository says otherwise. **A skipped stage passes the
gate** — see the note of the same name in the memory store.

## One required check, forever

Branch protection requires exactly one check: **`ci-gate`**. It is a plain job
in a workflow the repository owns, so its check name is just the job name, and
because every stage is a job in that same workflow it aggregates them with
`needs:` — no polling, no timeout, and no way to confuse "absent" with "not
started yet". Renaming a CI job means editing `.gt-repo.yaml`, never the
protection rule.

`gateNeeds` in `pipeline.go` builds that `needs:` list. It must name every
stage the spec enabled plus the fixed jobs, or the gate goes green on work that
never ran.

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

`ci-test` uploads coverage as an artifact named `gt-coverage`
(`CoverageArtifact`), and the bulwark stage extracts it instead of running the
suite a second time. `.bulwark.yml` must then say `coverage.source: report`;
`run` is not merely slower here, it is wrong, because bulwark's fallback runs
the suite without `-coverpkg` and every test lives in `./tests` while the code
lives in `./internal`.

## Permissions

`mergePermissions` merges per-stage `permissions:` maps, ranked
`none < read < write`. A stage gets the highest rank anything asked for. Widening
a default here widens it for every governed repository, so it is an "ask first"
change.

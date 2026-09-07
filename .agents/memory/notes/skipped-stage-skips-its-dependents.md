---
name: skipped-stage-skips-its-dependents
kind: gotcha
description: Rendered stage jobs carry no status-check function in their `if:`, so a preflight-skipped stage silently skips every stage that needs it — only bulwark is guarded.
anchors:
  - path: internal/repogov/pipeline.go
    blob: 746d072adfa4
  - path: internal/repogov/templates/workflows/*-orchestration.yml.tmpl
    matches:
      - path: internal/repogov/templates/workflows/cd-orchestration.yml.tmpl
        blob: fc436200cae7
      - path: internal/repogov/templates/workflows/ci-orchestration.yml.tmpl
        blob: b0890b65871e
  - path: tests/repogov_pipeline_test.go
    blob: d952808affb2
confidence: verified
---

GitHub Actions prefixes every job's `if:` with an implicit `success()` unless the condition
already contains a status-check function (`success()`, `failure()`, `cancelled()`,
`always()`). A job whose `needs:` includes a job that was *skipped* is therefore skipped too,
whatever its own `if:` says.

`buildStages` (`internal/repogov/pipeline.go:151-217`) builds each stage's `If` from the
pipeline-level guard (`pipeline.go:193-196`) plus
`needs.preflight.outputs.run-<stage> != 'false'` (`pipeline.go:199-200`) and never adds a
status-check function. The wiring chains stages:
`test` and `end2end` both `needs: build` (`ciWiring`, `pipeline.go:132-137`), and
`deploy`/`verify` chain behind `publish` (`cdWiring`, `pipeline.go:139-144`). So if a repo's
`ci-preflight.yml` sets `run-build: 'false'` while leaving `run-test` unset, `test` is skipped
anyway — not by its own gate, but by the implicit `success()` on a skipped `build`. The
preflight contract in `docs/pipeline-design.md` does not warn about this.

Only `bulwark` is guarded: `ci-orchestration.yml.tmpl:132` renders
`if: "!cancelled() && {{ .SkipGuard }}"` with the reason in the comment above it (`:128-131`),
so a skipped `test` does not take it down. `TestBulwarkSurvivesASkippedTestStage`
(`tests/repogov_pipeline_test.go:469`) is the only test covering this. Nothing equivalent
exists for `test`/`end2end` behind `build`, or `deploy`/`verify` in CD, and nothing states
whether that asymmetry is intended.

The escape hatch is `independent_stages`: a stage named there drops its sibling `needs:`
entirely, keeping only `preflight` (`pipeline.go:183-189`), so the propagation cannot reach it.
Anyone adding a stage, reordering the wiring maps, or writing a preflight that skips an
intermediate stage should decide between the `!cancelled()` treatment and
`independent_stages`.

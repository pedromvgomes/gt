---
about: internal/repogov/templates/workflows/ci-orchestration.yml.tmpl
saw: dispatched during the gt-runs-lydite-full-pipeline branch, which renamed the bulwark job and rewrote its skip guard
targets: skipped-stage-skips-its-dependents
verdict: needs-update
---

The existing note `skipped-stage-skips-its-dependents` says "Only `bulwark`
is guarded" against the implicit-`success()` skip propagation, citing
`ci-orchestration.yml.tmpl:132` (`if: "!cancelled() && {{ .SkipGuard }}"`) and
`TestBulwarkSurvivesASkippedTestStage`. Both are now wrong:

- The job was renamed `bulwark` → `lydite`
  (`internal/repogov/pipeline.go`, `fixed = append(fixed, "lydite")`).
- Its `if:` guard is no longer `"!cancelled() && {{ .SkipGuard }}"` — the
  `{{ .SkipGuard }}` clause was removed entirely, so it now reads
  `if: "!cancelled()"` alone (`ci-orchestration.yml.tmpl`, the `lydite:` job).
  This job survives *every* skipped dependency unconditionally now, not just
  a skipped `test` stage, because it is the sole publisher of the required
  `lydite/referral` status and must never be skipped — see the new rule
  `agentic/rules/never-skip-the-sole-publisher-of-a-required-status.md`.
- The test is renamed `TestBulwarkSurvivesASkippedTestStage` →
  `TestLyditeSurvivesASkippedDependency`
  (`tests/repogov_pipeline_test.go`).

The note's core finding — that `test`/`end2end` behind `build`, and
`deploy`/`verify` behind `publish`, have no equivalent guard, and
`independent_stages` is the escape hatch — is untouched by this branch and
still holds. Only the `lydite`/`bulwark`-specific paragraph and its anchors
need rewriting.

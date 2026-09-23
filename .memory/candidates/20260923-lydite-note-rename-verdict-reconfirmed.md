---
about: the already-staged "needs-update" verdict on skipped-stage-skips-its-dependents (bulwark -> lydite rename) still matches the current tree
saw:
  - internal/repogov/templates/workflows/ci-orchestration.yml.tmpl
  - internal/repogov/pipeline.go
  - tests/repogov_pipeline_test.go
targets: skipped-stage-skips-its-dependents
verdict: now-false
---

Re-checked while investigating issue #77 (adding a `relay` field to the
`Lydite` spec). Confirms the existing candidate
`20260920-skipped-stage-note-is-stale-after-the-lydite-rename.md`: there is
no `bulwark` job or stage anywhere in `internal/repogov/pipeline.go` or
`internal/repogov/templates/workflows/*.tmpl` today (only a stray
`.bulwark.yml` testdata filename unrelated to CI stage naming, at
`tests/repogov_pipeline_test.go:817`). The `lydite:` job's `if:` is
`"!cancelled()"` alone, no `{{ .SkipGuard }}` clause
(`ci-orchestration.yml.tmpl:138`), and the covering test is
`TestLyditeSurvivesASkippedDependency` (`tests/repogov_pipeline_test.go:704`).

No new information beyond the existing candidate; filing this only to record
that a second, independent read confirms it, since the base note itself has
sat un-promoted with a stale anchor through at least two more commits.

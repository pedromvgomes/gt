---
about: a new job in ci-orchestration.yml.tmpl gated only on {{- if .Lydite }} silently inherits contents:write and runs on every default-branch push even where spec.Lydite.Coverage is false, because coverage is a separate condition the lydite job checks with {{- if not .LyditeCoverage }} rather than something .Lydite implies
saw:
  - internal/repogov/templates/workflows/ci-orchestration.yml.tmpl
  - internal/repogov/pipeline.go
  - tests/repogov_pipeline_test.go
---

`lydite: bool` (`ciData`, `pipeline.go`) gates the whole `{{- if .Lydite }}`
block that both the `lydite:` job and (once added) `lydite-baseline:` sit
inside. `LyditeCoverage: bool` is a separate field, read only inside the
`lydite:` job body to add `with: coverage: false` when it is off
(`ci-orchestration.yml.tmpl`, `{{- if not .LyditeCoverage }}`) — it is not
part of the outer `.Lydite` gate, so a job added alongside `lydite:` that
only checks `.Lydite` renders unconditionally whenever lydite is enabled,
regardless of the coverage setting.

Caught by a security-review finding on the `lydite-baseline` job: it recorded
a coverage baseline (holding `contents: write`) on every push to the default
branch even for a repo with `spec.Lydite.Coverage == false` — one the
existing `lydite:` job's own comment already documents as "nothing here for
lydite to measure." The fix wraps the whole `lydite-baseline:` block in its
own `{{- if .LyditeCoverage }}`, nested inside the existing `{{- if .Lydite }}`.
Any future job added to this same conditional region needs the same check
made explicit — `.Lydite` alone does not imply coverage is on.

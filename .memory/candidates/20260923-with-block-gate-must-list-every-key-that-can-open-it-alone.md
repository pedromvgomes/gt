---
about: a template's outer `with:` gate (an `{{- if or A B }}` guarding a job's whole `with:` block) has to name every field that can populate the block on its own, or a spec setting only the missing field renders no `with:` block at all
saw:
  - internal/repogov/templates/workflows/ci-orchestration.yml.tmpl
  - internal/repogov/templates/workflows/gt-lydite-clearance.yml.tmpl
  - tests/repogov_pipeline_test.go
---

`ci-orchestration.yml.tmpl`'s `lydite:` job and `gt-lydite-clearance.yml.tmpl`'s
`clearance:` job each wrap their whole `with:` block in one outer
`{{- if or ... }}` gate, then gate each individual key (`dir`, `coverage`,
`relay`) inside it again. Adding a new conditional key inside the block does
nothing on its own — the outer gate has to list that field too, or a spec that
sets only the new field renders no `with:` block at all, silently dropping the
value gt/actions would otherwise have inherited.

This is easy to get half-right: a test that exercises the new field alongside
an existing one that already opens the gate (e.g. setting `relay` together with
`dir`) passes even when the outer-gate extension is missing, because the
existing field opens the block regardless. Isolating the new field alone in
its own test is what actually exercises the outer gate —
`TestLyditeJobForwardsRelay`/`TestLyditeClearanceForwardsRelay` in
`tests/repogov_pipeline_test.go` set `Relay` with `Dir`/`Coverage` left at spec
defaults for exactly this reason.

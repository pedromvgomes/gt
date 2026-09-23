---
about: the established path for threading a new .gt-repo.yaml field into a called reusable workflow, exemplified end-to-end by Lydite.Dir/Lydite.Coverage
saw:
  - internal/repospec/spec.go
  - internal/repogov/pipeline.go
  - internal/repogov/templates/workflows/ci-orchestration.yml.tmpl
  - .github/workflows/reusable-lydite.yml
---

Adding `Lydite.Dir`/`Lydite.Coverage` and forwarding them to
`reusable-lydite.yml` took four hops, all present today:

1. Spec field on `Lydite` (`spec.go:128-142`), tagged `yaml:"...,omitempty"`
   for a string default, plain for a bool default (Coverage defaults `true`
   via `Default()` at `spec.go:520`, so it cannot use `omitempty` — a bool
   zero value is a real, distinct value).
2. Template-data struct field: `ciData.LyditeDir`/`LyditeCoverage`
   (`pipeline.go:337-338`), populated in `buildCIData` straight from
   `in.Spec.Lydite.*` (`pipeline.go:389-390`). Same shape again in
   `lyditeClearanceData.LyditeDir` (`pipeline.go:317-329`) for the clearance
   workflow's own template.
3. Conditional emission in the `.tmpl`: `ci-orchestration.yml.tmpl:145-158`
   only writes a `with:` block, and only the changed keys inside it, when a
   value differs from the reusable workflow's own default (`{{- if .LyditeDir
   }}`, `{{- if not .LyditeCoverage }}`) — an unmodified default renders no
   `with:` line at all, keeping the rendered orchestrator minimal.
4. `workflow_call` input on the reusable workflow itself
   (`reusable-lydite.yml:17-36`), each with its own `default:` matching step
   2's spec default, so a field that is never forwarded still resolves the
   same way lydite itself is configured to run by default.

A new pass-through field (e.g. `relay`) follows the same four hops: spec
struct field, `ciData`/`lyditeClearanceData` field wired in `pipeline.go`,
conditional `with:` in the relevant `.tmpl`, and a matching `workflow_call`
input (with matching default) in the reusable workflow file(s) it targets.

---
about: the rendered lydite-clearance.yml filename is fed, as a bare literal, into building the uses: ref for gt's own upstream reusable workflow — a second string-literal coupling of the same shape as the lydite/referral context one
saw:
  - internal/repogov/pipeline.go
  - internal/repogov/render.go
  - .github/workflows/lydite-clearance.yml
  - .github/workflows/reusable-lydite-clearance.yml
---

`buildLyditeClearanceData` (`pipeline.go:317-328`) calls `workflowRef(
"lydite-clearance.yml", major, in.RepoOwner, in.RepoName)` with the filename
as a separate hardcoded string literal — not derived from `registry()`'s
`path` field for the `"lydite-clearance"` fileSpec (`render.go:161-171`).
`workflowRef` (`render.go:331-336`) turns that into
`./.github/workflows/reusable-lydite-clearance.yml` when the target is gt's
own repo (`Upstream`, `render.go:34`), or
`pedromvgomes/gt/.github/workflows/reusable-<file>.yml@v<major>` otherwise.
gt's own repo does carry a matching `.github/workflows/reusable-lydite-
clearance.yml` that the local rendered `lydite-clearance.yml` calls into.

So the local rendered filename is not just a path on disk — its basename
(minus the `reusable-` prefix `workflowRef` always adds) has to match the
name of a real reusable workflow file gt's own repo publishes at
`.github/workflows/reusable-<name>.yml`. Nothing type-checks this: the
`"lydite-clearance.yml"` literal in `pipeline.go` and gt's own upstream
filename `reusable-lydite-clearance.yml` are two independent strings kept
equal by convention, the same pattern the existing rule
`agentic/rules/keep-the-referral-context-string-synced-with-lydite.md`
already documents for `LyditeReferralContext` vs. lydite's own
`internal/clearance.Context`.

Renaming only the registry `path` (e.g. to `gt-lydite-clearance.yml`) would
NOT by itself break this `uses:` ref, since the literal passed to
`workflowRef` is separate — but it would create exactly that drift if someone
"fixes" the literal to match the new filename without also renaming gt's own
`reusable-lydite-clearance.yml`, or vice versa.

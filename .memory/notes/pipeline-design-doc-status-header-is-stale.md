---
name: pipeline-design-doc-status-header-is-stale
kind: gotcha
description: docs/pipeline-design.md still says "design, not implemented" but describes the shipped pipeline — read it as the operating contract, not a proposal.
anchors:
  - path: docs/pipeline-design.md
    blob: fc0472260927
  - path: internal/repogov/pipeline.go
    blob: 746d072adfa4
  - path: internal/repospec/spec.go
    blob: 655e83c7de27
confidence: verified
---

`docs/pipeline-design.md:3-4` reads "Status: **design, not implemented.** Replaces the polling
gate currently in `internal/repogov`." That is false in this tree:

- `internal/repogov/pipeline.go` already implements `mergePermissions` (`:72`), the stage
  wiring maps `ciWiring`/`cdWiring` (`:132-144`), `buildStages` with `independent_stages`
  handling (`:151-217`), and the orchestrator data builders `buildCIData` (`:387`) and
  `buildCDData` (`:431`).
- `internal/repospec/spec.go` already carries `StagePermissions` and `IndependentStages` on
  both `Pipeline.CI` and `Pipeline.CD`.
- `internal/repogov/lint.go`, which the doc's "What this deletes" section says the design
  removes, does not exist anywhere in the tree.

The doc is edited in step with the implementation — its most recent touch is commit `a142ebb`,
the same commit that shipped the merge-queue/`base_freshness` support the doc describes as
landed — but the status header was never updated. Parts of "Open questions" are stale for the
same reason: question 2 ("`ci-publish.yml` is scaffolded but nothing calls it") is answered
earlier in the same file (`docs/pipeline-design.md:60-61`) and by `cdWiring`
(`pipeline.go:139-144`), which wires `publish` under CD.

Treat the file as the current design of an implemented system when reasoning about
`internal/repogov`.

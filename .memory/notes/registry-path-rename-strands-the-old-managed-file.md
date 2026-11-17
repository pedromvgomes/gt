---
name: registry-path-rename-strands-the-old-managed-file
kind: gotcha
description: Orphan detection only knows the current registry()'s paths, so renaming a managed file's path leaves the old marked file live on every governed repo, never reported.
anchors:
  - path: internal/repogov/diff.go
    blob: 97bad2e77114
  - path: internal/repogov/render.go
    blob: 81e67a2e349a
confidence: verified
---

`managedPaths` (`internal/repogov/diff.go:41-53`) builds the orphan-candidate set from the
*current binary's* `registry()` only: each `ModeManaged` entry's `path`, plus `pathIn(spec)`
when the spec relocates it. There is no list or history of paths gt rendered in the past.

`Diff` reports an orphan only for a path that is in that set, is not wanted by the current
spec, and carries gt's marker on disk (see the comment at `diff.go:130-138` and the loop after
it). A path removed from `registry()` by a rename is therefore never a candidate: it is not
rendered, not diffed, and not scanned. The old file stays on disk with its `Managed by gt`
marker and keeps running, and `gt repo check`/`sync` never mention it again.

This has already happened once: the clearance workflow now renders at
`WorkflowDir + "/gt-lydite-clearance.yml"` (`internal/repogov/render.go:178-180`), and nothing
in `internal/repogov/*.go` names the earlier `lydite-clearance.yml` as a rendered path (the
only hit, `pipeline.go:329`, is the upstream reusable workflow's filename). A repo synced
before the rename keeps the old `issue_comment` workflow until someone deletes it by hand.

Retiring a rendered filename safely needs more than editing `registry()` — e.g. keeping the
old path in the set `managedPaths` returns. No test covers a registry path rename.

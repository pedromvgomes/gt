---
about: renaming a fileSpec's rendered path in repogov's registry() leaves the old file on every already-governed repo forever, uncleaned
saw:
  - internal/repogov/render.go
  - internal/repogov/diff.go
---

`internal/repogov/diff.go`'s `managedPaths()` (`diff.go:29-41`) enumerates
orphan candidates by calling `registry()` — the *current binary's* table of
`fileSpec`s — and collecting every `ModeManaged` entry's `path`
(`internal/repogov/render.go`'s registry, e.g. `key: "lydite-clearance"`,
`path: WorkflowDir + "/lydite-clearance.yml"` at `render.go:161-171`). There is
no separate list, and no history, of paths gt has ever rendered — only what
the current `registry()` says today.

`Diff` (`diff.go:105-140`) treats a path as an orphan only if it is (a) in
`managedPaths()`, (b) not wanted by the current spec, and (c) carries
`ManagedMarker` on disk. Renaming a registry path — say
`lydite-clearance.yml` → `gt-lydite-clearance.yml` — removes the *old* path
from `registry()` entirely, so the old path is never even considered a
candidate: it is neither rendered (missing/drifted/ok) nor scanned for
orphaning. The old file sits on disk, still carrying the `Managed by gt`
marker, still wired into whatever it was wired into (e.g. an `issue_comment`
trigger or a `uses:` reusable-workflow caller), and `gt repo check`/`sync`
across the whole fleet will never again mention it — every already-governed
repo keeps running the stale file forever, invisible to drift/orphan
reporting, until someone deletes it by hand.

No test exercises a registry path rename (`grep -rn rename tests/*.go
internal/repogov/*.go` finds only an unrelated hit in `settings.go`), so this
is not a covered/considered case — a plain rename in `registry()` is not
by itself enough to retire a rendered filename safely across the fleet.

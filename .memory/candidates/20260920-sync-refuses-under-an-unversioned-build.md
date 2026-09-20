---
about: internal/repogov/service.go
saw: dispatched during the gt-runs-lydite-full-pipeline branch, verifying `gt repo sync` locally
---

`gt repo sync` refuses to write when the running binary is a dev/unversioned
build and the spec's recorded `gt_version` names a real release
(`internal/repogov/service.go:240-275`). `MajorTag` falls back to `v0` for
anything unparseable, so an unversioned build resolves to `v0` — syncing would
repoint every rendered workflow's `uses:` tag backward (e.g. `@v1` → `@v0`),
which the guard refuses outright rather than silently downgrade.

This means `gt repo sync` (even `--dry-run`) cannot be used from a `go run
./cmd/gt` dev build to preview or verify template changes against a real
checkout that already tracks a tagged version — it errors with "this
repository was rendered by gt X.Y.Z, but an unversioned build is running:
syncing would repoint its workflows from vN to v0" before doing anything. To
inspect what a template change would render, either build a properly
versioned binary (`-ldflags "-X main.version=..."`) or compare the rendered
output by hand.

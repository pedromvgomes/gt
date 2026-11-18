---
about: the release stamp has no leading v, and go.vipengele.org/update treats a version without one as a development build, so the stamp is normalised only where it is handed to the libraries
saw:
  - internal/selfupdate/selfupdate.go
  - internal/app/app.go
  - .goreleaser.yaml
  - internal/repogov/service.go
---

GoReleaser stamps `main.version` with `{{.Version}}`, which carries no leading
`v` (`3.0.0`) — see the `-X main.version=` ldflags in `.goreleaser.yaml`.
repogov reads that raw stamp (`refuseDowngrade` and the major-tag derivation in
`internal/repogov/service.go`), so the stamp itself must stay as it is.

go.vipengele.org/update's release check requires a `v`: without it a release
binary counts as a development build and never checks for or installs an
update. `selfupdate.Version` (`internal/selfupdate/selfupdate.go`) adds the `v`
only when `semver.IsValid("v"+s)`, so `dev` stays `dev`, and both
`update.Config.Version` (`Base`) and the App's build info
(`internal/app/app.go`, `BuildInfo.info`) go through it. `internal/cli.Deps`
receives the raw stamp on purpose. Normalising at the stamp instead breaks
repogov; skipping the normalisation silently disables self-update for every
release build.

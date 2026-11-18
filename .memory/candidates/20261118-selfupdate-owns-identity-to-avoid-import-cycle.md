---
about: gt's identity, paths layout and version normalisation are defined in internal/selfupdate and re-exported by internal/app, because app imports selfupdate
saw:
  - internal/selfupdate/selfupdate.go
  - internal/app/app.go
  - tests/selfupdate_test.go
  - tests/app_test.go
---

`internal/app` builds the App and wires `selfupdate.Base`/`ForRun` into the
update command and the background check, so `internal/selfupdate` cannot import
`internal/app`. `Identity()`, `PathsLayout` and `Version()` are therefore
defined in `internal/selfupdate/selfupdate.go` and re-exported as
`app.Identity`, `app.PathsLayout` and `app.Version` in `internal/app/app.go`.

The point of a single `PathsLayout` constant is that the App
(`WithPathsLayout`) and the updater (`update.Config.PathsLayout`) agree:
config at `~/.config/gt` and update state at `~/.local/state/gt` on macOS as
well as Linux. `tests/app_test.go` and `tests/selfupdate_test.go` assert both
resolve to XDG paths on darwin. Defining a second copy in `internal/app` lets
the two drift apart, and the updater's state then lands under
`~/Library` on macOS while the config does not.

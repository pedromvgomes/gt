---
about: whether a fleet-wide default for a live GitHub setting belongs in the rendered spec-default path (sync/check) or the live-API path (settings apply/diff)
saw:
  - internal/repogov/settings.go
  - internal/repogov/fleetsync.go
  - cmd/gt/repo_settings.go
  - cmd/gt/repo_fleet.go
---

`gt repo sync`/`check` only render and diff **committed files** against
`internal/repospec`; they never touch live GitHub API state. `gt repo
settings apply`/`diff` (`internal/repogov/settings.go`) is the only path
that reads or writes live repository settings — branch protection, merge
methods, rulesets, and now the `GT_LYDITE_RELAY` Actions variable
(`findLyditeRelayVar`/`setLyditeRelayVar`/`wantsLyditeRelay`,
settings.go:609-703, wired into `SettingsApply`/`SettingsDiff` at the end of
each function).

A fleet-wide default expressed as a live setting therefore does **not**
propagate through `gt repo fleet check|sync` (`internal/repogov/fleetsync.go`,
`FleetSweep`) — that sweep only calls the rendered-file `Check`/`Sync` path,
never `SettingsApply`/`SettingsDiff`. `settings apply`/`diff` run under a
human's own `gh` credentials (`cmd/gt/repo_settings.go:20`,
`cmd/gt/repo_fleet.go:20-22`), never in CI, so rollout to any given repo is
manual: someone with `gh` access has to run `gt repo settings apply` against
that repo. This is the existing rollout model for every other live-API
setting gt manages (merge settings, branch protection), not a gap specific
to the relay variable.

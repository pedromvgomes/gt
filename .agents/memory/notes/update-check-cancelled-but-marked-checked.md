---
name: update-check-cancelled-but-marked-checked
kind: gotcha
description: The background update check gets ~50ms, not its 3s timeout, and records LastCheckedAt even when the context cancelled it — suppressing retries for 24h.
anchors:
  - path: cmd/gt/main.go
    blob: 2f345ef1d9c1
  - path: internal/config/config.go
    blob: b600da4aa097
confidence: verified
---

`startBackgroundUpdateCheck` (`cmd/gt/main.go:187`, called from `PersistentPreRunE` at
`main.go:182`) launches a goroutine under
`context.WithTimeout(context.Background(), 3*time.Second)` (`main.go:209`) and stores the
cancel in `opts.updateCancel` (`main.go:212`). `postRun` (`main.go:230`) waits on the result
channel with `select` whose other arm is `<-time.After(50 * time.Millisecond)`
(`main.go:246`), and its `defer` calls `opts.updateCancel()` unconditionally once that select
returns (`main.go:235-238`) — whichever arm fired.

So the real budget for the network round-trip is the command's own `RunE` duration plus at
most 50ms, not the advertised 3s. On a fast local command (`gt config path`) that is a few
milliseconds — nowhere near a GitHub API round-trip, so the check is usually cancelled
mid-flight.

The compounding part: inside the goroutine, `state.LastCheckedAt = time.Now()` (`main.go:216`)
and `update.SaveState(statePath, state)` (`main.go:221`) run unconditionally after
`update.Check` returns, *including* when it returned a context-cancelled error — the `err`
check only happens afterwards (`main.go:222-225`). A check that never reached the network is
recorded as "checked", and `update.DueForCheck` (`main.go:206`, default interval 24h at
`internal/config/config.go:62`) then suppresses any retry for a full day. On a machine mostly
running fast commands, auto-update checks can no-op indefinitely while still looking like they
ran.

Nothing in `tests/` exercises `startBackgroundUpdateCheck`/`postRun`, so a fix is easy to get
wrong — moving the cancel without also gating `SaveState` on success leaves the second half of
the bug in place.

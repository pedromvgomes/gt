---
about: the repo fleet sweep and merge-pending warn-and-continue paths are pinned by tests under --quiet, so the note's "the fleet paths have no such test" no longer holds
saw:
  - tests/cli_fleet_test.go
  - internal/cli/repo_fleet.go
  - internal/cli/cli.go
targets: quiet-silences-warnings-so-failures-need-exit-status
verdict: now-false
---

The note says the `repo fleet` paths have no test pinning their exit status
on partial failure. They do now. `internal/cli.Deps` carries the `gh` client
and `git` runner (`internal/cli/cli.go`), so `tests/cli_fleet_test.go` runs the
fleet commands under `--quiet` against fakes:

- `TestFleetMergePendingFailsWhenARepoCannotBeScanned` (`tests/cli_fleet_test.go:104`):
  one repo's scan fails, the command exits 1, the other repo's PR is listed.
- `TestFleetMergePendingMergesTheRestWhenOneMergeFails` (`:121`): one merge fails
  under `--merge`, the other PR is still merged, exit 1.
- `TestFleetCheckFailsWhenARepoCannotBeSwept` (`:155`): one clone fails,
  `printFleetResults` reports it, exit 1.

The rest of the note holds: `inv.Warning` is silent under `--quiet`, every
warn-and-continue loop in `internal/worktree/worktree.go` and
`internal/cli/repo_fleet.go` counts failures and returns an error, and the
untrusted setup-template warning in `internal/setup/runner.go` bypasses
`inv.Warning`. The line ranges the note cites for `repo_fleet.go` have moved
with the `Deps` change; the sweep's failure report and merge-pending's
summary sit at `internal/cli/repo_fleet.go` 100-130 and 160-223.

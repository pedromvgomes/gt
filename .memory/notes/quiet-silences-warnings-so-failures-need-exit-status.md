---
name: quiet-silences-warnings-so-failures-need-exit-status
kind: invariant
description: inv.Warning prints nothing under --quiet, so every warn-and-continue loop must also count failures and return an error, or a quiet partial failure exits 0 silently.
anchors:
  - path: internal/worktree/*.go
    matches:
      - path: internal/worktree/worktree.go
        blob: ec0074392049
  - path: internal/cli/*.go
    matches:
      - path: internal/cli/cli.go
        blob: 6e3915fc2491
      - path: internal/cli/local.go
        blob: e02d78cafe1f
      - path: internal/cli/repo.go
        blob: 266c4170bb3c
      - path: internal/cli/repo_fleet.go
        blob: 7843887f89a1
      - path: internal/cli/repo_settings.go
        blob: "833863684456"
  - path: internal/setup/runner.go
    blob: e01b6cb05b42
  - path: tests/worktree_test.go
    blob: 5aa4bf77f1fa
  - path: tests/setup_test.go
    blob: 7a3ed79123c9
confidence: verified
---

`inv.Warning` (`go.vipengele.org/cli/spec`) writes nothing under `--quiet`. A loop that warns
per failed item and then returns nil therefore exits 0 with no output on a partial failure.
(The library source was not read for this note; the behaviour is taken from the in-repo
comments at `internal/worktree/worktree.go:355-356` and `internal/cli/repo_fleet.go:218-219`,
and from the test below that needs a bypass to be seen under quiet.)

Every per-item failure path today counts its failures, keeps processing the remaining items,
and returns an error at the end:

- `worktree.Nuke` — `internal/worktree/worktree.go:323-362`
- `worktree.PruneBranches` — `internal/worktree/worktree.go:406-431`
- `repo fleet` sweep — `internal/cli/repo_fleet.go:100-130`
- `repo fleet merge-pending` — `internal/cli/repo_fleet.go:169-221`

`TestWorktreeNukeReturnsErrorOnPartialFailure` (`tests/worktree_test.go:407`) and
`TestPruneBranchesReturnsErrorOnPartialFailure` (`tests/worktree_test.go:435`) pin the first
two, under a quiet invocation, including that the healthy items are still processed. The
fleet paths have no such test. A new warn-and-continue loop needs the same counter + error.

The converse case: a warning that must be seen even under `--quiet` cannot go through
`inv.Warning`. The untrusted-template notice in `setup.Execute`
(`internal/setup/runner.go:73-78`) is written straight to `inv.Env.Stderr` with the Warning
style for that reason — it is part of the gate described in
[[setup-untrusted-gate-is-plan-wide]], telling the user the commands they are about to confirm
are repository-authored. `TestExecuteUntrustedInteractiveWarnsEvenWhenQuiet`
(`tests/setup_test.go:500`) pins it; "tidying" it into `inv.Warning` hides it.

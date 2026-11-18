---
about: spec.Invocation.Warning is silenced by --quiet, so any warning that is the only report of a failure must also reach the exit status, and the untrusted setup-template warning bypasses Warning entirely
saw:
  - internal/setup/runner.go
  - internal/worktree/worktree.go
  - internal/cli/repo_fleet.go
  - tests/worktree_test.go
---

`inv.Warning` (go.vipengele.org/cli/spec) returns without writing under
`--quiet`. A command that warns per failed item and returns nil therefore
exits 0 with no output on a partial failure. `worktree.Nuke`,
`worktree.PruneBranches` (`internal/worktree/worktree.go`) and `repo fleet
merge-pending` (`internal/cli/repo_fleet.go`) count failed items and return an
error after processing all of them; `tests/worktree_test.go` pins the first
two. A new per-item failure path needs the same treatment.

The warning that setup templates come from the repository's `.gt.yaml`
(`internal/setup/runner.go`, in `Execute`) is written straight to
`inv.Env.Stderr` with the Warning style rather than through `inv.Warning`, so
it shows under `--quiet`. It is part of the untrusted-template gate: it tells
the user the commands they are confirming are repository-authored. Routing it
through `inv.Warning` would hide it.

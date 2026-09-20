---
name: wt-add-from-ignored-when-branch-exists
kind: gotcha
description: gt wt add --from is silently a no-op when a local branch <type>/<name> already exists — the worktree attaches to the stale branch instead.
anchors:
  - path: internal/worktree/worktree.go
    blob: 7d5a8163d430
confidence: verified
---

`createWorktree` (`internal/worktree/worktree.go:79-113`) checks
`localBranchExists(ctx, runner, root, branch)` **first**, where `branch := typ + "/" + name`.
If that branch exists locally it runs `git worktree add <path> <branch>` and returns
immediately (`worktree.go:80-88`) — the `opts.From != ""` check is the *next* branch, at
`worktree.go:90-92`, and is never reached.

So `gt wt add feature/foo --from some-other-branch` does not start from `some-other-branch`
when `feature/foo` already exists locally: the worktree is created from the stale branch, with
no warning and no error.

That precondition is a routine side effect of normal cleanup: `Remove`'s `DeleteBranch` is
opt-in (`worktree.go:215-219`), so `gt wt rm` without `--delete-branch` removes the worktree
directory and leaves the local branch behind. A branch left over from a previous session is
exactly what silently defeats `--from` later.

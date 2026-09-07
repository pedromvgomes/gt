---
name: bare-git-calls-rely-on-absolute-root
kind: invariant
description: Every worktree/scratch git call passes dir="" and works only because FindRepoRoot returns an absolute root, making --git-dir absolute.
anchors:
  - path: internal/worktree/*.go
    matches:
      - path: internal/worktree/worktree.go
        blob: 7d5a8163d430
  - path: internal/scratch/*.go
    matches:
      - path: internal/scratch/scratch.go
        blob: 127fb9009c30
  - path: internal/git/git.go
    blob: 8e2fc13087ca
confidence: verified
---

Every git operation on the bare repo in `internal/worktree` and `internal/scratch` uses the
shape `runner.Run(ctx, "", gitDir(root), ...)` — e.g. `worktree.go:83`, `:97`, `:147`, `:158`,
`:242`, `:268`, `:331`, `:376`, `:428`, `:447`; `scratch.go:64`, `:108`, `:114`, `:141`,
`:144` — where `gitDir(root)` is `"--git-dir=" + filepath.Join(root, ".bare")`
(`worktree.go:538-540`, `scratch.go:180-182`). The few calls that do target a checkout use
`-C <path>` instead, still with `dir == ""` (`worktree.go:229`, `:298`; `scratch.go:93`,
`:96`, `:126`).

The first argument to `Run` is the subprocess working directory, and
`git.ExecRunner.Run` only sets `cmd.Dir` when `dir != ""` (`internal/git/git.go:33-35`). So
these commands inherit the *calling process's* cwd, never `root`. It works solely because
`root` is always absolute: it comes from `git.FindRepoRoot`, which resolves
`filepath.Abs(start)` before searching upward for `.bare` (`internal/git/git.go:52-53`), and
`rootFrom` is the only producer (`worktree.go:527-536`).

If `FindRepoRoot` ever returned a relative root, or a caller built a `--git-dir` from a
relative path some other way, these calls would still work when gt is invoked from the repo
root and would silently target the wrong `.bare` — or fail with "not a git repository" — from
anywhere else. Nothing here `-C`s into `root` as a fallback. Anchored with globs because the
claim is about *every* git call in those two packages, including files that do not exist yet.

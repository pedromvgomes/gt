---
name: default-branch-worktree-safe-only-by-omission
kind: invariant
description: gt wt list/nuke never touch the default-branch checkout only because it lives outside cfg.WorktreeTypes — no code protects it by name.
anchors:
  - path: internal/worktree/worktree.go
    blob: 7d5a8163d430
  - path: internal/config/config.go
    blob: b600da4aa097
  - path: internal/clone/clone.go
    blob: 4e96e77a7507
confidence: verified
---

`collectWorktrees` (`internal/worktree/worktree.go:496-515`) is what both `List`
(`worktree.go:291`) and `Nuke` (`worktree.go:316`) iterate: it optionally adds `scratch/`,
then walks `cfg.WorktreeTypes` and lists every directory under each type. The default
`WorktreeTypes` is `{"feature", "fix", "chore"}` (`internal/config/config.go:55`). The
default-branch checkout `clone.Run` creates sits at `<folder>/<defaultBranch>`
(`internal/clone/clone.go:127-130`), outside every type directory, so it is never enumerated —
`gt wt nuke` cannot delete it and `gt wt list` never shows it.

The protection is entirely structural. Nothing in `collectWorktrees`, `Nuke`
(`worktree.go:311-345`) or `findNamedWorktree` (`worktree.go:517-526`) special-cases the
default branch name. A repo whose `.gt.yaml` set `worktree_types` to include its default
branch name — or a repo using e.g. "main" as both default branch and worktree type — would
have `gt wt nuke --force` walk into and remove it like any other worktree.

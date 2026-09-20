---
name: clone-flips-core-bare-false
kind: rationale
description: clone.Run sets core.bare=false on the .bare repo before any worktree exists, and every worktree in the layout depends on that one-time fix-up.
anchors:
  - path: internal/clone/clone.go
    blob: 4e96e77a7507
confidence: verified
---

`clone.Run` clones with `git clone --bare <url> <folder>/.bare` (`clone.go:88`), writes a
`.git` pointer file at `<folder>/.git` containing `gitdir: ./.bare` (`clone.go:93-95`), and
then runs `--git-dir=.bare config core.bare false` as the **first** of its follow-up commands
(`clone.go:98`) — before `remote.origin.fetch` is set, before the fetch, and before
`git worktree add <defaultBranch>` (`clone.go:128`).

`git clone --bare` sets `core.bare = true`. That is a repo-wide config in `.bare/config`, not
a per-worktree one, so it is shared by the default-branch checkout (`clone.go:128`) and by
every worktree `gt wt add` / `gt scratch` attaches later. Leaving it `true` while the layout
also presents `folder/` as an ordinary working tree via the `.git` pointer file produces the
"this operation must be run in a work tree" class of git failures across the whole layout,
not just at clone time.

Nothing re-asserts this later: no other code path writes `core.bare`, so a clone that skips
or reorders `clone.go:98` leaves every worktree created afterwards broken. See
[[bare-git-calls-rely-on-absolute-root]] for the other half of how the `.bare` layout is
addressed.

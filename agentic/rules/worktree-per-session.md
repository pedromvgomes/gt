# Rule: one worktree per agent session

This repository uses a **bare-repo + typed-worktree** layout managed by the
`gt` CLI: `<root>/.bare/` plus typed worktree directories (`main/`,
`feature/`, `fix/`, `chore/`, …).

## Working in it

- **Use `gt`, never raw git, for worktrees and clones.** `gt clone <url>`,
  `gt wt add <type/name>`, `gt wt rm`. Raw `git clone` or `git worktree add`
  bypasses the typed layout, the branch-naming validation and the direnv
  `GH_TOKEN` wiring the user relies on across every repository.
- **Start each session with its own worktree.** `gt wt add feature/<task>`,
  then `cd` into it. One session, one worktree, one branch — so parallel runs
  cannot step on each other and every change is attributable.
- **The root is a hub.** Do not edit or commit there, do not touch `.bare/`,
  and do not delete the default-branch worktree.
- **Leave `gt scratch` alone.** It is the user's, not yours.

## Before reporting anything as project state

`gt wt add` fetches origin and branches from `origin/<default>`, so the
worktree it creates is current. Nothing does that for the long-lived `main/`
checkout — it moves only when someone moves it, and is usually behind.

So before saying what is on `main`, what a tag points at, whether a PR landed,
or what the latest release is:

```sh
git fetch origin --tags --force   # --force: a plain fetch will not update a
                                  # tag that has MOVED, only add new ones
```

Then read `origin/main`, not `main`. `git log main`, `git rev-parse v1` and
`git describe` answer from local refs — confidently, with month-old data. Use
`git ls-remote` when the question is literally "what does the remote say now".
To bring `main/` itself forward, `git merge --ff-only origin/main`.

This is worth the keystrokes because a stale ref does not fail, it answers,
and the wrong answer looks exactly like the right one. gt force-moves its
major alias on every release, so `v1` is the ref most likely to be stale.

## See also

- The `use-gt` skill for the command reference and post-clone setup behavior.

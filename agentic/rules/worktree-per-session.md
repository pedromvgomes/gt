# Rule: one worktree per agent session

This repository uses a **bare-repo + typed-worktree** layout managed by the
`gt` CLI. Agent sessions in this environment must respect the rules below.

## The rules

1. **Always use the bare-repo layout.** Clone with `gt clone <url>`, never
   plain `git clone`. The expected layout is `<root>/.bare/` plus typed
   worktree directories (`main/`, `feature/`, `fix/`, `chore/`, …).

2. **Sessions start at the gt-managed root** — the directory that is a sibling
   to `.bare/`. Treat that root as a navigation hub only; do NOT edit files
   there and do NOT run commits from it.

3. **Each session must create its own worktree before doing any work.** Use
   `gt wt add <type/name>` to provision a fresh worktree on its own branch,
   then `cd` into it. One session, one worktree, one branch.

   ```sh
   gt wt add feature/<short-task-name>
   cd feature/<short-task-name>
   ```

4. **Whenever a worktree is needed, use `gt`.** Never call `git worktree add`
   or `git worktree remove` directly — raw git bypasses the typed layout and
   the configured worktree-types validation. Use `gt wt add` and `gt wt rm`.

5. **Do not edit inside `.bare/`** and do not delete the default-branch
   worktree (`main/` or equivalent).

6. **Do not use `gt scratch`** — the scratch worktree is reserved for the
   user's manual exploration, not for agent sessions.

7. **Never read project state out of the `main/` worktree without fetching.**
   `gt wt add` already fetches origin and branches from `origin/<default>`, so
   the worktree you create is current. Nothing does that for the long-lived
   `main/` checkout: it moves only when someone moves it, and in a clone that
   has been around a while it is usually behind.

   That matters because a stale ref does not fail — it answers. Before saying
   what is on `main`, what a tag points at, whether a PR landed, or what the
   latest release is:

   ```sh
   git fetch origin --tags --force     # --tags --force: a plain fetch will
                                       # not update a tag that has MOVED
   ```

   Then read `origin/main` rather than `main` in anything whose output you are
   about to report. `git log main`, `git rev-parse v1` and `git describe`
   answer from local refs, confidently, with month-old data. When the question
   is specifically "what does the remote say right now", `git ls-remote` asks
   it directly and cannot be stale.

   If you do need `main/` itself up to date — to run a build, or to diff
   against it — bring it forward explicitly with `git merge --ff-only
   origin/main`, and never with a checkout that discards uncommitted work.

## Why

`gt` enforces conventions (typed worktrees, branch naming, direnv-based
`GH_TOKEN` per repo) that the user relies on across many repositories.
Bypassing it produces inconsistent state that the user has to clean up by
hand. The "one worktree per session" rule keeps parallel agent runs from
stepping on each other and keeps every change cleanly attributable to a
named branch.

Rule 7 exists because a stale local ref does not fail, it answers. Reading a
moved tag from a stale local ref reports the wrong commit with no indication
anything is wrong — and a released tag like a moving `v1` is exactly the kind
that moves. Reporting "main is at X" from a week-old checkout is the same
failure with a friendlier face. Both are cheap to prevent and expensive to
notice, because the wrong answer looks exactly like the right one.

## See also

- The `use-gt` skill for the detailed command reference and post-clone setup
  behavior.

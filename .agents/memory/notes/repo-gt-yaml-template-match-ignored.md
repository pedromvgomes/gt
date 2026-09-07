---
name: repo-gt-yaml-template-match-ignored
kind: gotcha
description: A `match:` on a template in a repo's committed .gt.yaml validates fine and is printed in the plan, but is never applied — those templates always run.
anchors:
  - path: internal/setup/repo.go
    blob: ca9cd5e6ab28
  - path: internal/setup/setup.go
    blob: 2bf41de680c6
  - path: internal/setup/runner.go
    blob: 22d7ba51cbd9
  - path: internal/config/config.go
    blob: b600da4aa097
confidence: verified
---

`config.ValidateSetup` (`internal/config/config.go:314-339`) validates `tpl.Match` identically
for every `Template`, whether it came from the user's global config or a repo's committed
`.gt.yaml` — `LoadRepoTemplates` calls it at `internal/setup/repo.go:38`. Nothing in
validation hints that origin changes the meaning.

It does. `LoadRepoTemplates` says so at `internal/setup/repo.go:18-21`: templates from a
repo's own `.gt.yaml` "are scoped to that repo, so they run unconditionally — their match
globs are ignored by the callers." Both callers confirm it: `internal/clone/clone.go:179-184`
and `internal/worktree/worktree.go:121-127` take the returned slice and merge/run it directly,
with no filtering step. Match filtering lives in `setup.Select`
(`internal/setup/setup.go:266`) and its `matchTemplate` (`setup.go:294`), and is only ever
applied to `config.Setup.Templates` from the global config (`clone.go:167-170`).

The only surviving use of `t.Match` for a repo-declared template is cosmetic: `printTerse`
(`internal/setup/runner.go:97-115`) prints `(match: ...)` in the plan, which actively misleads
a repo author who wrote `match: ["some-glob"]` expecting it to scope when the template runs.
It does not — the template runs whenever `LoadRepoTemplates` finds the file.

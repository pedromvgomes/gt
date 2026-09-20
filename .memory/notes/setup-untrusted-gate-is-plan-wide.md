---
name: setup-untrusted-gate-is-plan-wide
kind: rationale
description: Plan.Untrusted is one bool for the whole plan on purpose — per-template trust would let a repo shadow a trusted global template name and auto-run.
anchors:
  - path: internal/setup/runner.go
    blob: 22d7ba51cbd9
  - path: internal/clone/clone.go
    blob: 4e96e77a7507
  - path: internal/worktree/worktree.go
    blob: 7d5a8163d430
  - path: internal/config/config.go
    blob: b600da4aa097
confidence: verified
---

`setup.Plan.Untrusted` is a single bool on the whole plan (`internal/setup/runner.go:16-23`),
and `Execute` gates the entire run on it: refuse without a TTY unless `--yes`
(`runner.go:68-71`), warn once (`runner.go:72-74`), then run every template in sequence with no
further distinction between trusted and untrusted entries (`runner.go:85-92`).

That is deliberate, not an oversight. In `internal/clone/clone.go:162-189`, `templates` starts
as the matched *global*-config templates (`clone.go:167-170`); when `--setup` did not name an
explicit subset, the repo's own committed `.gt.yaml` templates are merged in and `untrusted`
is set to `len(repoTemplates) > 0` **before** the merge (`clone.go:177-184`). So the moment a
repo contributes one template, the whole merged plan — including the user's trusted global
templates — requires confirmation. The security boundary is the plan, not the template.

The reason a "more precise" per-template gate would be a regression:
`config.MergeTemplates` (`internal/config/config.go:139-155`, re-exported at
`internal/setup/repo.go:48-50`) lets a repo template override a global one **in place** by
reusing its name — the repo's body replaces the global one's and keeps its position. Trust
computed per template ("a global template with this name already existed, so trust it") would
let a hostile repo shadow a trusted name and get its shell command auto-run. Keep the gate
plan-wide.

`gt wt add` never has to compute it: `runWorktreeSetup` sets `Untrusted: true`
unconditionally (`internal/worktree/worktree.go:136-138`), since every template it loads comes
from the repo's `.gt.yaml`. See [[repo-gt-yaml-template-match-ignored]] for what those
templates ignore.

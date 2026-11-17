---
about: gt holds every governed repository to its base through strict required checks only; the merge queue and the base_freshness choice are retired, and a BEHIND Dependabot PR is asked to rebase by comment
saw:
  - internal/repogov/settings.go
  - internal/repospec/spec.go
  - internal/repogov/fleet.go
  - .github/workflows/reusable-dependabot-auto-merge.yml
  - docs/adr/0002-always-strict-freshness-instead-of-a-merge-queue.md
---

- `desiredRuleset` (`settings.go`) always sets strict on the `required_status_checks` rule when `pipeline.ci` is enabled. There is no queue, no `allow_auto_merge` handling and no resolver. `gtRuleTypes` contains `merge_queue`, so `settings apply` strips a queue rule from gt's ruleset and `rulesetChanges` reports it as `present -> absent`.
- `base_freshness` and `require_up_to_date` stay as tagged, `json:"-"` fields in `spec.go` so old manifests parse under `KnownFields(true)`; `dropRetiredKeys()` clears them before validation, so no value (including `none`) can turn strict off. `check` reports a manifest still carrying them as spec drift and `sync` drops them.
- Without a queue, the linear-history requirement still applies: `validateSettings` rejects `merge_commit` as the only enabled merge method.
- Strict leaves every other open Dependabot PR BEHIND after each merge. `eligibility` in `fleet.go` reports "behind the base branch" and sets `PendingPR.Behind`; the auto-merge workflow and `merge-pending --merge` comment `@dependabot rebase` once per PR per run (`RebaseComment`, `RequestRebase`). The job still merges at most what is CLEAN/HAS_HOOKS/UNSTABLE, so a backlog drains one PR per nightly run.
- The `merge_group:` trigger and the attest `merge_group` walk-back remain in the templates; they are inert without a queue. The lydite job keeps `if: "!cancelled()"`.
- The rejected alternative (a queue by default) is recorded in `docs/adr/0002-always-strict-freshness-instead-of-a-merge-queue.md`.

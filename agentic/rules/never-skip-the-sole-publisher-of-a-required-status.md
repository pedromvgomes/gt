---
description: A job that is the only publisher of a required per-SHA status check must never carry a skip guard, even where every other stage has one.
---

# Rule: never skip the sole publisher of a required status

`ci-preflight`'s skip guard exists so a stage whose work is provably redundant
— nothing changed that it would test — does not re-run. That is safe only
because `ci-gate` aggregates the *stage's own job result*, and a skipped job
counts as a pass. A job publishing an out-of-band required check (a commit
status like `lydite/referral`, not a job result branch protection reads via
`needs:`) breaks that equivalence: skipping it does not make the status pass,
it leaves the status **unpublished** for whatever SHA is being evaluated.

Whether that is safe depends on where `reusable-attest.yml` found the
attestation, and that differs by event:

- **`pull_request`** — attest reads only the head SHA's own statuses, so
  `validated: true` means an earlier run on that same head SHA already
  published every status the skipped job would. A skip here is redundant, not
  harmful.
- **`merge_group`** — attest walks the `gh-readonly-queue` ref back to the
  queued pull requests' heads, so `validated: true` is reported on the queue's
  own commit, one no earlier run ever published a status under. gt applies no
  merge queue, but `ci-orchestration.yml` keeps the `merge_group:` trigger so a
  queue somebody adds to a branch still gets its required checks. On that
  path a skip guard keyed on `validated` stalls the entry forever on a check
  nothing will report.

The guard is unconditional rather than keyed to the event because attest's
lookup is what makes the first case safe, and a guard that silently depends on
which commit attest read its statuses from breaks the moment that lookup
widens.

## Applies to

Any job in `ci-orchestration.yml` (or its template,
`internal/repogov/templates/workflows/ci-orchestration.yml.tmpl`) that
publishes a commit status branch protection requires directly, rather than
being aggregated into `ci-gate` via `needs:`. Today that is the `lydite` job
alone, publishing `lydite/referral`.

## Example

```yaml
# ✗ a skip guard here leaves lydite/referral unpublished on a merge_group
#   commit that never had its own run
if: "!cancelled() && needs.attest.outputs.validated != 'true'"

# ✓ always runs; a real verdict on a failing tree is more useful than none
if: "!cancelled()"
```

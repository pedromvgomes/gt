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

`reusable-attest.yml` walks a `merge_group` ref back to its queued PR's head,
so an already-validated PR tree can report `validated: true` on the merge
queue's own commit too — a commit no earlier run ever published a status
under. A skip guard keyed on `validated` then stalls that queue entry forever
on a required check nothing will ever report.

## Applies to

Any job in `ci-orchestration.yml` (or its template,
`internal/repogov/templates/workflows/ci-orchestration.yml.tmpl`) that
publishes a commit status branch protection requires directly, rather than
being aggregated into `ci-gate` via `needs:`. Today that is the `lydite` job
alone, publishing `lydite/referral`.

## Example

```yaml
# ✗ a skip guard here leaves lydite/referral unpublished for a merge-queue
#   SHA that never had its own run
if: "!cancelled() && needs.attest.outputs.validated != 'true'"

# ✓ always runs; a real verdict on a failing tree is more useful than none
if: "!cancelled()"
```

# Base freshness and the merge queue

An attestation reports what *was* tested. It cannot answer a question about a
tree that does not exist yet — two pull requests touching disjoint files, each
green, each conflict-free to git, and semantically incompatible, will both
merge and break the default branch with nothing out of compliance anywhere.
gt's own `main` broke this way, which is what prompted v1.7.0.

So every governed repository is held to its base. The spec declares the
**guarantee**; gt picks a mechanism the repository can actually have.

```yaml
settings:
  branch_protection:
    base_freshness: auto   # auto | queue | strict | none
```

- **`queue`** — a merge queue builds each entry against the base tip plus the
  entries ahead of it, and rejects the second of that pair. GitHub offers these
  only on **organization-owned public** repositories.
- **`strict`** — "require branches to be up to date". Same guarantee, worse
  ergonomics, works everywhere.
- **`auto`** — reads ownership and visibility and chooses.
- **`none`** — gives up the guarantee.

Exactly one is ever applied. They are branches of one decision, not two
switches, so paying the rebase churn *and* the queue latency for a single
guarantee is structurally impossible. `base_freshness` replaced the released
`require_up_to_date`; a spec still carrying that key parses to `auto`.

## resolveMergeQueue, and why the order matters

`repogov/settings.go` resolves the mechanism in this order, and the order is
the design:

1. **The spec first** — an explicit choice is not gt's to second-guess.
2. **Is there a pipeline?** Neither mechanism means anything without a required
   check; with `pipeline.ci` disabled both are omitted.
3. **The platform** — `queueImpossible` establishes what the repository can
   have from its own facts, not from a feature probe, because there is no probe
   that answers this.
4. **The `merge_group` trigger, last**, because its answer changes as a rollout
   proceeds. A queued pull request reports its required check from the
   `merge_group` event and no other, so `settings apply` reads
   `ci-orchestration.yml` **on the default branch** and withholds the queue rule
   until the trigger is actually there. Without that ordering every pull request
   enters a queue that can never build it, and the `governance` stage that would
   have warned you sits inside the check that no longer reports.

A read that fails yields `mergeQueueDeferred`, not a fallback: unknown is not
the same as unavailable, and falling back to strict on a blip would turn every
open pull request red. Strict is deliberately **not** applied in the meantime —
the window is one sync and one merge long.

Where the queue is the mechanism, `settings apply` also enables
`allow_auto_merge`: a queue is entered through it, and without it nobody can
put anything into one.

## What a queue changes elsewhere

- **`gh pr merge --delete-branch` becomes illegal.** gh refuses the combination
  before merging, so anything passing it unconditionally fails every merge in a
  queued repository. `settings.merge.delete_branch_on_merge` (default true)
  covers the deletion instead. Both the auto-merge workflow and
  `repogov.MergePending` probe `Repository.mergeQueue(branch:)` over GraphQL and
  drop the flag — see [`dependabot.md`](dependabot.md).
- **The spec cannot answer "is there a queue?".** Under `auto` the mechanism is
  chosen at `settings apply` time from live repository facts and withheld on the
  trigger check, so the repository is the only place the answer exists. Anything
  needing it must ask GitHub.
- **`QueueMergeMethod()` is derived, not configured.** The queue merges on the
  repository's behalf, so offering it a method `allowed_merge_methods` forbids
  would let a queue land what a human could not.

The queue stays cheap because a merge group whose tree matches an
already-validated one skips every stage.

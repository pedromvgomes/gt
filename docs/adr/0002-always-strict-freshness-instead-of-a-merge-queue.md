# Pull requests are held to their base by strict required checks, not a merge queue

Every governed repository with a required check gets GitHub's "require branches
to be up to date before merging" — `strict_required_status_checks_policy: true`
on the `required_status_checks` rule of gt's ruleset — wherever `pipeline.ci`
is enabled. There is no opt-out and no spec key that chooses a mechanism:
`settings.branch_protection.base_freshness` and `require_up_to_date` still
parse, so no manifest breaks, but nothing reads them, `gt repo check` reports a
manifest carrying either as spec drift, and `gt repo sync` drops them. `gt repo
settings apply` removes a `merge_queue` rule from the ruleset gt owns, and does
not manage `allow_auto_merge`.

The guarantee itself is not in question. Two pull requests that touch disjoint
files, each green against its own base and conflict-free to git, can still
break the default branch together; the validated-tree attestation cannot see
it, because it reports on trees that were tested, not on the one that results
from landing both. Something has to make the second pull request build against
what the first one produced.

## The rejected alternative: a merge queue by default

A merge queue gives the same guarantee with better ergonomics: it builds each
entry against the base tip plus the entries ahead of it, and nobody presses
"Update branch". The rejected design made it the default — a `base_freshness`
setting of `auto | queue | strict | none`, with `auto` choosing a queue where
the repository could hold one and strict where it could not, never both.

It was rejected because the cost was large and the gain was ergonomic:

- **Most of the fleet could never have one.** GitHub accepts the
  `merge_queue` rule only on organization-owned public repositories; it refuses
  it on user-owned repositories of any visibility and on organization-owned
  private ones short of Enterprise Cloud. The default therefore had to resolve
  per repository from live ownership and visibility, and most repositories
  landed on strict anyway.
- **No feature probe answers the question.** GraphQL's
  `repository.mergeQueue(branch:)` returns `null` both for "no queue configured"
  and "no queue possible", so availability had to be inferred from measured
  platform behaviour that GitHub can change without notice.
- **A queue applied too early hangs every pull request.** A queued pull request
  reports its required check only from the `merge_group` event, so the rule
  could not be applied until `ci-orchestration.yml` on the default branch
  carried that trigger. That needed a deferral state that carried a live queue
  and the strict flag through untouched while the decision was open, plus
  reporting so a repository stuck mid-rollout did not read as compliant.
- **A queue changes how everything merges.** `gh pr merge --delete-branch` is
  refused outright on a queued branch, so every merging path — the Dependabot
  auto-merge workflow and `gt repo fleet merge-pending` — needed a GraphQL probe
  per base branch to decide whether to pass the flag. Entering a queue goes
  through auto-merge, so `allow_auto_merge` had to be switched on where the
  queue applied. The queue's merge method had to be derived from the allowed
  methods and validated against `required_linear_history`.
- **The choice was a way to give the guarantee up.** `none` existed, and a
  setting that turns a guarantee off is the first thing reached for when a pull
  request is red.

Strict holds the identical guarantee on every repository gt governs, with one
rule parameter and no platform probing. Its cost is real and accepted: a
behind pull request must be updated before it merges, which re-runs CI, and a
Dependabot pull request that falls behind is skipped by the nightly auto-merge
until it is rebased.

The pieces that serve a queue someone adds by hand stay in place, because
they cost nothing without one: the orchestrator's `merge_group` trigger and
`reusable-attest.yml`'s recovery of the pull requests a `gh-readonly-queue` ref
names.

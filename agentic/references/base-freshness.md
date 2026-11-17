# Base freshness, and why there is no merge queue

An attestation reports what *was* tested. It cannot answer a question about a
tree that does not exist yet — two pull requests touching disjoint files, each
green, each conflict-free to git, and semantically incompatible, will both
merge and break the default branch with nothing out of compliance anywhere.
gt's own `main` broke this way.

So every governed repository with a required check is held to its base, by
GitHub's "require branches to be up to date before merging":
`strict_required_status_checks_policy: true` on the `required_status_checks`
rule `desiredRuleset` renders in `repogov/settings.go`. Once one of the pair
lands, the other is `BEHIND` and cannot merge until it is updated, and the
update runs `ci-gate` against the tree that will actually land.

## Where it applies

- **Wherever `pipeline.ci` is enabled** — which is exactly where the ruleset
  carries a `required_status_checks` rule. With CI disabled that rule is not
  rendered, because requiring a context nothing reports blocks every pull
  request forever, and strict has nothing to sit on.
- **With no opt-out.** There is no spec key for it. A required check that a
  stale tree can satisfy is the hole this closes, and a switch to reopen it is
  a switch somebody flips to make a red PR mergeable.

`settings diff` reports `ruleset.strict_required_status_checks_policy` as drift
whenever the live flag differs from `pipeline.ci.enabled`.

## Retired spec keys

`settings.branch_protection.base_freshness` and `require_up_to_date` are
retired. Both stay tagged fields on `repospec.BranchProtection` (`json:"-"`,
`omitempty`) because `Parse` decodes with `KnownFields(true)`, and deleting them
would make every manifest still carrying one a hard `check`/`sync` failure
across the fleet. `dropRetiredKeys()` clears them before validation, so:

- any value parses, and nothing downstream reads it — `base_freshness: none` is
  not an opt-out;
- the resolved spec omits them, so `gt repo config` never shows them;
- the manifest does not round-trip, so `gt repo check` reports `SpecStale`
  and `gt repo sync` rewrites `.gt-repo.yaml` without them.

## Converging a repository that holds a merge queue

`merge_queue` is in `gtRuleTypes` — gt owns it — and `desiredRuleset` never
renders it. So `settings apply`:

- removes it from gt's own ruleset, since owned rules are not carried through
  the way unmodelled ones are;
- does not fold it in from another ruleset gt absorbs, for the same reason;
- reports it first: `settings diff` names `ruleset.rules.merge_queue:
  present -> absent`. This is the one owned rule with an explicit converse
  check — the general rule that diff never reports a live rule as surplus
  exists for *unmanaged* rules apply carries through.

gt does not manage `allow_auto_merge`: nothing gt runs merges through it, and a
repository may be using it for something of its own. It is left as found.

## What stays, and why

- **The `merge_group:` trigger** in `ci-orchestration.yml.tmpl`, rendered
  unconditionally. A queue somebody adds to a branch reports its required check
  from that event and no other; without the trigger every entry would wait on a
  check nothing runs. With no queue it never fires.
- **`reusable-attest.yml`'s merge_group walk-back**, which recovers the pull
  requests a `gh-readonly-queue` ref names so an already-validated tree still
  skips. Dormant without a queue.
- **The `lydite` job's `if: "!cancelled()"`**, with no skip guard — see
  `agentic/rules/never-skip-the-sole-publisher-of-a-required-status.md`. It is
  the sole publisher of the required `lydite/referral` status, and that holds
  with or without a queue.

## Why not a queue

Recorded in [ADR 0002](../../docs/adr/0002-always-strict-freshness-instead-of-a-merge-queue.md).
The short form: GitHub offers queues only on organization-owned public
repositories, so most of the fleet takes strict regardless, and a queue changes
how everything merges — `gh pr merge --delete-branch` is refused, entering one
needs `allow_auto_merge`, and applying the rule before the `merge_group`
trigger lands hangs every pull request — for an ergonomic gain rather than a
stronger guarantee.

## What strict costs

A behind pull request needs updating before it merges, and the update re-runs
CI. Dependabot's are the visible case: the auto-merge workflow and
`MergePending` merge only `CLEAN`/`HAS_HOOKS`/`UNSTABLE`, so a `BEHIND` one is
skipped — not failed — and the same path comments `@dependabot rebase` on it
(the workflow on every run, `merge-pending` only with `--merge`). The rebase
re-runs CI on the current base, and a later run merges it once it is clean.
See [`dependabot.md`](dependabot.md).

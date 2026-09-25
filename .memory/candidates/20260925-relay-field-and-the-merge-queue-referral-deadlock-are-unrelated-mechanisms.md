---
about: gt's `Lydite.Relay` spec field (issue #77/#80/#81) is the comment/review relay identity, not any mechanism related to lydite/referral surviving a merge queue — gt currently has no code that consumes or forwards lydite's own out-of-band queue/status-relay fix at all
saw:
  - internal/repospec/spec.go
  - .github/workflows/reusable-lydite.yml
  - .github/workflows/reusable-lydite-clearance.yml
  - internal/repogov/templates/workflows/ci-orchestration.yml.tmpl
  - handoff/done/20260923-0503-lydite-relay-passthrough.md
---

Grepped this tree for any trace of lydite's `v0.3.0`/"queue job"/"relay
worker"/"fingerprint" mechanism (the thing that recomputes a clearance
fingerprint and posts `lydite/referral` for a `merge_group` SHA out of band):
zero hits outside a version-bump note in `docs/releases/v0.2.0.md` that is
about gt's own version, not lydite's. Nothing in `internal/repospec/spec.go`,
`internal/repogov/`, or the two hand-authored `reusable-lydite*.yml` files
references it.

`Lydite.Relay` (`spec.go:142-145`) is a different, already-shipped feature:
the *comment/review* relay identity forwarded to `lydite/actions`'s
`lydite.yml@v1`/`lydite-clearance.yml@v1` `relay` input, so PR comments and
`/lydite clear` replies post as the lydite App instead of
`github-actions[bot]`. It has no effect on how or whether `lydite/referral`
itself gets published for a `merge_group` SHA — confirmed live via `gh api`
against `lydite/actions`'s `workflow_call.inputs` during the work that added
it (`handoff/done/20260923-0503-lydite-relay-passthrough.md`), and the field's
own doc comment only mentions comments/replies, not the referral status.

What currently keeps a governed repo's merge-queue entry from stalling on
`lydite/referral` is unrelated to `relay`: `ci-orchestration.yml.tmpl` fires
on `merge_group` unconditionally (`ci-orchestration.yml.tmpl:40`), and the
`lydite:` job's own `if:` is `"!cancelled()"` with no skip guard
(confirmed at the job definition, not just the doc comment quoted in
`.memory/candidates/20260920-skipped-stage-note-is-stale-after-the-lydite-rename.md`),
so lydite fully re-runs and re-publishes the status for the queue SHA rather
than anything relaying a prior verdict. Whether that full re-run is what
lydite's own `v0.3.0` queue-job/relay-worker mechanism was meant to replace
or shortcut is not established here — there is nothing in this repo to check
it against.

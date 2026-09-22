---
about: .github/workflows/reusable-attest.yml
saw: dispatched during the gt-runs-lydite-full-pipeline branch, review pass 2 (panel-code-review, security reviewer)
---

`reusable-attest.yml`'s "has this exact tree already passed the gate?" check
does not only match a `push` event to the default branch against its own
prior attestation. For a `merge_group` event it walks the ref back to the
head of the PR(s) queued into it and checks *that* commit's `gt/validated-tree`
status — so a merge-queue entry whose resulting tree matches an already-passed
PR head reports `validated: true` on the **merge-queue commit's own SHA**,
even though nothing has ever posted a status specifically for that SHA.

This matters for any job whose `if:` is guarded by
`needs.attest.outputs.validated != 'true'` (the `SkipGuard` pattern in
`internal/repogov/pipeline.go`) if that job is also the sole publisher of a
required branch-protection status the merge queue's own commit must satisfy:
skipping it on `validated: true` never publishes that status for the
merge-queue SHA, and the queue entry stalls forever on a check nothing can
report (see the rule
`agentic/rules/never-skip-the-sole-publisher-of-a-required-status.md`, added
by the same branch that found this). It is safe for jobs whose result is
aggregated into `ci-gate` via `needs:` (a job result, not a commit status),
since `ci-gate` re-evaluates and re-posts its own status on every run
regardless of what was skipped.

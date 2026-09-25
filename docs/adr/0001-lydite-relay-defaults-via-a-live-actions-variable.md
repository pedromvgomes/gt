# lydite's relay defaults via a live Actions variable, not a rendered spec default

Every `lydite.enabled` governed repository gets `lydite`'s relay identity —
`https://pr.lydite.org` — regardless of whether its own `.gt-repo.yaml`
declares one. gt delivers this by having `gt repo settings apply` set a
repository-level GitHub Actions variable, `GT_LYDITE_RELAY`, via the API
(`gt repo settings diff` reports it read-only), rather than by rendering the
value into the repo's committed `ci-orchestration.yml`. This follows the
command split the repo already documents: live GitHub API state goes through
`settings diff|apply`, not `sync`/`check`, which only render and diff
committed files. gt's own fleet-shared `reusable-lydite.yml` and
`reusable-lydite-clearance.yml` fall back to `vars.GT_LYDITE_RELAY` when a
caller passes no explicit `relay` input; an explicit `lydite.relay` in a
repo's own spec still overrides it.

A rendered default was rejected because it would touch every governed repo's
committed workflow file on the next sync, correctly triggering the
"ask first: changing a repospec default" boundary for a fleet-wide render
diff. A live variable carries no such diff — it is out-of-band config in the
same style as the merge-settings PATCH gt already performs, not a change to
any committed file.

The default is fleet-wide (any `lydite.enabled` repo, independent of whether
it also uses a merge queue) and carries **no opt-out**: there is no field or
sentinel that distinguishes "repo owner declared no relay on purpose" from
"repo owner said nothing," so every `lydite.enabled` repo's PR comments and
clearance replies post as the lydite App as soon as someone runs `settings
apply` against it, with no way back short of new code adding one.

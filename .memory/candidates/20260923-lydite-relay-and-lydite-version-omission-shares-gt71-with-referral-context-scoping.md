---
about: the "relay/lydite-version not exposed as inputs" comment in reusable-lydite-clearance.yml and the "LyditeReferralContext is required unscoped" comment in spec.go cite the same upstream App-auth gap, but only one of them actually depends on it
saw:
  - .github/workflows/reusable-lydite-clearance.yml
  - .github/workflows/reusable-lydite.yml
  - internal/repospec/spec.go
targets: 20260923-lydite-relay-and-lydite-version-omission-shares-gt71-with-referral-context-scoping
verdict: now-false
---

`internal/repospec/spec.go:444-450` (`LyditeReferralContext` doc comment) is
still accurate: the `lydite/referral` branch-protection status is required
unscoped by `integration_id` because lydite posts that status with the plain
per-run `GITHUB_TOKEN`, not an App identity, so there is no App to scope the
check to.

The `reusable-lydite-clearance.yml` comment this candidate originally paired
with it was wrong, and the pairing does not hold. Verified live via `gh api`
against `lydite/actions`'s current `workflow_call.inputs`: both
`lydite/actions/.github/workflows/lydite.yml@v1` and
`lydite-clearance.yml@v1` already accept a `relay` input
(`type: string, default: ""`) that routes PR comments and clearance replies
through an App identity — this is the comment/review relay, deployed and
stable, and it is unrelated to the `/status` relay route
`LyditeReferralContext`'s comment describes. `internal/repospec.Lydite.Relay`
now threads this input through gt's own spec → `ciData`/`lyditeClearanceData`
→ `ci-orchestration.yml.tmpl`/`gt-lydite-clearance.yml.tmpl` →
`reusable-lydite.yml`/`reusable-lydite-clearance.yml`, with no dependency on
an App identity for the referral status.

`lydite-version` remains genuinely unexposed, for an unrelated reason: gt
pins `lydite/actions/...@v1` itself, so there is nothing for a per-repo input
to select.

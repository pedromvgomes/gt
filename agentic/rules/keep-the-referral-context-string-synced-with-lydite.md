---
description: repospec.LyditeReferralContext must be renamed together with lydite/lydite's internal/clearance.Context — nothing here can import that module.
---

# Rule: keep the referral context string synced with lydite

`repospec.LyditeReferralContext` (`"lydite/referral"`) is the commit-status
context gt requires in branch protection wherever lydite is enabled. It has
to be the exact string lydite's own referral step publishes under —
`internal/clearance.Context` in the separate `lydite/lydite` module — because
branch protection matches the context literally and a required check nothing
can ever satisfy blocks every PR forever.

Go's compiler cannot catch a mismatch here: `lydite/lydite` is a different
module gt cannot import, so the two constants are two string literals kept
equal by convention, not by the type system.

## Applies to

`internal/repospec/spec.go`'s `LyditeReferralContext`, and any change to what
context lydite's referral step publishes under.

## Example

A rename on either side without the matching change on the other silently
turns every governed repository's `lydite/referral` requirement into a check
that can never be satisfied — indistinguishable, from branch protection's
side, from lydite simply never running.

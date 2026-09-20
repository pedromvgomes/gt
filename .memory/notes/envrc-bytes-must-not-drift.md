---
name: envrc-bytes-must-not-drift
kind: invariant
description: EnvrcContent must stay byte-identical for an empty profile, because writeEnvrc decides whether to rewrite with plain string equality.
anchors:
  - path: internal/setauth/setauth.go
    blob: 1ef23500d776
  - path: internal/setauth/profile.go
    blob: c153c971a070
  - path: tests/setauth_test.go
    blob: 4aa4eaa5197a
confidence: verified
---

`EnvrcContent` (`internal/setauth/setauth.go:143-165`) renders the `GH_TOKEN` line
(`setauth.go:149-153`) and, only when `!profile.Empty()`, appends a profile comment plus one
`export` per env var (`setauth.go:157-163`). The comment on `Profile.Empty`
(`internal/setauth/profile.go:30-31`) states the contract: the empty profile "is the case that
must keep producing byte-identical output to pre-profile gt."

This is load-bearing, not cosmetic, because `writeEnvrc` (`setauth.go:253-281`) decides whether
to touch the file with `current == expected` (`setauth.go:264`) — a raw string compare, not a
semantic diff. Any change to `EnvrcContent` (a trailing newline, different quoting, reordered
`Fprintf` calls) makes every already-managed `.envrc` in the world register as "differs" on the
next `gt set-auth`: it is silently rewritten under `--yes`, and under `!yes` non-interactively
it errors with `.envrc differs; not overwriting` (`setauth.go:267-275`).

Three tests pin the exact rendered string — `TestEnvrcContentWithoutProfileIsUnchanged`
(`tests/setauth_test.go:26`), `TestEnvrcContentWithNamedButEmptyProfileIsUnchanged`
(`tests/setauth_test.go:38`), and `TestRunLeavesNonOptedInEnvrcByteIdentical`
(`tests/setauth_test.go:399`) — so a format change must update all three fixtures, and CI
catches it even though a hand-run smoke test would not.

The same rendered line is also parsed back by a hand-written regex; see
[[userfromenvrc-regex-couples-to-envrccontent]].

---
name: userfromenvrc-regex-couples-to-envrccontent
kind: gotcha
description: envrcUserPattern is an independent literal copy of the GH_TOKEN line EnvrcContent renders; drift makes set-auth start prompting for an identity it already knows.
anchors:
  - path: internal/setauth/setauth.go
    blob: 1ef23500d776
  - path: tests/setauth_test.go
    blob: 4aa4eaa5197a
  - path: agentic/skills/use-gt/SKILL.md
    blob: f05ace326510
confidence: verified
---

`EnvrcContent` renders the token line from a `fmt.Fprintf` template
(`internal/setauth/setauth.go:149-153`). `userFromEnvrc` (`setauth.go:288-297`) recovers the gh
user from an existing `.envrc` by matching `envrcUserPattern` (`setauth.go:286`), a hand-written
regex over that same line. The two are independent string literals with no shared source of
truth; nothing forces them to agree, and no test changes one and asserts the other still
matches.

If the template changes (spacing, quoting, an added flag to `gh auth token`) without a matching
regex update, `userFromEnvrc` returns `""` for every existing repo's `.envrc`, silently.
`determineUser` (`setauth.go:209-238`) then falls past its "reuse the existing envrc's user"
fast path (`setauth.go:219-221`) into `gh api user` plus `printer.Prompt`
(`setauth.go:222-227`) — which breaks the promise at
`agentic/skills/use-gt/SKILL.md:186-188` that on an existing clone "the gh user is recovered
from the existing `.envrc`, so it neither prompts nor changes" the identity, and hard-fails in
a no-TTY run where there is no prompt to fall back to.

The same literal is separately load-bearing for rewrite decisions; see
[[envrc-bytes-must-not-drift]].

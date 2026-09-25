---
about: how to check whether a single named live GitHub resource (a ruleset, an Actions variable) exists, given the GH interface's error handling
saw:
  - internal/repogov/settings.go
---

`ExecGH.RunWithInput` (settings.go, the real `GH` implementation) collapses
every non-zero `gh` exit into one opaque error string — there is no
HTTP-status discrimination anywhere in this file. A `GET
.../actions/variables/<name>` (or any by-name fetch) for a resource that
does not exist and the same call against a broken token or insufficient
scope surface identically, so treating a non-nil error as "absent" would
silently misreport a credential failure as a repository that just needs
writing to.

Both existence-check helpers in this file route around this by listing the
collection and filtering client-side instead of fetching by name:
`findRuleset` (settings.go:567-610) lists `.../rulesets` and matches by
name; `findLyditeRelayVar` (settings.go:609-660ish) lists
`.../actions/variables?per_page=100` and matches by name. The `per_page=100`
on the latter is deliberate — the default page is 30, and a repository with
more variables than that would report gt's own as absent and attempt a
create (which 409s) instead of an update.

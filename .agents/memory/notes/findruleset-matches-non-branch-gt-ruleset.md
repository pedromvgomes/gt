---
name: findruleset-matches-non-branch-gt-ruleset
kind: gotcha
description: findRuleset claims any ruleset named "gt" as gt's own regardless of target, so SettingsApply would PUT a branch payload over a non-branch ruleset.
anchors:
  - path: internal/repogov/settings.go
    blob: 5eadcc1f6841
confidence: verified
---

`findRuleset` filters the summaries it looks at with, at `internal/repogov/settings.go:584`:

```go
if sum.Name != RulesetName && sum.Target != "branch" {
    continue
}
```

By De Morgan that **keeps** an entry when `sum.Name == RulesetName` (`RulesetName = "gt"`,
`settings.go:99`) **or** `sum.Target == "branch"` — not only when the target is a branch. A
ruleset named "gt" that targets something else (a tag ruleset, say) is therefore fetched, and
because the name matches it is assigned to `mine` at `settings.go:595` with no check of
`sum.Target`.

`mine` flows into `SettingsApply` (`settings.go:993`), which switches the request to
`PUT repos/<owner>/<name>/rulesets/<mine.ID>` when `mine != nil` (`settings.go:1056-1059`),
carrying
`desiredRuleset`'s branch-targeted payload (`settings.go:484-495`) — silently repurposing an
unrelated "gt" ruleset into gt's branch ruleset and dropping whatever it enforced.

Under normal operation this cannot fire: gt only ever creates a ruleset named "gt" with
`target: branch` (`settings.go:484-486`). It bites only if some other ruleset independently
ends up named exactly "gt" with a different target — a hand-created tag-protection ruleset, or
a future gt feature managing tag rulesets under the same name. `SettingsDiff` would surface it
as a `ruleset.target` mismatch (`settings.go:724`) before anyone applies; `SettingsApply` has
no equivalent guard. The filter that matches the surrounding intent is probably `sum.Target !=
"branch"` alone (target gates inclusion, name only sorts mine from others) — but that is
inferred from the code, not from any doc or issue.

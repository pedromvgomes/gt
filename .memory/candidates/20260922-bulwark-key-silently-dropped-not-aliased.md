---
about: the bulwark-to-lydite spec rename has no legacy-key alias; an old bulwark opt-out is silently lost, and repospec relies on non-strict YAML decoding to make that work at all
saw:
  - internal/repospec/spec.go
  - tests/repospec_test.go
---

`repospec.Parse` (`spec.go:538-548`) unmarshals with plain `yaml.Unmarshal`
from `gopkg.in/yaml.v3` (`spec.go:19`) over `Default()`, not a strict/
`KnownFields` decoder. `TestABulwarkKeyIsUnrecognizedAndIgnored`
(`tests/repospec_test.go:260-270`) documents this as deliberate: a
`.gt-repo.yaml` still carrying `bulwark: {enabled: false}` from before the
rename parses successfully, the unknown `bulwark:` key is dropped, and the
result is the *default* spec — `Lydite.Enabled` comes back `true`. There is no
alias mapping `bulwark:` to `Lydite`; a repo that had opted out under the old
name silently loses that opt-out on upgrade rather than erroring or carrying
it forward.

`TestTheRetiredRequireUpToDateKeyStillParses` (`tests/repospec_test.go:274-283`)
looked, on first read, like a different compat strategy — a still-recognized,
translated field rather than a drop. It is not: grep confirms `spec.go` has no
`yaml:"require_up_to_date"` field anywhere (only a doc comment on
`BaseFreshness` at `spec.go:270` mentioning the retired name). It "still
parses" for the identical reason `bulwark:` does — permissive decoding drops
the unknown key — and only reads as correct because `BaseFreshness`'s default
(`auto`) happens to reproduce the old key's one meaningful setting. `bulwark:`
and `require_up_to_date` are the same mechanism with different luck: one
lands on the wrong default, the other lands on a default that happens to
match. Nothing found in `git log` (`grep -i bulwark` over all commits) shows a
rename commit or an alias ever being added or removed for `bulwark` — the
drop-and-ignore behavior looks like it was never anything else, not a
rejected alias.

Consequence for `internal/repospec` adopting strict/`KnownFields` decoding:
both `TestABulwarkKeyIsUnrecognizedAndIgnored` and
`TestTheRetiredRequireUpToDateKeyStillParses` pin *current* silently-dropped
behavior as passing tests, so strict decoding breaks both, not just the
`bulwark` one — and more broadly turns every legacy or misspelled key across
the whole governed fleet into a hard `gt repo check`/`sync` failure instead of
a silent default fallback. There was no "known-but-retired key" migration
path in place before this was noticed; the fix adopted for `require_up_to_date`
was to give it a real struct field and an explicit translation, turning an
accidental compatibility trick into a genuine one.

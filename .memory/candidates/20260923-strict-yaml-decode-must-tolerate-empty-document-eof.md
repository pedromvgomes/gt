---
about: yaml.NewDecoder(...).KnownFields(true).Decode() returns io.EOF for an empty document, and repospec.Parse must handle that explicitly to keep resolving an empty .gt-repo.yaml to the default spec
saw:
  - internal/repospec/spec.go
---

`Parse` (`spec.go`) switched from `yaml.Unmarshal(data, &spec)` to
`yaml.NewDecoder(bytes.NewReader(data)).KnownFields(true)` plus `.Decode(&spec)`
to reject unrecognized keys recursively. `yaml.Unmarshal` on an empty byte
slice is a no-op that leaves `spec` (pre-seeded with `Default()`) untouched.
`Decoder.Decode` on an empty document instead returns `io.EOF` — there is no
YAML document to decode, not zero fields to set — so `Parse` has to check for
and ignore that specific error (`errors.Is(err, io.EOF)`) or every repository
with an empty or all-comments `.gt-repo.yaml` starts failing `gt repo
check`/`sync` outright instead of falling back to defaults.

This is a `Decoder`-specific behavior difference from `Unmarshal` that is easy
to miss when switching between the two for `KnownFields` support — the two
functions are not drop-in replacements for an empty input.

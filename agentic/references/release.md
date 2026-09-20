# Releasing

## Cutting one

```sh
git tag v1.8.0
git push origin v1.8.0
```

`.github/workflows/release.yml` runs GoReleaser, which builds darwin/linux ×
amd64/arm64, publishes four `tar.gz` archives plus `checksums.txt`, and stamps
`main.version`, `main.commit` and `main.date` via `-ldflags`. **Keep those
variable names and their package stable** — `install.sh`, `internal/update` and
the workflows all read `gt --version`.

Release notes come from `docs/releases/<tag>.md` when that file exists;
otherwise GoReleaser generates a changelog from conventional-commit prefixes.
Write the notes file before tagging — a release cut without one loses the
narrative and cannot get it back.

Go's toolchain comes from `go.mod` via `go-version-file`, never a literal
`go-version`. A pinned `"1.26"` resolved to 1.26.0 and stayed there, silently
keeping a toolchain with 18 open stdlib advisories.

## The major tag move

Governed repositories pin gt's reusable workflows to a **moving major tag**
(`v0`, `v1`, …) rather than a SHA, so gate and sync logic reaches them without
editing any file — which matters because `GITHUB_TOKEN` cannot write
`.github/workflows/**`.

The "Move the major version tag" step is therefore load-bearing. **If it is
ever skipped, every governed repository silently freezes on the old logic with
no drift signal**: their caller files are still byte-identical, so `gt repo
check` reports compliant. That is why the step verifies the move rather than
assuming it worked. Do not weaken it.

The same property is why `v1` is the ref most likely to be stale in a local
checkout: a plain `git fetch` will not update a tag that has *moved*, only add
new ones. Use `git fetch origin --tags --force`.

## Installing

`install.sh` is the primary installer, served from `main` and piped to `sh`. It
verifies the checksum of the binary it downloads.

That verification is not enough for CI, and the reusable workflows deliberately
do **not** use it: the script doing the verifying would itself have been
fetched unpinned from `main` and piped into a shell, in every governed
repository, holding a token with write access. They use `gh release download`
plus an explicit `sha256sum -c` instead. semgrep's `gha-curl-pipe-shell` rule
was right; keep it that way.

`go install github.com/pedromvgomes/gt/cmd/gt@latest` is the fallback, and
produces a binary with `version = "dev"` — which `refuseDowngrade` treats as
major 0, because that is what it would actually render.

## Self-update

`internal/update` checks for a newer release in the background and `gt update`
applies one. `ManagedExternally` declines to self-update a binary installed by
a package manager. `update-check-cancelled-but-marked-checked` in the memory
store records a live trap here: the state file can record a check that was
cancelled rather than completed.

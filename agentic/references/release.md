# Releasing

## Cutting one

A release is the merge of its notes. A pull request that adds
`docs/releases/vX.Y.Z.md` and merges to `main` releases that version; the
`cut-a-release` skill walks the steps. Nobody pushes a tag, and a tag pushed by
hand releases nothing, because `release.yml` has no tag trigger.

`.github/workflows/release.yml` runs on every push to `main` and sweeps. Its
`plan` job runs `.github/scripts/release/plan.sh` over the pushed commit, the
remote's tags and its GitHub Releases, and picks a mode:

- **`release`** — notes exist and the tag does not. The `release` job pushes an
  unsigned annotated tag as `github-actions[bot]`, runs GoReleaser and moves the
  major tag.
- **`finish`** — the latest tag has no GitHub Release, which is a run that
  failed after tagging. It builds that tag and completes the release.
- **`none`** — nothing to do.

`plan.sh` fails on more than one pending version, and on a pending version that
is not newer than the latest tag, so notes added for an old version cannot
publish an older release. Only the latest tag is ever finished, so an old tag
that never had a release is not rebuilt with today's config.

GitHub does not start a workflow for a tag pushed with `GITHUB_TOKEN`, which is
why tagging, building and the major-tag move are one run rather than a tag
trigger. A failure rolls nothing back: the next push, or `gh workflow run
release.yml --ref main`, sweeps again. `-f dry_run=true` runs plan and verify
alone.

The jobs hold different tokens on purpose. `plan` and `verify` are read-only and
check out without persisting credentials; `verify` builds, vets and tests the
commit. Only `release` holds `contents: write`.

GoReleaser builds darwin/linux × amd64/arm64, publishes four `tar.gz` archives
plus `checksums.txt`, and stamps `main.version`, `main.commit` and `main.date`
via `-ldflags`. **Keep those variable names and their package stable** —
`install.sh`, `internal/update` and the workflows all read `gt --version`.

The release body is `docs/releases/<tag>.md`, so the file is required: a release
cannot get its narrative back afterwards.

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

`Check` and `Apply` use separate `HTTPClient`s because they bound very
different requests. `Check`'s client (`defaultClient`) keeps a 10s
`http.Client.Timeout`, sized for the small GitHub API metadata request —
that field bounds the whole request including the body read, so it would cut
off a release archive part way through on any connection slower than the
field allows. `Apply`'s client carries no `Client.Timeout` at all; `download`
instead wraps each request's context in its own deadline,
`Options.DownloadTimeout`, which defaults to 5 minutes and is sized for a
multi-megabyte archive rather than a JSON response.

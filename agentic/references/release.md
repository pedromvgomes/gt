# Releasing

## Cutting one

```sh
git tag v1.8.0
git push origin v1.8.0
```

`.github/workflows/release.yml` runs GoReleaser, which builds darwin/linux ×
amd64/arm64, publishes four `tar.gz` archives plus `checksums.txt`, and stamps
`main.version`, `main.commit` and `main.date` via `-ldflags`. **Keep those
variable names and their package stable** — `repogov`'s `MajorTag` and
`.gt-repo.yaml`'s recorded version read the stamp as `3.0.0`, without the `v`.
`internal/app` adds the `v` only where it hands the version to the
vipengele libraries, because `IsRelease` requires it. The reusable workflows
and `ensure-gt.sh` only run `gt version` to show what was installed; none of
them parses its output.

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

## Signing

GoReleaser signs `checksums.txt` with minisign, producing
`checksums.txt.minisig`; the `signs:` block reads the key from
`MINISIGN_KEY_FILE`, which `release.yml` writes to `$RUNNER_TEMP` from the
`MINISIGN_KEY` repository secret (an unencrypted key, from `minisign -G -W`).
Before GoReleaser runs, `release.yml` signs a probe file and verifies it
against the committed `internal/selfupdate/minisign.pub`, so a secret that does
not match the embedded public key fails the release before anything is
published. The key file is removed in an `if: always()` step.

The public key is embedded in the binary and is the only thing `gt update`
trusts. Rotating the key means a new `MINISIGN_KEY` secret and a new
`minisign.pub` in the same release; binaries already installed cannot verify a
release signed with a key they do not embed.

The installers and the reusable workflows verify `checksums.txt` by SHA-256
only; the minisign signature is checked by `gt update`.

## Self-update

`internal/selfupdate` builds the `go.vipengele.org/update` `Config`: `Base`
sets gt's identity, the `v`-normalised version, the embedded minisign key,
`app.PathsLayout` and the asset names GoReleaser produces
(`gt_<os>_<arch>.tar.gz`); `ForRun` applies the user's `auto_update` settings
for one run. `internal/app` wires the `gt update` command and the background
check around it. The check is unauthenticated (`Token` returns "") so a
per-repository `GH_TOKEN` never reaches it.

State is under the state directory (`~/.local/state/gt`, or `$GT_HOME/state`).
A check cancelled by the command finishing is not recorded, so the next run
checks again; a failed live check is recorded and throttles until
`auto_update.check_interval`. `gt update` refuses a package-manager-managed
binary (Homebrew) and a `dev` build, and runs the downloaded binary before
installing it. `update --check` exits 10 when an update exists.

`ForRun` rejects a `check_interval` under one hour only when the check is
enabled; `gt update` itself does not depend on `auto_update`.

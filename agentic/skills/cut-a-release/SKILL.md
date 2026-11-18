---
name: cut-a-release
description: >
  Cut a new release of gt. Trigger when the user asks to release, ship a
  version, cut or tag a release, bump the version, or write release notes. A
  release is the merge of its notes file to main; nobody pushes a tag.
---

# cut-a-release

A release is a pull request that adds `docs/releases/vX.Y.Z.md` and merges to
`main`. `.github/workflows/release.yml` then tags the merge commit, runs
GoReleaser and moves the major tag. **Never run `git tag` or push a tag**: there
is no tag trigger, and a hand-pushed tag releases nothing.

## Steps

1. **Work in a worktree** (`gt wt add chore/release-vX.Y.Z`, or the feature's own
   worktree when the notes ship with the change). Run `git fetch origin --tags
   --force` and read `origin/main`, not `main`.
2. **Pick the version.** Find the latest tag (`git tag --sort=-v:refname | head`)
   and list what landed since (`git log <tag>..origin/main --oneline`). Any
   `type!:` subject or breaking note means a major bump; a `feat` means a minor;
   only fixes mean a patch. If the user named a version that disagrees with the
   commits, say so before writing it.
3. **Write `docs/releases/vX.Y.Z.md`**, in the shape of the files beside it: a
   `## Highlights` list of what changed for a user, a section per change that
   needs explaining, and an `## Upgrading from vA.B.C` block with the commands to
   run. Comments and notes describe the release, not the PR that made it.
4. **Open the pull request** with a conventional-commit title such as
   `chore(release): vX.Y.Z`. The release is not run on the PR; merging it is the
   release.
5. **After the merge**, watch the run: `gh run list --workflow release.yml
   --limit 3`, then `gh run watch <id>`. When it finishes, `git fetch origin
   --tags --force` and confirm that `vX.Y.Z` exists, that `gh release view
   vX.Y.Z` lists four archives plus `checksums.txt` and
   `checksums.txt.minisig`, and that the major tag (`v2`, ...) points at the
   same commit.

## Constraints

- Exactly one pending version at a time: two notes files without tags fail the
  plan.
- The version must be newer than the latest tag. Notes added for an old version
  fail the plan rather than publishing an older release.
- The notes file is the GitHub Release body. Write it before merging; a release
  cannot get its narrative back afterwards.
- The major-tag move in `release.yml` is load-bearing. Governed repositories pin
  to it, and skipping it freezes the fleet on old logic with no drift signal.

## A release that failed part way

The next push to `main` sweeps again, and so does a manual dispatch:
`gh workflow run release.yml --ref main`. A tag with no GitHub Release is
finished; a release whose major-tag move failed is repaired on the same run.
`gh workflow run release.yml --ref main -f dry_run=true` runs plan and verify
alone and writes nothing.

See `agentic/references/release.md` for the pipeline's reasoning.

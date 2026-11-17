# Governance

`gt repo` keeps many repositories structurally consistent. A template repo
cannot: templates copy once and never propagate. A repository opts in by
committing `.gt-repo.yaml`, and that one file drives three mechanisms.

| Layer | How it reaches the repo | Needs |
|---|---|---|
| Stage logic (attestation, conventional commits, lydite, governance) | gt's reusable workflows, pinned to a moving major tag | nothing — a gt release is enough |
| Files that must exist in-repo (orchestrators, `dependabot.yml`, CODEOWNERS) | `gt repo sync` | a local run or the weekly in-repo job |
| GitHub API state (branch protection, merge methods, strict required checks) | `gt repo settings apply` | your own `gh` credentials |

## Only overrides are written

`.gt-repo.yaml` records what a repository decided **differently**. Everything
absent follows gt's default and keeps following it as that default changes.

This is load-bearing, not a formatting preference. A repository that spells out
a default has silently stopped tracking it: changing the opinion in gt would
reach none of the repositories whose files already state the old value, and the
propagation this subsystem exists for would quietly do nothing. So `sync` writes
only overrides, and `check` reports a file restating a default as **drift**.

`repospec.Parse` unmarshals over `Default()` rather than over a zero value,
which is what makes an explicit `false` against a default of `true` a real
override rather than an absent key. Any change to a default must preserve that.

`gt repo config [--json]` prints the resolved spec with defaults applied. gt's
own workflows consume that rather than re-parsing YAML — see the `yq` trap in
`reusable-dependabot-auto-merge.yml`, where `//` treated an explicit `false`
exactly like an absent key.

## Managed vs scaffold

`repogov/render.go` holds one registry, `registry()`, and both rendering and
orphan detection read from it. Keeping two lists in sync by hand is how a
managed path silently stops being orphan-checked.

- **`ModeManaged`** — gt owns the content. Any difference is drift and `sync`
  rewrites the file. Every managed file carries the `Managed by gt` marker.
- **`ModeScaffold`** — gt creates the file once and the repository owns it from
  then on. gt never rewrites a scaffold and never deletes one, because both
  would destroy code gt did not write. **Existence is the entire contract.**
  The `ci-*`/`cd-*` stage files are scaffolds. gt scaffolds no coverage
  configuration at all — the `lydite` job forwards entirely to lydite's own
  pipeline, which reads its own config from the scan root.

`diff.go` reports four states: `ok`, `drifted`, `missing`, and `orphaned` — the
last meaning gt rendered the file previously but the spec no longer asks for it.
Without `orphaned`, dropping an ecosystem would leave the old file on disk and
still running while `check` called the repository compliant. Scaffolds are
deliberately absent from `managedPaths()`: an orphaned scaffold holds the
repository's own work.

## lydite's exemptions file

Wherever `lydite.enabled` is true, gt renders `.lydite/exemptions.yml` as a
**managed** file (`repogov/exemptions.go`, registered in `registry()`). Without
it lydite declares no exemptions and refers every change, so every Dependabot
pull request waits on `lydite/referral` for a human. The file holds:

- **`dependency-version-bump`**, derived from `dependabot:` while
  `lydite.exempt_dependency_bumps` is true (the default), with `versions:
  patch-and-minor`. Its paths are the files lydite can measure, joined under
  each entry's `directory`, sorted and de-duplicated: `gomod` → `go.mod`, `go.sum`;
  `cargo` → `Cargo.lock`; `npm` → `package-lock.json`. `pip`, `docker`,
  `terraform` and `github-actions` contribute nothing: lydite cannot yet
  measure an action bump, so a workflow path would pass vacuously and the
  bump stays referred. Manifests (`Cargo.toml`, `package.json`) are never
  listed: lydite reads no comparison from them, so the condition would pass
  vacuously over a manifest-only edit.
- **`lydite.exemptions`**, the repository's own entries, rendered after the
  derived one in declared order. `repospec.validateLydite` refuses what lydite
  would refuse to load, plus the derived entry's name while it is on.

No entries means no file, and a file the spec stops asking for is orphaned and
removed. lydite reads the file from its scan root, so `lydite.dir` moves it;
patterns stay repository-root-relative. Orphan detection checks both the root
location and the one the current spec puts it at (`DiffSpec`), but a file left
under a `lydite.dir` the spec no longer names is invisible to it.

lydite reads the file from the base commit, disqualifies any change under
`.lydite/`, and wants a pull request that changes the file to change nothing
else. A sync that changes it is therefore always referred and needs a `/lydite
clear`; gt does not split sync pull requests to isolate it. See [ADR 0003](../../docs/adr/0003-lydite-exemptions-are-managed-not-scaffolded.md)
for why the file is managed rather than scaffolded.

## The version gate

`Spec.GTVersion` (`gt_version`) records the gt release that last rendered the
repo, and **sync writes it**. `refuseDowngrade` compares majors only, because
the major is exactly what the `uses:` pin carries: a stale gt would silently
repoint every caller at an older major. There is deliberately no `--force` —
rolling back means editing `gt_version` by hand, which is a considered act.

## Retired and unknown keys

`repospec.Parse` decodes `.gt-repo.yaml` with `yaml.KnownFields(true)`,
recursively: a key no struct in the tree claims is a parse error naming the
field, not a line silently dropped. A misspelled or retired key that parsed
successfully would produce the default spec while its author believes it is
in force — indistinguishable, from the repo's side, from the setting actually
applying.

Retiring a key is therefore not the same as deleting its struct field.
`BranchProtection.BaseFreshness` and `RequireUpToDate` in
`internal/repospec/spec.go` stay real, tagged fields (`omitempty`, `json:"-"`)
so a manifest still carrying either keeps parsing, and `dropRetiredKeys()`
clears them before validation runs. Removing the fields outright would turn
every manifest still carrying them into a hard `gt repo check`/`sync` failure
across the fleet.

Clearing is what retires the key from the fleet without breaking anyone on the
way out: the resolved spec does not hold it, so the manifest does not
round-trip, `check` reports it as spec drift, and `sync` writes the file back
without it.

## Working on this

- Adding a rendered file means adding a `fileSpec` to `registry()`, a template
  under `internal/repogov/templates/`, and — if it is managed — an entry that
  orphan detection will see. Do not add a second list. A file whose location
  depends on the spec sets `pathFor`; its `path` stays the default location,
  and orphan detection checks both.
- Adding a spec field means a default in `Default()`, validation if the value
  can be wrong, and a decision about whether `sync` should strip it when it
  merely restates the default.
- Retiring a spec field means giving it the same keep-the-tag-then-clear
  treatment as `base_freshness` and `require_up_to_date`, not deleting it — see
  "Retired and unknown keys" above.
- Shared policy (Dependabot cooldown, commit-message prefixes, the sync
  schedule) belongs in gt's templates, never in `.gt-repo.yaml` — that is what
  makes changing it everywhere one gt release.

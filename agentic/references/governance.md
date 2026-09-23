# Governance

`gt repo` keeps many repositories structurally consistent. A template repo
cannot: templates copy once and never propagate. A repository opts in by
committing `.gt-repo.yaml`, and that one file drives three mechanisms.

| Layer | How it reaches the repo | Needs |
|---|---|---|
| Stage logic (attestation, conventional commits, lydite, governance) | gt's reusable workflows, pinned to a moving major tag | nothing — a gt release is enough |
| Files that must exist in-repo (orchestrators, `dependabot.yml`, CODEOWNERS) | `gt repo sync` | a local run or the weekly in-repo job |
| GitHub API state (branch protection, merge methods, the queue) | `gt repo settings apply` | your own `gh` credentials |

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
`BranchProtection.RequireUpToDate` in `internal/repospec/spec.go` stays a real,
tagged field so a manifest still carrying it keeps parsing, and
`resolveFreshness()` translates it onto `BaseFreshness` and clears it before
validation runs. Removing the field outright would turn every manifest still
carrying the old spelling into a hard `gt repo check`/`sync` failure across
the fleet.

## Working on this

- Adding a rendered file means adding a `fileSpec` to `registry()`, a template
  under `internal/repogov/templates/`, and — if it is managed — an entry that
  orphan detection will see. Do not add a second list.
- Adding a spec field means a default in `Default()`, validation if the value
  can be wrong, and a decision about whether `sync` should strip it when it
  merely restates the default.
- Retiring a spec field means giving it the same translate-then-clear treatment
  as `require_up_to_date`, not deleting it — see "Retired and unknown keys"
  above.
- Shared policy (Dependabot cooldown, commit-message prefixes, the sync
  schedule) belongs in gt's templates, never in `.gt-repo.yaml` — that is what
  makes changing it everywhere one gt release.

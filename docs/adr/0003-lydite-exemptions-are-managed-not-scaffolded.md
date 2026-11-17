# lydite's exemptions file is managed, with repository entries in .gt-repo.yaml

Every governed repository with `lydite.enabled` gets `.lydite/exemptions.yml`
as a **managed** file: gt owns its content, `gt repo check` reports any hand
edit as drift, and `gt repo sync` rewrites it. It carries a
`dependency-version-bump` entry derived from the `dependabot:` block, with
`versions: patch-and-minor`, so a Dependabot bump that stays within patch or
minor and keeps its licence and SCA rows green is no longer referred.
Anything else a repository wants exempt goes under `lydite.exemptions:` in
`.gt-repo.yaml` and is rendered after the derived entry.
`lydite.exempt_dependency_bumps: false` drops the derived entry for a
repository that wants every bump read.

The problem is the referral default. With no exemptions file, lydite refers
every change, `lydite/referral` is a required check, and so the nightly
Dependabot auto-merge finds every Dependabot pull request blocked until a
human comments `/lydite clear` on each one. The entry that fixes it is the
same in every repository except for its paths, and those are exactly the
lockfiles of the Dependabot directories gt already knows.

## The rejected alternative: a scaffold

A scaffold would have written the derived entry once and handed the file to
the repository, which could then add its own entries in place. It was
rejected because the derived entry is the part worth propagating, and a
scaffold propagates nothing:

- **It stops tracking `dependabot:`.** A repository that adds an ecosystem or
  a directory after the scaffold landed gets no paths for it, and its new
  bumps are referred again with nothing reporting why.
- **It cannot be corrected fleet-wide.** Which files an ecosystem contributes
  follows what lydite can measure. When lydite learns to read a new lockfile,
  or can measure an action bump and `github-actions` starts to contribute
  paths, a scaffolded file would need a hand edit in every repository.
- **It cannot be retired.** gt never deletes a scaffold, so turning lydite off
  would leave an exemptions file behind that nothing claims.

The cost of managing it is that a repository cannot edit the file directly.
`lydite.exemptions:` exists so that cost is a relocation of the entries, not
a loss of them. A repository being onboarded that already carries a
hand-written `.lydite/exemptions.yml` has it overwritten on the first sync, so
its entries have to move into `lydite.exemptions:` first.

## Sync pull requests are not split

lydite reads the exemptions file from the base commit, disqualifies any change
under `.lydite/`, and wants a pull request that changes the file to change
nothing else. gt does not open a separate pull request for it: a `gt repo
sync` that changes this file, alone or together with others, produces one pull
request that is referred and needs a `/lydite clear`. Isolating the file would
not avoid the clearance, since the disqualifier fires either way, and would
make sync's output depend on which files changed, for a case a reviewer
handles once per change in gt's policy.

## Fleet rollout

The default is on, so the next `gt repo sync` in every governed repository
with lydite enabled and a Dependabot ecosystem lydite can read writes the
file — the weekly in-repo sync included, since it is not a workflow file.
From then on, patch and minor bumps in those ecosystems stop being referred.
Bumps lydite cannot measure — `pip`, `docker`, `terraform`, and GitHub
Actions, which lydite cannot yet measure — stay referred.

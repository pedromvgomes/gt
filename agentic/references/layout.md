# Layout

The annotated tree, and which reference governs each package.

```
cmd/gt/                 # the CLI surface: cobra commands, flag wiring, and nothing else
  main.go               # every command except `repo`; version var, update check, ui construction
  repo.go               # gt repo init|check|sync|config
  repo_settings.go      # gt repo settings diff|apply
  repo_fleet.go         # gt repo fleet check|sync|merge-pending

internal/               # all the logic; cmd/ is a thin shell over it
  git/                  # the one git seam: Runner, ExecRunner, FindRepoRoot, ParseDefaultBranch
  ui/                   # printer, prompts, colour, and ExitError (the exit-code carrier)
  config/               # global + per-repo YAML: Config, Profile, SSH, Setup, Template; Load/Merge/Validate
  clone/                # gt clone: bare layout, URL resolution, HTTPS→SSH, ~/.ssh/config aliases
  worktree/             # gt wt add|rm|list|nuke|prune-branches; ParseSpec validates <type>/<name>
  scratch/              # gt scratch: the single top-level scratch worktree
  setauth/              # gt set-auth: the managed .envrc, GH_TOKEN, environment profiles
  setssh/               # gt set-ssh: rewrite a clone's remote to an SSH alias
  setup/                # setup templates: Context, Select, MatchURL, Plan, Execute
  update/               # gt update and the background update check; release lookup, install, state
  repospec/             # spec.go alone: .gt-repo.yaml's types, Default(), Parse, validation
  repogov/              # everything that acts on a spec — see governance.md
    detect.go           #   ecosystem detection for `repo init`
    render.go           #   the fileSpec registry: what gt renders, and managed vs scaffold
    pipeline.go         #   stage wiring, ci-gate's needs:, the attestation guard
    diff.go             #   ok / drifted / missing / orphaned, and Write
    service.go          #   Check and Sync, the version gate, ResolveWorkDir
    settings.go         #   the GitHub API side: ruleset, merge methods, merge queue
    fleet.go            #   fleet listing, Dependabot title parsing, merge-pending
    fleetsync.go        #   the cross-repo sweep that opens a PR per repo
    templates/          #   embedded text/template sources for every rendered file

tests/                  # every test lives here, one file per area, package `tests`
docs/pipeline-design.md # why the pipeline is shaped the way it is
docs/releases/          # one file per release
agentic/                # this directory — see the -repo instruction
.github/workflows/      # gt's own CI, plus the reusable-*.yml other repos call
```

## Which reference governs what

| Changing | Read |
|---|---|
| `internal/repospec`, `.gt-repo.yaml`, `repogov/{render,diff,service,detect}.go` | [`governance.md`](governance.md) |
| `repogov/pipeline.go`, the orchestrator templates, `ci-gate`, attestation | [`pipeline.md`](pipeline.md) |
| `repogov/settings.go`, branch protection, `base_freshness` | [`merge-queue.md`](merge-queue.md) |
| `repogov/fleet.go`, `fleetsync.go`, `reusable-dependabot-auto-merge.yml` | [`dependabot.md`](dependabot.md) |
| `internal/{clone,worktree,scratch,setauth,setssh,setup,config}` | [`worktrees-and-setup.md`](worktrees-and-setup.md) |
| `.goreleaser.yaml`, `install.sh`, `internal/update`, `docs/releases/` | [`release.md`](release.md) |

## Two structural rules

- **Tests live in `tests/`, code lives in `internal/`.** The suite is run with
  `-coverpkg=./internal/...` for exactly this reason; a plain `go test` measures
  coverage on the test package and reports near zero. `.bulwark.yml` documents
  the same trap.
- **`cmd/gt` holds no logic worth testing.** Anything with a decision in it
  belongs in `internal/`, where the suite can reach it without a process.

---
description: What gt is, how to build and test it, and the boundaries an agent works inside. Scoped to the gt repository itself — no consumer stack should list it.
---

# gt

A Go CLI for bare-repo + worktree git workflows, and for keeping a fleet of
repositories structurally consistent. Two halves in one binary: the local side
(`clone`, `wt`, `scratch`, `set-auth`, `setup`) that gives every repository the
same layout and credential wiring, and `gt repo`, which propagates CI
pipelines, Dependabot config and branch protection from one committed
`.gt-repo.yaml` per repository.

User-facing documentation is [README.md](README.md); the pipeline's rationale
is [docs/pipeline-design.md](docs/pipeline-design.md).

## Where the detail is

**This file is the map, not the territory.** Every deep narrative lives in
`agentic/references/`, one file per concern, and is read **on demand** — open
the one that governs what you are about to change, before you change it. Do not
read them all; do not start work in one of these areas without reading its row.

| Read | Before changing |
|---|---|
| [`layout.md`](agentic/references/layout.md) | anything — the annotated tree, and which reference governs each package |
| [`governance.md`](agentic/references/governance.md) | `internal/repospec`, `.gt-repo.yaml`, or `repogov/{render,diff,service,detect}.go` |
| [`pipeline.md`](agentic/references/pipeline.md) | `repogov/pipeline.go`, an orchestrator template, `ci-gate`, or the attestation |
| [`merge-queue.md`](agentic/references/merge-queue.md) | `repogov/settings.go`, branch protection, or `base_freshness` |
| [`dependabot.md`](agentic/references/dependabot.md) | `repogov/fleet.go`, `fleetsync.go`, or `reusable-dependabot-auto-merge.yml` |
| [`worktrees-and-setup.md`](agentic/references/worktrees-and-setup.md) | `internal/{clone,worktree,scratch,setauth,setssh,setup,config}` |
| [`release.md`](agentic/references/release.md) | `.goreleaser.yaml`, `install.sh`, `internal/update`, or cutting a release |

## Prescriptive rules

Prescriptive rules live in `agentic/rules/`, one file per rule, kebab-case
filename matching the rule's intent. **Read every file in that directory before
making changes here, and follow each one strictly.** New rules go there.

## Memory

Durable findings about this codebase — invariants, rationale, gotchas that cost
real exploration — live in `.memory/`. Check `agtk memory index` and read the
ones that touch what you are changing with `agtk memory show <name>`; several
record invariants that are not visible in the code that depends on them.

## Commands

```sh
go build ./...
go test ./tests/... -coverpkg=./internal/... -coverprofile=coverage.out
go vet ./...
golangci-lint run          # must be clean before a PR
go run ./cmd/gt <args>     # run the CLI locally

gt repo check              # render the spec and diff it against the tree
gt repo config --json      # the resolved spec, defaults applied
```

`-coverpkg=./internal/...` is not optional: every test lives in `./tests` and
every line of code in `./internal`, so a plain `go test` measures the test
package and reports near zero.

## Conventions

- **Tests live in `tests/`, logic lives in `internal/`, and `cmd/gt` is a thin
  shell.** Anything with a decision in it belongs where the suite can reach it
  without spawning a process.
- **`cmd/gt` exposes `var version = "dev"`**, overridden at release via
  `-ldflags`. Keep that variable name and package stable.
- **golangci-lint v2 schema**, with `errcheck` on: check returned errors.
- Conventional Commits on PR titles; the squash subject is the PR title.

## Boundaries

- **Always:** run `go build ./...`, `go test ./tests/...`, `go vet ./...` and
  `golangci-lint run` before proposing a PR. Change `fleet.go`'s title regex
  and the auto-merge workflow's bash regex together — they parse the same
  titles.
- **Ask first:** changing a `repospec` default (it reaches every governed
  repository on the next sync), widening a stage's `permissions:`, altering the
  release archive layout, or renaming the binary.
- **Never:** hand-edit `AGENTS.md`, `CLAUDE.md`, `.mcp.json`, or anything under
  `.claude/`, `.agents/` or `.codex/` — all of them are rendered from
  `agentic/` by `agtk sync`, and an edit there is gone on the next render.
  Never commit secrets or `dist/`. Never weaken the untrusted-template gate in
  `internal/setup` or the major-tag move in `release.yml`.

## Worktrees

gt is developed the way it asks other repositories to be used: a bare repo plus
typed worktrees, one session one `gt wt add <type/name>`. Never use raw `git
worktree`, never edit inside `.bare/`, and never report what is on `main`
without `git fetch origin --tags --force` first — see
`agentic/rules/worktree-per-session.md`.

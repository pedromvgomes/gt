package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
)

// writeWorkflow drops a workflow file into a fixture repo.
func writeWorkflow(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
}

func specRequiring(names ...string) repospec.Spec {
	s := repospec.Default()
	s.Checks.Required = names
	return s
}

// wardnet's shape: the workflow always triggers and gates individual jobs with
// job-level if:, so every job still reports a conclusion. This must lint clean
// — it is the pattern gt asks repos to adopt.
func TestLintAcceptsJobLevelGating(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on:
  pull_request:
    branches: [main]
jobs:
  preflight:
    name: Preflight
    runs-on: ubuntu-latest
  build:
    name: Build Daemon
    needs: preflight
    if: needs.preflight.outputs.daemon == 'true'
    runs-on: ubuntu-latest
`)
	findings, err := repogov.Lint(root, specRequiring("Preflight", "Build Daemon"))
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("Lint() = %v, want no findings", findings)
	}
}

// A top-level paths: filter means the workflow may not run at all, producing no
// check run — which the gate cannot distinguish from "not started yet".
func TestLintRejectsTopLevelPathsFilter(t *testing.T) {
	for _, filter := range []string{"paths", "paths-ignore"} {
		t.Run(filter, func(t *testing.T) {
			root := t.TempDir()
			writeWorkflow(t, root, "ci.yml", `
name: CI
on:
  pull_request:
    `+filter+`:
      - "src/**"
jobs:
  build:
    name: Build
    runs-on: ubuntu-latest
`)
			findings, err := repogov.Lint(root, specRequiring("Build"))
			if err != nil {
				t.Fatalf("Lint() error = %v", err)
			}
			if len(findings) != 1 {
				t.Fatalf("Lint() = %v, want exactly one finding", findings)
			}
			if !strings.Contains(findings[0].Message, filter) {
				t.Errorf("finding %q does not mention %q", findings[0].Message, filter)
			}
		})
	}
}

// A check declared optional is allowed to be absent, so the same filter that
// fails for a required check must be tolerated here.
func TestLintIgnoresOptionalChecks(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "e2e.yml", `
name: E2E
on:
  pull_request:
    paths: ["e2e/**"]
jobs:
  e2e:
    name: E2E Tests
    runs-on: ubuntu-latest
`)
	spec := repospec.Default()
	spec.Checks.Optional = []string{"E2E Tests", "Never Produced"}
	findings, err := repogov.Lint(root, spec)
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("Lint() = %v, want no findings for optional checks", findings)
	}
}

func TestLintCatchesUnproducibleCheck(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: pull_request
jobs:
  build:
    name: Build
    runs-on: ubuntu-latest
`)
	findings, err := repogov.Lint(root, specRequiring("Buidl"))
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "no workflow produces") {
		t.Fatalf("Lint() = %v, want a finding about an unproducible check", findings)
	}
}

// A matrix job reports one check per matrix value with the expression already
// expanded, so the literal template text can never match a real check.
func TestLintCatchesTemplatedCheckName(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "codeql.yml", `
name: CodeQL
on: pull_request
jobs:
  analyze:
    name: Analyze (${{ matrix.language }})
    runs-on: ubuntu-latest
`)
	findings, err := repogov.Lint(root, specRequiring("Analyze (${{ matrix.language }})"))
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "${{ }}") {
		t.Fatalf("Lint() = %v, want a finding about an unexpanded expression", findings)
	}
}

func TestLintCatchesNonPRWorkflow(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on:
  push:
    branches: [main]
jobs:
  build:
    name: Build
    runs-on: ubuntu-latest
`)
	findings, err := repogov.Lint(root, specRequiring("Build"))
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "pull_request") {
		t.Fatalf("Lint() = %v, want a finding about the missing pull_request trigger", findings)
	}
}

// A job that calls a local reusable workflow reports as "<caller> / <called>",
// unlike a plain job which reports under its own name alone. wardnet depends on
// both forms, so both must resolve.
func TestLintResolvesLocalReusableWorkflows(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "pr.yml", `
name: PR
on:
  pull_request:
    branches: [main]
jobs:
  build-daemon:
    name: Build Daemon
    uses: ./.github/workflows/build-daemon.yml
  all-checks:
    name: All checks passed
    runs-on: ubuntu-latest
`)
	writeWorkflow(t, root, "build-daemon.yml", `
name: Build Daemon
on:
  workflow_call:
jobs:
  check:
    name: Check Daemon
    runs-on: ubuntu-latest
`)
	findings, err := repogov.Lint(root, specRequiring("Build Daemon / Check Daemon", "All checks passed"))
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("Lint() = %v, want no findings", findings)
	}
}

// A remote `uses:` cannot be enumerated without fetching it, so an unmatched
// name might legitimately come from there. Reporting it as a typo would be a
// false positive.
func TestLintStaysSilentWhenRemoteUsesIsUnresolvable(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "pr.yml", `
name: PR
on: pull_request
jobs:
  gate:
    name: PR
    uses: pedromvgomes/gt/.github/workflows/gate.yml@v0
`)
	findings, err := repogov.Lint(root, specRequiring("Something From Upstream"))
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("Lint() = %v, want no findings when a remote uses: is unresolvable", findings)
	}
}

func TestLintHandlesMissingWorkflowDir(t *testing.T) {
	findings, err := repogov.Lint(t.TempDir(), repospec.Default())
	if err != nil {
		t.Fatalf("Lint() error = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("Lint() = %v, want no findings", findings)
	}
}

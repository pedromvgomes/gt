package tests

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/config"
	vcli "go.vipengele.org/cli/core"
	"go.vipengele.org/cli/spec"
)

// probeConfig runs a command added to gt's App that records the configuration
// the App loaded for it, so every layer is resolved by the same chain the
// shipped binary uses: defaults, $XDG_CONFIG_HOME/gt/config.yaml, the managed
// root's .gt.yaml, then GT_* variables.
func probeConfig(t *testing.T, env *spec.Env) (config.Config, appRun) {
	t.Helper()
	a := devApp()
	var (
		cfg    config.Config
		loaded bool
	)
	a.Add(&spec.Command{
		Name:    "probe",
		Summary: "record the configuration the App loads for a command",
		Run: func(ctx context.Context, _ *spec.Invocation) error {
			res, ok := vcli.ConfigFrom[config.Config](ctx)
			if ok {
				cfg, loaded = res.Value(), true
			}
			return nil
		},
	})
	got := runApp(t, a, env, "probe")
	if got.code == vcli.ExitOK && !loaded {
		t.Fatal("probe ran without a loaded configuration")
	}
	return cfg, got
}

// probeConfigOK is probeConfig for a configuration that must load.
func probeConfigOK(t *testing.T, env *spec.Env) config.Config {
	t.Helper()
	cfg, got := probeConfig(t, env)
	if got.code != vcli.ExitOK {
		t.Fatalf("probe exit = %d, stderr = %q", got.code, got.stderr)
	}
	return cfg
}

// probeConfigFails is probeConfig for a configuration that must not load; it
// returns what the App wrote to stderr.
func probeConfigFails(t *testing.T, env *spec.Env) string {
	t.Helper()
	_, got := probeConfig(t, env)
	if got.code != vcli.ExitError {
		t.Fatalf("probe exit = %d, want %d; stderr = %q", got.code, vcli.ExitError, got.stderr)
	}
	return got.stderr
}

// inManagedRoot creates a managed root carrying overlay as its .gt.yaml and
// moves the run into a directory nested several levels below it, where the
// overlay has to be found by walking up to the .bare directory.
func inManagedRoot(t *testing.T, env *spec.Env, overlay string) {
	t.Helper()
	root := managedRoot(t, overlay)
	nested := filepath.Join(root, "feature", "some-work", "internal", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	env.WorkDir = nested
}

// A missing global file is valid: the defaults apply, and loading writes
// nothing. Only `gt config edit` creates the file.
func TestAppConfigWithoutGlobalFileUsesDefaults(t *testing.T) {
	env, configHome := appEnv(t)

	cfg := probeConfigOK(t, env)
	if !reflect.DeepEqual(cfg.WorktreeTypes, []string{"feature", "fix", "chore"}) {
		t.Fatalf("WorktreeTypes = %#v", cfg.WorktreeTypes)
	}
	if want := config.Default().AutoUpdate; cfg.AutoUpdate != want {
		t.Fatalf("AutoUpdate = %#v, want %#v", cfg.AutoUpdate, want)
	}
	if _, err := os.Stat(filepath.Join(configHome, "gt", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("loading wrote a global config, stat error = %v", err)
	}
}

// Every section of the global file reaches the command: setup templates,
// per-user SSH aliases and profiles, in file order.
func TestAppConfigReadsTheGlobalFile(t *testing.T) {
	env, configHome := appEnv(t)
	writeUserConfig(t, configHome, `worktree_types: [feature]
setup:
  templates:
    - name: agentic-toolkit
      match: ["github.com:pedromvgomes/*"]
      run: |
        echo hi
    - name: golang-extras
      match: ["*"]
      script: /path/to/script.sh
ssh:
  host_aliases:
    github.com: github-personal
  user_aliases:
    pedromvgomes:
      github.com: github-personal
    pedro-work:
      github.com: github-work
profiles:
  - name: work
    match: ["github.com:acme/*"]
    env:
      CLAUDE_CONFIG_DIR: ~/.claude
  - name: personal
    env:
      CODEX_HOME: ~/.codex
`)

	cfg := probeConfigOK(t, env)
	wantTemplates := []config.Template{
		{Name: "agentic-toolkit", Match: []string{"github.com:pedromvgomes/*"}, Run: "echo hi\n"},
		{Name: "golang-extras", Match: []string{"*"}, Script: "/path/to/script.sh"},
	}
	if !reflect.DeepEqual(cfg.Setup.Templates, wantTemplates) {
		t.Fatalf("Templates = %#v, want %#v", cfg.Setup.Templates, wantTemplates)
	}
	if got := cfg.SSH.UserAliases["pedromvgomes"]["github.com"]; got != "github-personal" {
		t.Fatalf("UserAliases[pedromvgomes][github.com] = %q", got)
	}
	if got := cfg.SSH.UserAliases["pedro-work"]["github.com"]; got != "github-work" {
		t.Fatalf("UserAliases[pedro-work][github.com] = %q", got)
	}
	if len(cfg.Profiles) != 2 {
		t.Fatalf("len(Profiles) = %d, want 2", len(cfg.Profiles))
	}
	if cfg.Profiles[0].Name != "work" || cfg.Profiles[0].Env["CLAUDE_CONFIG_DIR"] != "~/.claude" {
		t.Fatalf("Profiles[0] = %#v", cfg.Profiles[0])
	}
	if len(cfg.Profiles[1].Match) != 0 {
		t.Fatalf("Profiles[1].Match = %#v, want none", cfg.Profiles[1].Match)
	}
}

// A managed root's .gt.yaml, found from a directory nested below the root,
// replaces worktree_types and the ssh maps set by the global file.
func TestAppConfigAppliesTheManagedRootOverlay(t *testing.T) {
	env, configHome := appEnv(t)
	writeUserConfig(t, configHome, "worktree_types: [feature]\nssh:\n  host_aliases:\n    gitlab.com: gitlab-personal\n")
	inManagedRoot(t, env, `worktree_types:
  - hotfix
ssh:
  host_aliases:
    github.com: github-personal
`)

	cfg := probeConfigOK(t, env)
	if !reflect.DeepEqual(cfg.WorktreeTypes, []string{"hotfix"}) {
		t.Fatalf("WorktreeTypes = %#v", cfg.WorktreeTypes)
	}
	if want := map[string]string{"github.com": "github-personal"}; !reflect.DeepEqual(cfg.SSH.HostAliases, want) {
		t.Fatalf("HostAliases = %#v, want %#v", cfg.SSH.HostAliases, want)
	}
}

// A template or profile the overlay shadows by name takes the global entry's
// place: one entry under that name, at the global entry's position, so a
// shadowed template never runs twice and first-match-wins profile order holds.
// Names the overlay adds come after every global entry.
func TestAppConfigReplacesShadowedNamesInPlace(t *testing.T) {
	env, configHome := appEnv(t)
	writeUserConfig(t, configHome, `worktree_types: [feature]
setup:
  templates:
    - name: shared
      run: "echo global"
    - name: global-only
      run: "echo global-only"
profiles:
  - name: work
    env:
      A: "global"
  - name: personal
    env:
      B: "1"
`)
	inManagedRoot(t, env, `setup:
  templates:
    - name: shared
      run: "echo repo"
    - name: repo-only
      run: "echo repo-only"
profiles:
  - name: work
    env:
      A: "repo"
  - name: client
    env:
      C: "1"
`)

	cfg := probeConfigOK(t, env)
	wantTemplates := []config.Template{
		{Name: "shared", Run: "echo repo"},
		{Name: "global-only", Run: "echo global-only"},
		{Name: "repo-only", Run: "echo repo-only"},
	}
	if !reflect.DeepEqual(cfg.Setup.Templates, wantTemplates) {
		t.Fatalf("Templates = %#v, want %#v", cfg.Setup.Templates, wantTemplates)
	}
	wantProfiles := []config.Profile{
		{Name: "work", Env: map[string]string{"A": "repo"}},
		{Name: "personal", Env: map[string]string{"B": "1"}},
		{Name: "client", Env: map[string]string{"C": "1"}},
	}
	if !reflect.DeepEqual(cfg.Profiles, wantProfiles) {
		t.Fatalf("Profiles = %#v, want %#v", cfg.Profiles, wantProfiles)
	}
}

// A misspelled key stops the command with an error naming the key and its
// line, never a setting that silently does nothing.
func TestAppConfigRejectsUnknownKeyWithItsLine(t *testing.T) {
	env, configHome := appEnv(t)
	writeUserConfig(t, configHome, "worktree_types: [feature]\nworktree_typos: [fix]\n")

	stderr := probeConfigFails(t, env)
	for _, want := range []string{"worktree_typos", "config.yaml:2"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to mention %q", stderr, want)
		}
	}
}

// How gt updates itself is the user's choice: a managed root's .gt.yaml that
// sets auto_update stops the command, rather than being obeyed or ignored.
func TestAppConfigRefusesAutoUpdateFromRepoOverlay(t *testing.T) {
	env, configHome := appEnv(t)
	writeUserConfig(t, configHome, "worktree_types: [feature]\n")
	inManagedRoot(t, env, "worktree_types: [feature]\nauto_update:\n  enabled: false\n")

	stderr := probeConfigFails(t, env)
	for _, want := range []string{"auto_update", "not settable in .gt.yaml", ".gt.yaml:3"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to mention %q", stderr, want)
		}
	}
}

// A malformed profile stops the command with a message naming the profile,
// rather than surfacing later as an .envrc missing a variable.
func TestAppConfigRejectsMalformedProfiles(t *testing.T) {
	env, configHome := appEnv(t)
	writeUserConfig(t, configHome, "worktree_types: [feature]\nprofiles:\n  - name: work\n")

	stderr := probeConfigFails(t, env)
	if !strings.Contains(stderr, `profile "work"`) {
		t.Fatalf("stderr = %q, want the profile name", stderr)
	}
}

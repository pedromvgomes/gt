package tests

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/config"
)

func TestLoadBootstrapsGlobalConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.WorktreeTypes, []string{"feature", "fix", "chore"}) {
		t.Fatalf("WorktreeTypes = %#v", cfg.WorktreeTypes)
	}
	if _, err := os.Stat(filepath.Join(configHome, "gt", "config.yaml")); err != nil {
		t.Fatalf("global config was not created: %v", err)
	}
}

func TestLoadMergesRepoConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gt.yaml"), []byte(`worktree_types:
  - hotfix
ssh:
  host_aliases:
    github.com: github-personal
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.WorktreeTypes, []string{"hotfix"}) {
		t.Fatalf("WorktreeTypes = %#v", cfg.WorktreeTypes)
	}
	if cfg.SSH.HostAliases["github.com"] != "github-personal" {
		t.Fatalf("HostAliases = %#v", cfg.SSH.HostAliases)
	}
}

func TestMergeKeepsBaseForEmptyOverride(t *testing.T) {
	base := config.Default()
	override := config.Config{}

	got := config.Merge(base, override)
	if !reflect.DeepEqual(got.WorktreeTypes, base.WorktreeTypes) {
		t.Fatalf("WorktreeTypes = %#v, want %#v", got.WorktreeTypes, base.WorktreeTypes)
	}
	if got.SSH.HostAliases == nil {
		t.Fatal("HostAliases is nil")
	}
}

func TestValidateRejectsReservedTypes(t *testing.T) {
	tests := []config.Config{
		{WorktreeTypes: nil},
		{WorktreeTypes: []string{"scratch"}},
		{WorktreeTypes: []string{"main"}},
		{WorktreeTypes: []string{"feature", "feature"}},
	}
	for _, cfg := range tests {
		t.Run(stringsForName(cfg.WorktreeTypes), func(t *testing.T) {
			if err := config.Validate(cfg); err == nil {
				t.Fatalf("Validate(%#v) succeeded, want error", cfg)
			}
		})
	}
}

func stringsForName(values []string) string {
	if len(values) == 0 {
		return "empty"
	}
	return values[0]
}

func TestLoadParsesSetupTemplates(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "gt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "gt", "config.yaml"), []byte(`worktree_types: [feature]
setup:
  templates:
    - name: agentic-toolkit
      match: ["github.com:pedromvgomes/*"]
      run: |
        echo hi
    - name: golang-extras
      match: ["*"]
      script: /path/to/script.sh
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Setup.Templates) != 2 {
		t.Fatalf("len(Templates) = %d, want 2", len(cfg.Setup.Templates))
	}
	if cfg.Setup.Templates[0].Name != "agentic-toolkit" {
		t.Fatalf("Templates[0].Name = %q", cfg.Setup.Templates[0].Name)
	}
	if cfg.Setup.Templates[1].Script != "/path/to/script.sh" {
		t.Fatalf("Templates[1].Script = %q", cfg.Setup.Templates[1].Script)
	}
}

func TestValidateSetupRejectsBadTemplates(t *testing.T) {
	cases := map[string]config.Setup{
		"missing-name": {Templates: []config.Template{{Run: "echo"}}},
		"both-run-and-script": {Templates: []config.Template{
			{Name: "x", Run: "echo", Script: "/a"},
		}},
		"neither-run-nor-script": {Templates: []config.Template{{Name: "x"}}},
		"duplicate-names": {Templates: []config.Template{
			{Name: "x", Run: "a"}, {Name: "x", Run: "b"},
		}},
		"empty-match-pattern": {Templates: []config.Template{
			{Name: "x", Match: []string{""}, Run: "a"},
		}},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if err := config.ValidateSetup(s); err == nil {
				t.Fatalf("ValidateSetup(%#v) succeeded, want error", s)
			}
		})
	}
}

func TestValidateSetupAcceptsValid(t *testing.T) {
	s := config.Setup{Templates: []config.Template{
		{Name: "a", Match: []string{"*"}, Run: "echo hi"},
		{Name: "b", Script: "/path/to/script.sh"},
	}}
	if err := config.ValidateSetup(s); err != nil {
		t.Fatalf("ValidateSetup() error = %v", err)
	}
}

func TestLoadParsesUserAliases(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "gt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "gt", "config.yaml"), []byte(`worktree_types: [feature]
ssh:
  host_aliases:
    github.com: github-personal
  user_aliases:
    pedromvgomes:
      github.com: github-personal
    pedro-work:
      github.com: github-work
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.SSH.UserAliases["pedromvgomes"]["github.com"]; got != "github-personal" {
		t.Fatalf("UserAliases[pedromvgomes][github.com] = %q", got)
	}
	if got := cfg.SSH.UserAliases["pedro-work"]["github.com"]; got != "github-work" {
		t.Fatalf("UserAliases[pedro-work][github.com] = %q", got)
	}
}

func TestMergePrefersOverrideUserAliases(t *testing.T) {
	base := config.Default()
	base.SSH.UserAliases = map[string]map[string]string{
		"alice": {"github.com": "github-alice"},
	}
	override := config.Config{SSH: config.SSH{UserAliases: map[string]map[string]string{
		"bob": {"github.com": "github-bob"},
	}}}
	got := config.Merge(base, override)
	if _, ok := got.SSH.UserAliases["alice"]; ok {
		t.Fatal("override did not replace user_aliases")
	}
	if got.SSH.UserAliases["bob"]["github.com"] != "github-bob" {
		t.Fatalf("UserAliases[bob][github.com] = %q", got.SSH.UserAliases["bob"]["github.com"])
	}
}

func TestValidateSSHRejectsEmptyEntries(t *testing.T) {
	cases := map[string]config.SSH{
		"empty-host-alias":   {HostAliases: map[string]string{"github.com": ""}},
		"empty-user":         {UserAliases: map[string]map[string]string{"": {"github.com": "x"}}},
		"empty-user-host":    {UserAliases: map[string]map[string]string{"u": {"": "x"}}},
		"empty-user-alias":   {UserAliases: map[string]map[string]string{"u": {"github.com": ""}}},
		"user-with-no-hosts": {UserAliases: map[string]map[string]string{"u": {}}},
	}
	for name, ssh := range cases {
		t.Run(name, func(t *testing.T) {
			if err := config.ValidateSSH(ssh); err == nil {
				t.Fatalf("ValidateSSH(%#v) succeeded, want error", ssh)
			}
		})
	}
}

func TestLoadMergesRepoSetupTemplates(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "gt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "gt", "config.yaml"), []byte(`worktree_types: [feature]
setup:
  templates:
    - name: global-only
      match: ["*"]
      run: "echo global"
    - name: shared
      match: ["*"]
      run: "echo global-shared"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The per-repo config overrides "shared" by name and adds "repo-only".
	if err := os.WriteFile(filepath.Join(root, ".gt.yaml"), []byte(`setup:
  templates:
    - name: shared
      match: ["*"]
      run: "echo repo-shared"
    - name: repo-only
      match: ["*"]
      run: "echo repo"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := make(map[string]string, len(cfg.Setup.Templates))
	var order []string
	for _, tpl := range cfg.Setup.Templates {
		got[tpl.Name] = tpl.Run
		order = append(order, tpl.Name)
	}
	if !reflect.DeepEqual(order, []string{"global-only", "shared", "repo-only"}) {
		t.Fatalf("template order = %#v", order)
	}
	if got["shared"] != "echo repo-shared" {
		t.Fatalf("per-repo override did not win: shared = %q", got["shared"])
	}
	if got["repo-only"] != "echo repo" {
		t.Fatalf("per-repo template missing: repo-only = %q", got["repo-only"])
	}
}

func TestValidateProfilesRejectsBadShapes(t *testing.T) {
	cases := map[string][]config.Profile{
		"missing-name":       {{Env: map[string]string{"A": "1"}}},
		"bad-name":           {{Name: "work profile", Env: map[string]string{"A": "1"}}},
		"reserved-name":      {{Name: "none", Env: map[string]string{"A": "1"}}},
		"duplicate-names":    {{Name: "w", Env: map[string]string{"A": "1"}}, {Name: "w", Env: map[string]string{"B": "2"}}},
		"empty-env":          {{Name: "w"}},
		"empty-match":        {{Name: "w", Match: []string{""}, Env: map[string]string{"A": "1"}}},
		"bad-env-name":       {{Name: "w", Env: map[string]string{"A-B": "1"}}},
		"leading-digit-name": {{Name: "w", Env: map[string]string{"1A": "1"}}},
		"newline-in-value":   {{Name: "w", Env: map[string]string{"A": "one\ntwo"}}},
	}
	for name, profiles := range cases {
		t.Run(name, func(t *testing.T) {
			if err := config.ValidateProfiles(profiles); err == nil {
				t.Fatalf("ValidateProfiles(%#v) succeeded, want error", profiles)
			}
		})
	}
}

func TestValidateProfilesAcceptsValid(t *testing.T) {
	profiles := []config.Profile{
		{Name: "work", Match: []string{"github.com:acme/*"}, Env: map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude"}},
		{Name: "personal.2", Env: map[string]string{"_X1": "anything at all"}},
	}
	if err := config.ValidateProfiles(profiles); err != nil {
		t.Fatalf("ValidateProfiles() error = %v", err)
	}
}

func TestLoadParsesProfiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "gt", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `worktree_types: [feature]
profiles:
  - name: work
    match: ["github.com:acme/*"]
    env:
      CLAUDE_CONFIG_DIR: ~/.claude
  - name: personal
    env:
      CODEX_HOME: ~/.codex
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
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

// A malformed profile must fail the load with a message naming the profile,
// not surface later as an .envrc missing a variable.
func TestLoadRejectsMalformedProfiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "gt", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "worktree_types: [feature]\nprofiles:\n  - name: work\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(t.TempDir())
	if err == nil {
		t.Fatal("Load() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "work") {
		t.Fatalf("Load() error = %v, want the profile name", err)
	}
}

// Profiles layer like setup templates: a same-named profile is replaced in
// place, keeping its position, because selection is first-match-wins.
func TestMergeProfilesReplacesInPlaceAndAppends(t *testing.T) {
	base := []config.Profile{
		{Name: "work", Env: map[string]string{"A": "1"}},
		{Name: "personal", Env: map[string]string{"B": "1"}},
	}
	override := []config.Profile{
		{Name: "personal", Env: map[string]string{"B": "2"}},
		{Name: "client", Env: map[string]string{"C": "3"}},
	}
	got := config.MergeProfiles(base, override)
	want := []config.Profile{
		{Name: "work", Env: map[string]string{"A": "1"}},
		{Name: "personal", Env: map[string]string{"B": "2"}},
		{Name: "client", Env: map[string]string{"C": "3"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeProfiles() = %#v, want %#v", got, want)
	}
}

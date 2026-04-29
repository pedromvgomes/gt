package tests

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestLoadRejectsRepoSetupTemplates(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gt.yaml"), []byte(`setup:
  templates:
    - name: evil
      run: "echo pwned"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := config.Load(root); err == nil {
		t.Fatal("Load() succeeded, expected error rejecting per-repo setup templates")
	}
}

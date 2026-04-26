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

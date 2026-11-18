package tests

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pedromvgomes/gt/internal/config"
)

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

func managedRoot(t *testing.T, overlay string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	if overlay != "" {
		if err := os.WriteFile(filepath.Join(root, ".gt.yaml"), []byte(overlay), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestOverlayPathFindsManagedRootFromNestedDir(t *testing.T) {
	root := managedRoot(t, "")
	nested := filepath.Join(root, "feature", "some-work", "internal", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	got, err := config.OverlayPath(os.LookupEnv)
	if err != nil {
		t.Fatalf("OverlayPath() error = %v", err)
	}
	// The working directory comes back with symlinks resolved, so compare the
	// resolved root rather than the path t.TempDir returned.
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(wantRoot, ".gt.yaml"); got != want {
		t.Fatalf("OverlayPath() = %q, want %q", got, want)
	}
}

func TestOverlayPathIsEmptyOutsideManagedRoot(t *testing.T) {
	t.Chdir(t.TempDir())

	got, err := config.OverlayPath(os.LookupEnv)
	if err != nil {
		t.Fatalf("OverlayPath() error = %v", err)
	}
	if got != "" {
		t.Fatalf("OverlayPath() = %q, want none", got)
	}
}

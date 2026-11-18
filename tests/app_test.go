package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/app"
	vcli "go.vipengele.org/cli/core"
	"go.vipengele.org/cli/spec"
	"go.vipengele.org/cli/spec/paths"
	specversion "go.vipengele.org/cli/spec/version"
)

type appRun struct {
	code   int
	stdout string
	stderr string
}

// appEnv is an environment with its own HOME and XDG_CONFIG_HOME and nothing
// else, so no GT_* variable or real config file of the machine running the
// tests reaches the App. The process also moves into a fresh directory, which
// is where the .gt.yaml overlay is looked for.
func appEnv(t *testing.T) (env *spec.Env, configHome string) {
	t.Helper()
	home := t.TempDir()
	configHome = filepath.Join(home, "xdg-config")
	vars := map[string]string{"HOME": home, "XDG_CONFIG_HOME": configHome}
	work := t.TempDir()
	t.Chdir(work)
	return &spec.Env{
		WorkDir: work,
		LookupEnv: func(key string) (string, bool) {
			v, ok := vars[key]
			return v, ok
		},
	}, configHome
}

func runApp(t *testing.T, a *vcli.App, env *spec.Env, args ...string) appRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	run := *env
	run.Stdout = &stdout
	run.Stderr = &stderr
	code := a.ExecuteArgs(&run, args)
	return appRun{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func devApp() *vcli.App {
	return app.New(app.BuildInfo{Version: "dev"}, app.Options{})
}

func TestVersionGainsTheLeadingVOnlyForSemver(t *testing.T) {
	cases := map[string]string{
		"1.2.3":        "v1.2.3",
		"1.2.3-rc.1":   "v1.2.3-rc.1",
		"v1.2.3":       "v1.2.3",
		"dev":          "dev",
		"":             "",
		"not-a-semver": "not-a-semver",
	}
	for stamped, want := range cases {
		if got := app.Version(stamped); got != want {
			t.Errorf("Version(%q) = %q, want %q", stamped, got, want)
		}
	}
	if !specversion.IsRelease(app.Version("1.2.3")) {
		t.Errorf("IsRelease(Version(%q)) = false, want true", "1.2.3")
	}
	if specversion.IsRelease(app.Version("dev")) {
		t.Errorf("IsRelease(Version(%q)) = true, want false", "dev")
	}
}

// A release build is stamped "1.2.3"; the libraries only treat "v1.2.3" as a
// release, so the App must be handed the normalised form.
func TestVersionCommandPrintsTheNormalisedRelease(t *testing.T) {
	env, _ := appEnv(t)
	a := app.New(app.BuildInfo{Version: "1.2.3", Commit: "0123456789abcdef", Date: "2026-11-17T00:00:00Z"}, app.Options{})

	got := runApp(t, a, env, "version")
	if got.code != vcli.ExitOK {
		t.Fatalf("version exit = %d, stderr = %q", got.code, got.stderr)
	}
	if want := "v1.2.3 build 0123456789ab\n"; got.stdout != want {
		t.Fatalf("version = %q, want %q", got.stdout, want)
	}

	got = runApp(t, a, env, "version", "--json")
	var info struct {
		Version string `json:"version"`
		Dev     bool   `json:"dev"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &info); err != nil {
		t.Fatalf("version --json: %v\n%s", err, got.stdout)
	}
	if info.Version != "v1.2.3" || info.Dev {
		t.Fatalf("version --json = %+v, want v1.2.3 and not dev", info)
	}
}

func TestVersionCommandKeepsDev(t *testing.T) {
	env, _ := appEnv(t)
	got := runApp(t, devApp(), env, "version", "--json")
	var info struct {
		Version string `json:"version"`
		Dev     bool   `json:"dev"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &info); err != nil {
		t.Fatalf("version --json: %v\n%s", err, got.stdout)
	}
	if info.Version != "dev" || !info.Dev {
		t.Fatalf("version --json = %+v, want dev", info)
	}
}

func TestConfigPathIsUnderXDGConfigHome(t *testing.T) {
	env, configHome := appEnv(t)
	got := runApp(t, devApp(), env, "config", "path")
	if got.code != vcli.ExitOK {
		t.Fatalf("config path exit = %d, stderr = %q", got.code, got.stderr)
	}
	if want := filepath.Join(configHome, "gt", "config.yaml"); strings.TrimSpace(got.stdout) != want {
		t.Fatalf("config path = %q, want %q", strings.TrimSpace(got.stdout), want)
	}
}

// macOS keeps gt's configuration in ~/.config/gt, not ~/Library.
func TestPathsLayoutKeepsXDGOnDarwin(t *testing.T) {
	home := t.TempDir()
	dirs, err := paths.Resolve(app.Identity(), paths.Options{
		GOOS:        "darwin",
		Layout:      app.PathsLayout,
		LookupEnv:   func(string) (string, bool) { return "", false },
		UserHomeDir: func() (string, error) { return home, nil },
	}, paths.Config)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := filepath.Join(home, ".config", "gt"); dirs.Config() != want {
		t.Fatalf("Config() = %q, want %q", dirs.Config(), want)
	}
}

func TestCommandsRunUnderThePathsLayout(t *testing.T) {
	env, _ := appEnv(t)
	a := devApp()
	var seen paths.Layout = -1
	a.Add(&spec.Command{
		Name:    "probe",
		Summary: "record the layout the App runs commands under",
		Run: func(ctx context.Context, _ *spec.Invocation) error {
			seen = vcli.PathsLayoutFrom(ctx)
			return nil
		},
	})
	if got := runApp(t, a, env, "probe"); got.code != vcli.ExitOK {
		t.Fatalf("probe exit = %d, stderr = %q", got.code, got.stderr)
	}
	if seen != app.PathsLayout {
		t.Fatalf("PathsLayoutFrom() = %v, want %v", seen, app.PathsLayout)
	}
}

func TestUnknownFlagIsAUsageError(t *testing.T) {
	env, _ := appEnv(t)
	got := runApp(t, devApp(), env, "wt", "list", "--no-such-flag")
	if got.code != vcli.ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "no-such-flag") {
		t.Fatalf("stderr = %q, want it to name the flag", got.stderr)
	}
}

func TestWorktreeIsAnAliasOfWt(t *testing.T) {
	env, _ := appEnv(t)
	for _, args := range [][]string{{"worktree", "--help"}, {"worktree", "remove", "--help"}, {"wt", "delete", "--help"}} {
		got := runApp(t, devApp(), env, args...)
		if got.code != vcli.ExitOK {
			t.Fatalf("%v exit = %d, stderr = %q", args, got.code, got.stderr)
		}
		want := "gt wt"
		if len(args) == 3 {
			want = "gt wt rm"
		}
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("%v help does not describe %q:\n%s", args, want, got.stdout)
		}
	}
}

func TestExclusiveFlagsAreAUsageError(t *testing.T) {
	cases := []struct {
		args  []string
		flags [2]string
	}{
		{[]string{"clone", "--ssh", "--no-ssh", "https://github.com/o/r"}, [2]string{"--ssh", "--no-ssh"}},
		{[]string{"clone", "--setup", "x", "--no-setup", "https://github.com/o/r"}, [2]string{"--setup", "--no-setup"}},
		{[]string{"clone", "--profile", "x", "--no-profile", "https://github.com/o/r"}, [2]string{"--profile", "--no-profile"}},
		{[]string{"setup", "--setup", "x", "--no-setup"}, [2]string{"--setup", "--no-setup"}},
		{[]string{"set-auth", "--profile", "x", "--no-profile"}, [2]string{"--profile", "--no-profile"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args[:2], " "), func(t *testing.T) {
			env, _ := appEnv(t)
			got := runApp(t, devApp(), env, tc.args...)
			if got.code != vcli.ExitUsage {
				t.Fatalf("exit = %d, want %d; stderr = %q", got.code, vcli.ExitUsage, got.stderr)
			}
			// Each --no-x contains --x, so --x is looked for with --no-x removed.
			negated, plain := tc.flags[1], tc.flags[0]
			if !strings.Contains(got.stderr, negated) {
				t.Fatalf("stderr = %q, want it to name %s", got.stderr, negated)
			}
			if !strings.Contains(strings.ReplaceAll(got.stderr, negated, ""), plain) {
				t.Fatalf("stderr = %q, want it to name %s", got.stderr, plain)
			}
		})
	}
}

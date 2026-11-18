package tests

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pedromvgomes/gt/internal/app"
	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/selfupdate"
	vcli "go.vipengele.org/cli/core"
	"go.vipengele.org/cli/spec"
	"go.vipengele.org/update"
	"go.vipengele.org/update/updatecmd"
)

// fakeGitHub answers the two requests a stable release lookup makes for
// pedromvgomes/gt: the latest-release web page, which redirects to the latest
// tag, and the REST API's latest release, which it falls back to. Every
// request to github.com or api.github.com reaches it through client.
type fakeGitHub struct {
	t   *testing.T
	srv *httptest.Server

	// latest is the tag served; status, when set, is the status of every
	// answer instead; gate, when set, holds every answer until it is closed
	// or the request is abandoned, and arrived is closed once the first held
	// request has reached the server.
	latest  string
	status  int
	gate    chan struct{}
	arrived chan struct{}
	once    sync.Once

	mu       sync.Mutex
	requests int
	auth     []string
}

func newFakeGitHub(t *testing.T, latest string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{t: t, latest: latest}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	if a := r.Header.Get("Authorization"); a != "" {
		f.auth = append(f.auth, a)
	}
	gate := f.gate
	f.mu.Unlock()

	if gate != nil {
		f.once.Do(func() { close(f.arrived) })
		select {
		case <-gate:
		case <-r.Context().Done():
			panic(http.ErrAbortHandler)
		}
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch r.URL.Path {
	case "/pedromvgomes/gt/releases/latest":
		w.Header().Set("Location", "/pedromvgomes/gt/releases/tag/"+f.latest)
		w.WriteHeader(http.StatusFound)
	case "/repos/pedromvgomes/gt/releases/latest":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": f.latest, "assets": []any{}})
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) authorizations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auth...)
}

func (f *fakeGitHub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// client sends every request to the fake server, whatever host it names.
func (f *fakeGitHub) client() *http.Client {
	target, err := url.Parse(f.srv.URL)
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		r.Host = target.Host
		return f.srv.Client().Transport.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

// hold makes every answer wait until release is called.
func (f *fakeGitHub) hold() (release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = make(chan struct{})
	f.arrived = make(chan struct{})
	gate := f.gate
	return func() { close(gate) }
}

// updateSeams is the environment of one App's update check: a terminal on
// stderr, the clock at now, the state in stateDir, the network through gh
// and an environment holding env alone, so CI or GT_NO_UPDATE_CHECK on the
// machine running the tests cannot mask a check.
type updateSeams struct {
	gh       *fakeGitHub
	stateDir string
	now      time.Time
	env      map[string]string
	wait     time.Duration
	exe      string
	// during, when set, runs inside the "noop" command.
	during func()
}

func newUpdateSeams(t *testing.T, gh *fakeGitHub) *updateSeams {
	t.Helper()
	return &updateSeams{
		gh:       gh,
		stateDir: t.TempDir(),
		now:      time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		env:      map[string]string{},
	}
}

func (s *updateSeams) options() app.Options {
	return app.Options{Update: func(c *update.Config) {
		c.HTTPClient = s.gh.client()
		c.StateDir = s.stateDir
		c.Now = func() time.Time { return s.now }
		c.IsTerminal = func() bool { return true }
		c.Getenv = func(k string) string { return s.env[k] }
		c.CheckWait = s.wait
		if s.exe != "" {
			exe := s.exe
			c.Executable = func() (string, error) { return exe, nil }
			c.EvalSymlinks = func(p string) (string, error) { return p, nil }
		}
	}}
}

// releaseApp is the App of release 3.0.0 under s, with a "noop" command that
// does nothing but s.during.
func releaseApp(s *updateSeams) *vcli.App {
	a := app.New(app.BuildInfo{Version: "3.0.0"}, s.options())
	during := s.during
	a.Add(&spec.Command{
		Name:    "noop",
		Summary: "do nothing",
		Run: func(context.Context, *spec.Invocation) error {
			if during != nil {
				during()
			}
			return nil
		},
	})
	return a
}

// savedState reads the update state the check saved, and false when it saved
// none.
func savedState(t *testing.T, dir string) (update.State, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "update-state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return update.State{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var st update.State
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("update-state.json: %v\n%s", err, raw)
	}
	return st, true
}

// A check still waiting on the network when the command ends is cancelled,
// and a cancelled check is not an attempt: nothing is recorded, so the next
// run checks again instead of being throttled for a whole interval.
func TestUpdateCheckCancelledByTheCommandEndingIsNotRecorded(t *testing.T) {
	env, _ := appEnv(t)
	gh := newFakeGitHub(t, "v3.1.0")
	release := gh.hold()
	s := newUpdateSeams(t, gh)
	s.wait = 20 * time.Millisecond
	// The command ends only once the check is waiting on the server, so the
	// check is in flight, not yet unstarted, when the run cancels it.
	s.during = func() {
		select {
		case <-gh.arrived:
		case <-time.After(10 * time.Second):
			t.Error("the update check never reached the server")
		}
	}

	got := runApp(t, releaseApp(s), env, "noop")
	if got.code != vcli.ExitOK {
		t.Fatalf("noop exit = %d, stderr = %q", got.code, got.stderr)
	}
	if gh.count() != 1 {
		t.Fatalf("requests = %d, want the one the cancelled check made", gh.count())
	}
	if st, ok := savedState(t, s.stateDir); ok && !st.LastAttempt.IsZero() {
		t.Fatalf("cancelled check recorded an attempt: %+v", st)
	}
	if strings.Contains(got.stderr, "available") {
		t.Fatalf("cancelled check printed a notice: %q", got.stderr)
	}

	release()
	s.during = nil
	s.wait = 2 * time.Second
	got = runApp(t, releaseApp(s), env, "noop")
	if got.code != vcli.ExitOK {
		t.Fatalf("second noop exit = %d, stderr = %q", got.code, got.stderr)
	}
	if gh.count() != 2 {
		t.Fatalf("requests = %d, want the next run to check again", gh.count())
	}
	st, ok := savedState(t, s.stateDir)
	if !ok || !st.LastAttempt.Equal(s.now) || st.LatestVersion != "v3.1.0" {
		t.Fatalf("state after the completed check = %+v (saved %v), want an attempt at %v finding v3.1.0", st, ok, s.now)
	}
}

// A check the server fails is an attempt: it is recorded and throttles the
// next check until check_interval has passed. It is also unauthenticated, so a
// GH_TOKEN in the environment never reaches the API.
func TestUpdateCheckFailureIsRecordedAndThrottles(t *testing.T) {
	env, _ := appEnv(t)
	gh := newFakeGitHub(t, "v3.1.0")
	gh.status = http.StatusInternalServerError
	s := newUpdateSeams(t, gh)
	s.wait = 2 * time.Second
	s.env["GH_TOKEN"] = "per-repository-token"

	if got := runApp(t, releaseApp(s), env, "noop"); got.code != vcli.ExitOK {
		t.Fatalf("noop exit = %d, stderr = %q", got.code, got.stderr)
	}
	first := gh.count()
	if first == 0 {
		t.Fatal("no request reached the server")
	}
	st, ok := savedState(t, s.stateDir)
	if !ok || !st.LastAttempt.Equal(s.now) {
		t.Fatalf("state after the failed check = %+v (saved %v), want an attempt at %v", st, ok, s.now)
	}
	if auth := gh.authorizations(); len(auth) != 0 {
		t.Fatalf("Authorization sent: %q", auth)
	}

	s.now = s.now.Add(time.Hour)
	if got := runApp(t, releaseApp(s), env, "noop"); got.code != vcli.ExitOK {
		t.Fatalf("second noop exit = %d, stderr = %q", got.code, got.stderr)
	}
	if gh.count() != first {
		t.Fatalf("requests = %d, want %d: a run within the interval checks again", gh.count(), first)
	}
}

func TestUpdateCheckAnnouncesANewerReleaseOnStderr(t *testing.T) {
	env, _ := appEnv(t)
	gh := newFakeGitHub(t, "v3.1.0")
	s := newUpdateSeams(t, gh)
	s.wait = 2 * time.Second

	got := runApp(t, releaseApp(s), env, "noop")
	if got.code != vcli.ExitOK {
		t.Fatalf("noop exit = %d, stderr = %q", got.code, got.stderr)
	}
	if want := "gt v3.1.0 is available (you're on v3.0.0)"; !strings.Contains(got.stderr, want) {
		t.Fatalf("stderr = %q, want it to contain %q", got.stderr, want)
	}
	if strings.Contains(got.stdout, "available") {
		t.Fatalf("notice written to stdout: %q", got.stdout)
	}
}

func TestUpdateCheckIsSuppressed(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		config string
		args   []string
	}{
		{name: "GT_NO_UPDATE_CHECK", env: map[string]string{"GT_NO_UPDATE_CHECK": "1"}, args: []string{"noop"}},
		{name: "CI", env: map[string]string{"CI": "1"}, args: []string{"noop"}},
		{name: "auto_update disabled", config: "auto_update:\n  enabled: false\n  check_interval: 30m\n", args: []string{"noop"}},
		{name: "command without configuration", args: []string{"version"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, configHome := appEnv(t)
			writeUserConfig(t, configHome, tc.config)
			gh := newFakeGitHub(t, "v3.1.0")
			s := newUpdateSeams(t, gh)
			s.wait = 2 * time.Second
			s.env = tc.env

			got := runApp(t, releaseApp(s), env, tc.args...)
			if got.code != vcli.ExitOK {
				t.Fatalf("%v exit = %d, stderr = %q", tc.args, got.code, got.stderr)
			}
			if gh.count() != 0 {
				t.Fatalf("requests = %d, want no check", gh.count())
			}
			if strings.Contains(got.stderr, "available") {
				t.Fatalf("stderr = %q, want no notice", got.stderr)
			}
		})
	}
}

// auto_update governs the background check alone: with it disabled, an
// interval under the hour the updater requires is accepted and ignored, and
// "gt update" still reaches the releases.
func TestUpdateCommandIgnoresDisabledAutoUpdate(t *testing.T) {
	env, configHome := appEnv(t)
	writeUserConfig(t, configHome, "auto_update:\n  enabled: false\n  check_interval: 30m\n")
	gh := newFakeGitHub(t, "v3.1.0")
	s := newUpdateSeams(t, gh)
	s.exe = filepath.Join(t.TempDir(), "gt")

	got := runApp(t, releaseApp(s), env, "update", "--check")
	if got.code != updatecmd.ExitUpdateAvailable {
		t.Fatalf("update --check exit = %d, want %d; stderr = %q", got.code, updatecmd.ExitUpdateAvailable, got.stderr)
	}
	if want := "gt v3.1.0 is available (current v3.0.0)"; !strings.Contains(got.stdout, want) {
		t.Fatalf("stdout = %q, want it to contain %q", got.stdout, want)
	}
}

func TestUpdateRefusesAHomebrewInstall(t *testing.T) {
	env, _ := appEnv(t)
	gh := newFakeGitHub(t, "v3.1.0")
	s := newUpdateSeams(t, gh)
	s.exe = "/opt/homebrew/bin/gt"

	got := runApp(t, releaseApp(s), env, "update", "--yes")
	if got.code != vcli.ExitError {
		t.Fatalf("update exit = %d, want %d; stderr = %q", got.code, vcli.ExitError, got.stderr)
	}
	if !strings.Contains(got.stderr, "brew upgrade gt") {
		t.Fatalf("stderr = %q, want it to name brew upgrade gt", got.stderr)
	}
	if gh.count() != 0 {
		t.Fatalf("requests = %d, want the refusal before any request", gh.count())
	}
}

func TestBaseIsValid(t *testing.T) {
	cfg := selfupdate.Base("3.0.0")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Base(3.0.0).Validate() = %v", err)
	}
	if cfg.Version != "v3.0.0" {
		t.Fatalf("Version = %q, want v3.0.0", cfg.Version)
	}
	if len(cfg.TrustedKeys) != 1 || cfg.AllowUnsigned {
		t.Fatalf("TrustedKeys = %q, AllowUnsigned = %v; want the embedded key alone", cfg.TrustedKeys, cfg.AllowUnsigned)
	}
	if cfg.Token == nil {
		t.Fatal("Token is nil, which reads GH_TOKEN from the environment")
	}
	if tok, err := cfg.Token(context.Background()); tok != "" || err != nil {
		t.Fatalf("Token() = %q, %v; want the unauthenticated empty token", tok, err)
	}
	if cfg.PathsLayout != app.PathsLayout {
		t.Fatalf("PathsLayout = %v, want app.PathsLayout %v", cfg.PathsLayout, app.PathsLayout)
	}
}

// macOS keeps gt's update state in ~/.local/state/gt, beside the
// configuration in ~/.config/gt, not under ~/Library.
func TestUpdateStateIsXDGOnDarwin(t *testing.T) {
	home := t.TempDir()
	cfg := selfupdate.Base("3.0.0")
	cfg.GOOS = "darwin"
	cfg.Getenv = func(string) string { return "" }
	cfg.UserHomeDir = func() (string, error) { return home, nil }
	r, err := cfg.Resolved()
	if err != nil {
		t.Fatalf("Resolved() error = %v", err)
	}
	if want := filepath.Join(home, ".local", "state", "gt"); r.StateDir != want {
		t.Fatalf("StateDir = %q, want %q", r.StateDir, want)
	}
}

func TestForRunAppliesAutoUpdate(t *testing.T) {
	base := selfupdate.Base("3.0.0")

	enabled := config.Default()
	enabled.AutoUpdate.Enabled = true
	enabled.AutoUpdate.CheckInterval = 6 * time.Hour
	run := selfupdate.ForRun(base, enabled)
	if run.DisableCheck || run.CheckInterval != 6*time.Hour {
		t.Fatalf("enabled: DisableCheck = %v, CheckInterval = %v; want false, 6h", run.DisableCheck, run.CheckInterval)
	}

	disabled := config.Default()
	disabled.AutoUpdate.Enabled = false
	disabled.AutoUpdate.CheckInterval = 30 * time.Minute
	run = selfupdate.ForRun(base, disabled)
	if !run.DisableCheck || run.CheckInterval != base.CheckInterval {
		t.Fatalf("disabled: DisableCheck = %v, CheckInterval = %v; want true and base's", run.DisableCheck, run.CheckInterval)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("disabled with a 30m interval: Validate() = %v", err)
	}
}

func writeUserConfig(t *testing.T, configHome, body string) {
	t.Helper()
	if body == "" {
		return
	}
	dir := filepath.Join(configHome, "gt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

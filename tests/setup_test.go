package tests

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/setup"
	"github.com/pedromvgomes/gt/internal/ui"
)

func TestParseRepoURL(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		wantOwner string
		wantName  string
	}{
		{name: "ssh-scp", url: "git@github.com:pedromvgomes/gt.git", wantOwner: "pedromvgomes", wantName: "gt"},
		{name: "ssh-scp-no-suffix", url: "git@github.com:pedromvgomes/gt", wantOwner: "pedromvgomes", wantName: "gt"},
		{name: "https", url: "https://github.com/pedromvgomes/gt.git", wantOwner: "pedromvgomes", wantName: "gt"},
		{name: "https-no-suffix", url: "https://github.com/pedromvgomes/gt", wantOwner: "pedromvgomes", wantName: "gt"},
		{name: "ssh-protocol", url: "ssh://git@github.com/pedromvgomes/gt.git", wantOwner: "pedromvgomes", wantName: "gt"},
		{name: "host-alias", url: "git@github-personal:pedromvgomes/gt.git", wantOwner: "pedromvgomes", wantName: "gt"},
		{name: "empty", url: "", wantOwner: "", wantName: ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			owner, name, err := setup.ParseRepoURL(tt.url)
			if err != nil {
				t.Fatalf("ParseRepoURL(%q) error = %v", tt.url, err)
			}
			if owner != tt.wantOwner || name != tt.wantName {
				t.Fatalf("ParseRepoURL(%q) = %q,%q want %q,%q", tt.url, owner, name, tt.wantOwner, tt.wantName)
			}
		})
	}
}

func TestFromBareCloneFillsContext(t *testing.T) {
	root := t.TempDir()
	c, err := setup.FromBareClone(root, "main", "git@github.com:pedromvgomes/gt.git")
	if err != nil {
		t.Fatalf("FromBareClone error = %v", err)
	}
	if c.Layout != setup.LayoutBare {
		t.Fatalf("Layout = %q", c.Layout)
	}
	wantWork := filepath.Join(root, "main")
	if c.WorkDir != wantWork {
		t.Fatalf("WorkDir = %q, want %q", c.WorkDir, wantWork)
	}
	if c.RepoOwner != "pedromvgomes" || c.RepoName != "gt" {
		t.Fatalf("Owner/Name = %q/%q", c.RepoOwner, c.RepoName)
	}
	vars := c.Vars()
	if vars["GT_LAYOUT"] != "bare" || vars["GT_REPO_NAME"] != "gt" {
		t.Fatalf("Vars = %#v", vars)
	}
}

func TestDetectFromCWDBare(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "feature", "x")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := &fakeGitRunner{responses: map[string]git.Result{
		"--git-dir=.bare symbolic-ref --short HEAD":      {Stdout: "trunk\n"},
		"--git-dir=.bare config --get remote.origin.url": {Stdout: "git@github.com:pedromvgomes/gt.git\n"},
	}}

	c, err := setup.DetectFromCWD(context.Background(), runner, nested)
	if err != nil {
		t.Fatalf("DetectFromCWD error = %v", err)
	}
	if c.Layout != setup.LayoutBare {
		t.Fatalf("Layout = %q", c.Layout)
	}
	if c.Root != root {
		t.Fatalf("Root = %q, want %q", c.Root, root)
	}
	if c.WorkDir != filepath.Join(root, "trunk") {
		t.Fatalf("WorkDir = %q", c.WorkDir)
	}
	if c.DefaultBranch != "trunk" {
		t.Fatalf("DefaultBranch = %q", c.DefaultBranch)
	}
	if c.RepoName != "gt" {
		t.Fatalf("RepoName = %q", c.RepoName)
	}
}

func TestDetectFromCWDPlain(t *testing.T) {
	root := t.TempDir()
	runner := &fakeGitRunner{responses: map[string]git.Result{
		"rev-parse --show-toplevel":                      {Stdout: root + "\n"},
		"symbolic-ref --short refs/remotes/origin/HEAD":  {Stdout: "origin/main\n"},
		"config --get remote.origin.url":                 {Stdout: "https://github.com/pedromvgomes/gt.git\n"},
	}}
	c, err := setup.DetectFromCWD(context.Background(), runner, root)
	if err != nil {
		t.Fatalf("DetectFromCWD error = %v", err)
	}
	if c.Layout != setup.LayoutPlain {
		t.Fatalf("Layout = %q", c.Layout)
	}
	if c.Root != root || c.WorkDir != root {
		t.Fatalf("Root/WorkDir = %q/%q", c.Root, c.WorkDir)
	}
	if c.DefaultBranch != "main" {
		t.Fatalf("DefaultBranch = %q", c.DefaultBranch)
	}
}

func TestDetectFromCWDNotAGitRepo(t *testing.T) {
	cwd := t.TempDir()
	runner := &fakeGitRunner{errs: map[string]error{
		"rev-parse --show-toplevel": errFakeNotARepo,
	}}
	if _, err := setup.DetectFromCWD(context.Background(), runner, cwd); err == nil {
		t.Fatal("DetectFromCWD succeeded outside a git repo, want error")
	}
}

var errFakeNotARepo = &fakeError{msg: "not a git repository"}

type fakeError struct{ msg string }

func (e *fakeError) Error() string { return e.msg }

func TestMatchURL(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		url     string
		want    bool
	}{
		{name: "star matches all", pattern: "*", url: "git@github.com:foo/bar.git", want: true},
		{name: "ssh prefix glob", pattern: "github.com:pedromvgomes/*", url: "git@github.com:pedromvgomes/gt.git", want: true},
		{name: "https prefix glob", pattern: "github.com/pedromvgomes/*", url: "https://github.com/pedromvgomes/gt.git", want: true},
		{name: "non-match owner", pattern: "github.com:pedromvgomes/*", url: "git@github.com:other/gt.git", want: false},
		{name: "literal match", pattern: "git@github.com:pedromvgomes/gt.git", url: "git@github.com:pedromvgomes/gt.git", want: true},
		{name: "question mark", pattern: "github.com:foo/ba?", url: "git@github.com:foo/bar", want: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := setup.MatchURL(tt.pattern, tt.url); got != tt.want {
				t.Fatalf("MatchURL(%q,%q) = %v, want %v", tt.pattern, tt.url, got, tt.want)
			}
		})
	}
}

func TestSelectNoSetup(t *testing.T) {
	s := config.Setup{Templates: []config.Template{{Name: "a", Match: []string{"*"}, Run: "echo"}}}
	got, err := setup.Select(s, "url", setup.SelectOptions{NoSetup: true})
	if err != nil || got != nil {
		t.Fatalf("Select(NoSetup) = %v, %v", got, err)
	}
}

func TestSelectExplicitOrderAndUnknown(t *testing.T) {
	s := config.Setup{Templates: []config.Template{
		{Name: "a", Match: []string{"*"}, Run: "echo a"},
		{Name: "b", Match: []string{"*"}, Run: "echo b"},
	}}
	got, err := setup.Select(s, "url", setup.SelectOptions{Names: []string{"b", "a"}})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if len(got) != 2 || got[0].Name != "b" || got[1].Name != "a" {
		t.Fatalf("Select() = %#v", got)
	}

	if _, err := setup.Select(s, "url", setup.SelectOptions{Names: []string{"missing"}}); err == nil {
		t.Fatal("expected error for unknown template name")
	}
}

func TestSelectMatchesAllInOrder(t *testing.T) {
	s := config.Setup{Templates: []config.Template{
		{Name: "specific", Match: []string{"github.com:pedromvgomes/*"}, Run: "echo"},
		{Name: "always", Match: []string{"*"}, Run: "echo"},
		{Name: "other", Match: []string{"gitlab.com/*"}, Run: "echo"},
		{Name: "no-match", Match: nil, Run: "echo"},
	}}
	got, err := setup.Select(s, "git@github.com:pedromvgomes/gt.git", setup.SelectOptions{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if len(got) != 2 || got[0].Name != "specific" || got[1].Name != "always" {
		t.Fatalf("Select() = %#v", got)
	}
}

func TestSelectEmptyMatchSkipsAuto(t *testing.T) {
	s := config.Setup{Templates: []config.Template{{Name: "manual", Run: "echo"}}}
	got, err := setup.Select(s, "any", setup.SelectOptions{})
	if err != nil || len(got) != 0 {
		t.Fatalf("Select() = %v, err = %v", got, err)
	}
}

func TestExecuteEmptyPlanIsNoop(t *testing.T) {
	out := &bytes.Buffer{}
	printer := ui.New(strings.NewReader(""), out, out, true, false)
	err := setup.Execute(context.Background(), printer, setup.Plan{}, setup.RunOptions{Yes: true})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "No setup templates apply") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestExecuteRunsTemplatesInOrder(t *testing.T) {
	dir := t.TempDir()
	out := &bytes.Buffer{}
	printer := ui.New(strings.NewReader(""), out, out, true, false)
	plan := setup.Plan{
		Templates: []config.Template{
			{Name: "first", Run: "echo first > " + filepath.Join(dir, "1.txt")},
			{Name: "second", Run: "echo second > " + filepath.Join(dir, "2.txt")},
		},
		Ctx: setup.Context{Root: dir, WorkDir: dir, Layout: setup.LayoutPlain, DefaultBranch: "main"},
	}
	err := setup.Execute(context.Background(), printer, plan, setup.RunOptions{Yes: true})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, fname := range []string{"1.txt", "2.txt"} {
		if _, err := os.Stat(filepath.Join(dir, fname)); err != nil {
			t.Fatalf("expected %s to be written: %v", fname, err)
		}
	}
	output := out.String()
	if !strings.Contains(output, "==> [1/2] first") || !strings.Contains(output, "==> [2/2] second") {
		t.Fatalf("output missing step headers: %q", output)
	}
}

func TestExecuteStopsOnFailure(t *testing.T) {
	dir := t.TempDir()
	out := &bytes.Buffer{}
	printer := ui.New(strings.NewReader(""), out, out, true, false)
	plan := setup.Plan{
		Templates: []config.Template{
			{Name: "ok", Run: "true"},
			{Name: "boom", Run: "exit 7"},
			{Name: "never", Run: "touch " + filepath.Join(dir, "never.txt")},
		},
		Ctx: setup.Context{Root: dir, WorkDir: dir, Layout: setup.LayoutPlain},
	}
	err := setup.Execute(context.Background(), printer, plan, setup.RunOptions{Yes: true})
	if err == nil {
		t.Fatal("Execute() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "--from boom") {
		t.Fatalf("error = %v, want it to mention boom and --from boom", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "never.txt")); err == nil {
		t.Fatal("third template ran after failure; expected stop")
	}
}

func TestExecuteFromSkipsEarlierTemplates(t *testing.T) {
	dir := t.TempDir()
	out := &bytes.Buffer{}
	printer := ui.New(strings.NewReader(""), out, out, true, false)
	plan := setup.Plan{
		Templates: []config.Template{
			{Name: "skip", Run: "touch " + filepath.Join(dir, "skip.txt")},
			{Name: "run-me", Run: "touch " + filepath.Join(dir, "ran.txt")},
		},
		Ctx: setup.Context{Root: dir, WorkDir: dir, Layout: setup.LayoutPlain},
	}
	err := setup.Execute(context.Background(), printer, plan, setup.RunOptions{Yes: true, From: "run-me"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skip.txt")); err == nil {
		t.Fatal("skipped template ran")
	}
	if _, err := os.Stat(filepath.Join(dir, "ran.txt")); err != nil {
		t.Fatalf("expected ran.txt to exist: %v", err)
	}
}

func TestExecuteDryRunDoesNotRun(t *testing.T) {
	dir := t.TempDir()
	out := &bytes.Buffer{}
	printer := ui.New(strings.NewReader(""), out, out, true, false)
	plan := setup.Plan{
		Templates: []config.Template{{Name: "x", Run: "touch " + filepath.Join(dir, "x.txt")}},
		Ctx:       setup.Context{Root: dir, WorkDir: dir, Layout: setup.LayoutPlain},
	}
	err := setup.Execute(context.Background(), printer, plan, setup.RunOptions{DryRun: true, Yes: true})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); err == nil {
		t.Fatal("dry-run executed the template")
	}
}

func TestExecuteExportsGTVarsToRunSteps(t *testing.T) {
	dir := t.TempDir()
	out := &bytes.Buffer{}
	printer := ui.New(strings.NewReader(""), out, out, true, false)
	plan := setup.Plan{
		Templates: []config.Template{
			{Name: "env", Run: `printf '%s\n' "$GT_REPO_NAME" > "$GT_WORKDIR/repo.txt"`},
		},
		Ctx: setup.Context{Root: dir, WorkDir: dir, Layout: setup.LayoutPlain, RepoName: "gt"},
	}
	if err := setup.Execute(context.Background(), printer, plan, setup.RunOptions{Yes: true}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "repo.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "gt" {
		t.Fatalf("repo.txt = %q, want gt", got)
	}
}

func TestExecutePromptDeclines(t *testing.T) {
	out := &bytes.Buffer{}
	printer := &ui.UI{In: strings.NewReader("n\n"), Out: out, Err: out, Interactive: true}
	plan := setup.Plan{
		Templates: []config.Template{{Name: "x", Run: "touch /tmp/should-not-exist.gt"}},
	}
	err := setup.Execute(context.Background(), printer, plan, setup.RunOptions{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Fatalf("output = %q", out.String())
	}
}

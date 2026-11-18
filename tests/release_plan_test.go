package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const planScript = "../.github/scripts/release/plan.sh"

func releaseRepo(t *testing.T, notes []string, tags []string) string {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	git("config", "commit.gpgsign", "false")
	git("config", "tag.gpgsign", "false")
	if err := os.MkdirAll(filepath.Join(root, "docs", "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Each tag is made at a commit holding the notes known so far, the way a
	// real release is.
	written := map[string]bool{}
	write := func(n string) {
		writeFile(t, root, "docs/releases/"+n+".md", "notes\n")
		written[n] = true
	}
	for _, tag := range tags {
		write(tag)
		git("add", "-A")
		git("commit", "-q", "-m", "release "+tag)
		git("tag", tag)
	}
	for _, n := range notes {
		if !written[n] {
			write(n)
		}
	}
	git("add", "-A")
	git("commit", "-q", "--allow-empty", "-m", "head")
	return root
}

func runPlan(t *testing.T, root string, tags, releases []string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	tf := filepath.Join(dir, "tags")
	rf := filepath.Join(dir, "releases")
	// ls-remote shape, peeled lines included.
	var tl []string
	for _, tag := range tags {
		tl = append(tl, "abc123\trefs/tags/"+tag, "def456\trefs/tags/"+tag+"^{}")
	}
	tl = append(tl, "abc123\trefs/tags/v2")
	if err := os.WriteFile(tf, []byte(strings.Join(tl, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rf, []byte(strings.Join(releases, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(planScript)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", abs, root, tf, rf).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestReleasePlan(t *testing.T) {
	cases := []struct {
		name     string
		notes    []string
		tags     []string
		releases []string
		want     string
		wantErr  string
	}{
		{
			name:  "untagged notes are a release",
			notes: []string{"v2.2.0"}, tags: []string{"v2.1.0", "v2.1.1"},
			releases: []string{"v2.1.0", "v2.1.1"},
			want:     `{"mode":"release","version":"v2.2.0"}`,
		},
		{
			name: "nothing pending and everything released is none",
			tags: []string{"v2.1.0"}, releases: []string{"v2.1.0"},
			want: `{"mode":"none","version":""}`,
		},
		{
			name: "latest tag without a release is finished",
			tags: []string{"v2.1.0", "v2.2.0"}, releases: []string{"v2.1.0"},
			want: `{"mode":"finish","version":"v2.2.0"}`,
		},
		{
			name: "an older tag without a release is left alone",
			tags: []string{"v2.0.0", "v2.1.0"}, releases: []string{"v2.1.0"},
			want: `{"mode":"none","version":""}`,
		},
		{
			name:  "two pending versions fail",
			notes: []string{"v2.2.0", "v2.3.0"}, tags: []string{"v2.1.0"},
			releases: []string{"v2.1.0"}, wantErr: "more than one release is pending",
		},
		{
			name:  "notes older than the latest tag fail",
			notes: []string{"v1.9.0"}, tags: []string{"v2.1.0"},
			releases: []string{"v2.1.0"}, wantErr: "not newer than the latest tag",
		},
		{
			name:  "the first release has no latest tag to compare with",
			notes: []string{"v0.1.0"}, want: `{"mode":"release","version":"v0.1.0"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := releaseRepo(t, tc.notes, tc.tags)
			got, err := runPlan(t, root, tc.tags, tc.releases)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(got, tc.wantErr) {
					t.Fatalf("plan = %q, err = %v, want error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("plan error = %v\n%s", err, got)
			}
			if got != tc.want {
				t.Errorf("plan = %s, want %s", got, tc.want)
			}
		})
	}
}

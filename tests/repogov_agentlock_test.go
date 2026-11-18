package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedromvgomes/gt/internal/repogov"
	"github.com/pedromvgomes/gt/internal/repospec"
)

func agentLockRepo(t *testing.T, trackLock bool, gitignore string) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writeFile(t, root, repogov.AgentManifestFile, "stacks: []\n")
	if gitignore != "" {
		writeFile(t, root, ".gitignore", gitignore)
	}
	if trackLock {
		writeFile(t, root, repogov.AgentLockFile, "version: 2\n")
		if out, err := exec.Command("git", "-C", root, "add", "-f", repogov.AgentLockFile).CombinedOutput(); err != nil {
			t.Fatalf("git add: %v\n%s", err, out)
		}
	}
	return root
}

func lockTracked(t *testing.T, root string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files", repogov.AgentLockFile).Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	return strings.TrimSpace(string(out)) != ""
}

func checkLock(t *testing.T, root string, spec repospec.Spec) repogov.AgentLockChange {
	t.Helper()
	if err := repogov.SaveSpec(root, spec); err != nil {
		t.Fatalf("SaveSpec() error = %v", err)
	}
	report, err := repogov.CheckSpec(spec, testOptions(root))
	if err != nil {
		t.Fatalf("CheckSpec() error = %v", err)
	}
	return report.AgentLock
}

func TestAgentLockDefaultsToIgnored(t *testing.T) {
	if got := repospec.Default().AgentLock; got != repospec.AgentLockIgnored {
		t.Errorf("default agent_lock = %q, want %q", got, repospec.AgentLockIgnored)
	}
}

func TestAgentLockRejectsUnknownValue(t *testing.T) {
	_, err := repospec.Parse([]byte("agent_lock: sometimes\n"), "x")
	if err == nil || !strings.Contains(err.Error(), "agent_lock") {
		t.Fatalf("Parse() error = %v, want agent_lock complaint", err)
	}
}

func TestAgentLockUntracksAndIgnoresATrackedLock(t *testing.T) {
	root := agentLockRepo(t, true, "")
	spec := repospec.Default()

	c := checkLock(t, root, spec)
	if !c.AddIgnore || !c.Untrack {
		t.Fatalf("change = %+v, want AddIgnore and Untrack", c)
	}
	if _, err := repogov.ApplyAgentLock(testOptions(root), c); err != nil {
		t.Fatalf("ApplyAgentLock() error = %v", err)
	}
	if lockTracked(t, root) {
		t.Error("lockfile is still tracked")
	}
	if _, err := os.Stat(filepath.Join(root, repogov.AgentLockFile)); err != nil {
		t.Errorf("lockfile left the working tree: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(got), "/"+repogov.AgentLockFile+"\n") {
		t.Errorf(".gitignore = %q, want anchored entry", got)
	}
	if again := checkLock(t, root, spec); again.Needed() {
		t.Errorf("second check = %+v, want converged", again)
	}
}

func TestAgentLockLeavesATrackedPolicyAlone(t *testing.T) {
	root := agentLockRepo(t, true, "bin/\n")
	spec := repospec.Default()
	spec.AgentLock = repospec.AgentLockTracked
	if c := checkLock(t, root, spec); c.Needed() {
		t.Errorf("change = %+v, want none", c)
	}
}

func TestAgentLockTrackedPolicyRemovesOnlyTheLockEntry(t *testing.T) {
	root := agentLockRepo(t, false, "bin/\n/"+repogov.AgentLockFile+"\n/.claude/\n")
	spec := repospec.Default()
	spec.AgentLock = repospec.AgentLockTracked

	c := checkLock(t, root, spec)
	if !c.RemoveIgnore {
		t.Fatalf("change = %+v, want RemoveIgnore", c)
	}
	if _, err := repogov.ApplyAgentLock(testOptions(root), c); err != nil {
		t.Fatalf("ApplyAgentLock() error = %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if string(got) != "bin/\n/.claude/\n" {
		t.Errorf(".gitignore = %q", got)
	}
}

func TestAgentLockIgnoresARepositoryWithoutAgtk(t *testing.T) {
	root := agentLockRepo(t, false, "")
	if err := os.Remove(filepath.Join(root, repogov.AgentManifestFile)); err != nil {
		t.Fatal(err)
	}
	if c := checkLock(t, root, repospec.Default()); c.Needed() {
		t.Errorf("change = %+v, want none without an agtk manifest", c)
	}
}

package repogov

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/repospec"
)

const (
	// AgentManifestFile is agentic-toolkit's entry manifest. A repository without
	// it does not use agtk, and the lockfile policy does not apply to it.
	AgentManifestFile = ".agentic-toolkit.yaml"
	// AgentLockFile is the lockfile agtk writes next to the manifest.
	AgentLockFile = ".agentic-toolkit.lock.yaml"

	gitignoreFile = ".gitignore"
	// agentLockIgnoreEntry is anchored to the repository root; a bare pattern
	// would also match a lockfile in a subdirectory that is tracked on purpose.
	agentLockIgnoreEntry = "/" + AgentLockFile
)

// AgentLockChange is what sync would do to bring the agtk lockfile in line with
// the spec's agent_lock policy.
//
// A lockfile that is ignored is absent from every fresh checkout, and `agtk
// sync` locks from scratch when the file is missing, so a new worktree starts on
// the current head of each source ref. A tracked lockfile pins them instead.
// The policy is the repository's choice and is recorded in the spec, never
// inferred from the state of .gitignore: a repository that tracks its lock on
// purpose is indistinguishable, from the file system alone, from one that was
// never migrated.
type AgentLockChange struct {
	// AddIgnore means .gitignore lacks the lockfile entry the policy requires.
	AddIgnore bool `json:"add_ignore,omitempty"`
	// Untrack means the lockfile is in the index and must leave it. The file
	// stays on disk.
	Untrack bool `json:"untrack,omitempty"`
	// RemoveIgnore means .gitignore carries an entry the tracked policy forbids.
	RemoveIgnore bool `json:"remove_ignore,omitempty"`
}

// Needed reports whether sync has anything to do for the lockfile.
func (c AgentLockChange) Needed() bool {
	return c.AddIgnore || c.Untrack || c.RemoveIgnore
}

// agentLockChange computes the change the spec's policy calls for. It returns
// the zero value for a repository that does not use agtk.
func agentLockChange(ctx context.Context, runner git.Runner, workdir string, spec repospec.Spec) (AgentLockChange, error) {
	if _, err := os.Stat(filepath.Join(workdir, AgentManifestFile)); err != nil {
		if os.IsNotExist(err) {
			return AgentLockChange{}, nil
		}
		return AgentLockChange{}, fmt.Errorf("stat %s: %w", AgentManifestFile, err)
	}
	lines, err := readGitignore(workdir)
	if err != nil {
		return AgentLockChange{}, err
	}
	ignored := hasIgnoreEntry(lines)

	if spec.AgentLock == repospec.AgentLockTracked {
		return AgentLockChange{RemoveIgnore: ignored}, nil
	}
	tracked, err := lockTracked(ctx, runner, workdir)
	if err != nil {
		return AgentLockChange{}, err
	}
	return AgentLockChange{AddIgnore: !ignored, Untrack: tracked}, nil
}

// lockTracked asks the index, not the working tree: an ignored file that was
// committed before the ignore entry existed stays tracked.
func lockTracked(ctx context.Context, runner git.Runner, workdir string) (bool, error) {
	res, err := runner.Run(ctx, workdir, "ls-files", "--", AgentLockFile)
	if err != nil {
		return false, fmt.Errorf("list tracked files: %w", err)
	}
	return strings.TrimSpace(res.Stdout) != "", nil
}

func readGitignore(workdir string) ([]string, error) {
	f, err := os.Open(filepath.Join(workdir, gitignoreFile)) // #nosec G304 -- repository's own .gitignore.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", gitignoreFile, err)
	}
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", gitignoreFile, err)
	}
	return lines, nil
}

func hasIgnoreEntry(lines []string) bool {
	for _, l := range lines {
		if isLockIgnoreEntry(l) {
			return true
		}
	}
	return false
}

// isLockIgnoreEntry matches the exact patterns that ignore only the root
// lockfile, so removing the entry under the tracked policy cannot touch an
// unrelated rule.
func isLockIgnoreEntry(line string) bool {
	t := strings.TrimSpace(line)
	return t == agentLockIgnoreEntry || t == AgentLockFile
}

// applyAgentLock performs the change: .gitignore first, then the index.
func applyAgentLock(ctx context.Context, runner git.Runner, workdir string, c AgentLockChange) ([]string, error) {
	var written []string
	if c.AddIgnore || c.RemoveIgnore {
		lines, err := readGitignore(workdir)
		if err != nil {
			return written, err
		}
		if c.RemoveIgnore {
			kept := lines[:0:0]
			for _, l := range lines {
				if !isLockIgnoreEntry(l) {
					kept = append(kept, l)
				}
			}
			lines = kept
		} else {
			lines = append(lines, agentLockIgnoreEntry)
		}
		out := strings.Join(lines, "\n")
		if out != "" {
			out += "\n"
		}
		// #nosec G306 -- .gitignore is committed repository content, not a secret.
		if err := os.WriteFile(filepath.Join(workdir, gitignoreFile), []byte(out), 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", gitignoreFile, err)
		}
		written = append(written, gitignoreFile)
	}
	if c.Untrack {
		if _, err := runner.Run(ctx, workdir, "rm", "--cached", "--quiet", "--", AgentLockFile); err != nil {
			return written, fmt.Errorf("untrack %s: %w", AgentLockFile, err)
		}
		written = append(written, AgentLockFile+" (untracked)")
	}
	return written, nil
}

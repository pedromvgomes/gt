package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
)

type Result struct {
	Stdout string
	Stderr string
	Exit   int
}

type Runner interface {
	Run(ctx context.Context, dir string, args ...string) (Result, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir string, args ...string) (Result, error) {
	slog.Debug("running git", "dir", dir, "args", args)
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), Exit: 0}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.Exit = exitErr.ExitCode()
		return result, fmt.Errorf("git %s failed with exit %d: %s", strings.Join(args, " "), result.Exit, strings.TrimSpace(result.Stderr))
	}
	return result, fmt.Errorf("run git %s: %w", strings.Join(args, " "), err)
}

func FindRepoRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve current directory: %w", err)
	}
	for {
		if isDir(filepath.Join(dir, ".bare")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside a gt-managed repository (no .bare directory found)")
		}
		dir = parent
	}
}

func ParseDefaultBranch(remoteShow string) string {
	for _, line := range strings.Split(remoteShow, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "HEAD branch:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "HEAD branch:"))
		}
	}
	return ""
}

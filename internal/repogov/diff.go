package repogov

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Status is the state of one managed file relative to disk.
type Status string

const (
	// StatusOK means the file on disk matches what gt would render.
	StatusOK Status = "ok"
	// StatusDrifted means the file exists but differs.
	StatusDrifted Status = "drifted"
	// StatusMissing means gt would render the file but it is absent.
	StatusMissing Status = "missing"
)

// Result is one file's diff outcome.
type Result struct {
	Path     string
	Status   Status
	Workflow bool
	// Want is the rendered content; Got is what is on disk (nil when missing).
	Want []byte
	Got  []byte
}

// Drifted reports whether this file needs writing.
func (r Result) Drifted() bool { return r.Status != StatusOK }

// Diff compares rendered files against the working tree at workdir.
//
// skipWorkflows excludes files under .github/workflows/ from the comparison.
// The weekly in-repo sync passes it because GITHUB_TOKEN cannot write them;
// reporting drift it is structurally unable to fix would be noise.
func Diff(workdir string, files []File, skipWorkflows bool) ([]Result, error) {
	results := make([]Result, 0, len(files))
	for _, f := range files {
		if skipWorkflows && f.Workflow {
			continue
		}
		abs := filepath.Join(workdir, filepath.FromSlash(f.Path))
		got, err := os.ReadFile(abs)
		switch {
		case os.IsNotExist(err):
			results = append(results, Result{
				Path: f.Path, Status: StatusMissing, Workflow: f.Workflow, Want: f.Content,
			})
			continue
		case err != nil:
			return nil, fmt.Errorf("read %s: %w", f.Path, err)
		}
		status := StatusDrifted
		if bytes.Equal(got, f.Content) {
			status = StatusOK
		}
		results = append(results, Result{
			Path: f.Path, Status: status, Workflow: f.Workflow, Want: f.Content, Got: got,
		})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Path < results[j].Path })
	return results, nil
}

// Drifted returns only the results that need writing.
func Drifted(results []Result) []Result {
	out := make([]Result, 0, len(results))
	for _, r := range results {
		if r.Drifted() {
			out = append(out, r)
		}
	}
	return out
}

// Write applies the drifted results to the working tree, creating parent
// directories as needed. It returns the paths written, in order.
func Write(workdir string, results []Result) ([]string, error) {
	var written []string
	for _, r := range results {
		if !r.Drifted() {
			continue
		}
		abs := filepath.Join(workdir, filepath.FromSlash(r.Path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return written, fmt.Errorf("create directory for %s: %w", r.Path, err)
		}
		if err := os.WriteFile(abs, r.Want, 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", r.Path, err)
		}
		written = append(written, r.Path)
	}
	return written, nil
}

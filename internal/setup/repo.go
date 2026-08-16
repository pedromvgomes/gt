package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pedromvgomes/gt/internal/config"
	"gopkg.in/yaml.v3"
)

// RepoConfigName is the in-tree, committed file a repository uses to declare
// setup templates that gt runs after clone and after every new worktree.
const RepoConfigName = ".gt.yaml"

// LoadRepoTemplates reads <dir>/.gt.yaml and returns the setup templates it
// declares. Templates declared in a repo's own committed .gt.yaml are scoped to
// that repo, so they run unconditionally — their match globs are ignored by the
// callers. Returns nil with no error when the file does not exist.
func LoadRepoTemplates(dir string) ([]config.Template, error) {
	path := filepath.Join(dir, RepoConfigName)
	// #nosec G304 -- reads the repository's own .gt.yaml.
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Setup config.Setup `yaml:"setup"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := config.ValidateSetup(doc.Setup); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc.Setup.Templates, nil
}

// MergeTemplates appends repo-local templates to the matched global templates.
// A repo template with the same name as a global one overrides it in place
// (the global entry keeps its position; its body is replaced). Order is
// preserved: global templates first, then repo templates not already present.
func MergeTemplates(global, repo []config.Template) []config.Template {
	return config.MergeTemplates(global, repo)
}

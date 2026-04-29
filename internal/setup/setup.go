package setup

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/ui"
)

const (
	LayoutBare  = "bare"
	LayoutPlain = "plain"
)

// Context captures the resolved repository state used to build environment
// variables for setup templates.
type Context struct {
	Root          string
	WorkDir       string
	Layout        string
	DefaultBranch string
	RepoOwner     string
	RepoName      string
	RepoURL       string
}

// Vars returns the GT_* substitution map.
func (c Context) Vars() map[string]string {
	return map[string]string{
		"GT_ROOT":           c.Root,
		"GT_WORKDIR":        c.WorkDir,
		"GT_LAYOUT":         c.Layout,
		"GT_DEFAULT_BRANCH": c.DefaultBranch,
		"GT_REPO_OWNER":     c.RepoOwner,
		"GT_REPO_NAME":      c.RepoName,
		"GT_REPO_URL":       c.RepoURL,
	}
}

// Env returns "KEY=VALUE" pairs suitable for exec.Cmd.Env, layered on top of
// the parent environment.
func (c Context) Env() []string {
	env := os.Environ()
	for k, v := range c.Vars() {
		env = append(env, k+"="+v)
	}
	return env
}

// FromBareClone builds a Context for a freshly created gt-managed root.
func FromBareClone(root, defaultBranch, repoURL string) (Context, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Context{}, fmt.Errorf("resolve root path: %w", err)
	}
	owner, name, err := ParseRepoURL(repoURL)
	if err != nil {
		return Context{}, err
	}
	return Context{
		Root:          absRoot,
		WorkDir:       filepath.Join(absRoot, defaultBranch),
		Layout:        LayoutBare,
		DefaultBranch: defaultBranch,
		RepoOwner:     owner,
		RepoName:      name,
		RepoURL:       repoURL,
	}, nil
}

// DetectFromCWD walks up from cwd to find a gt-managed root or a plain git
// repository, then resolves the default branch and origin URL.
func DetectFromCWD(ctx context.Context, runner git.Runner, cwd string) (Context, error) {
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return Context{}, fmt.Errorf("resolve current directory: %w", err)
	}
	if root, ok := findBareRoot(absCWD); ok {
		return detectBare(ctx, runner, root)
	}
	return detectPlain(ctx, runner, absCWD)
}

func detectBare(ctx context.Context, runner git.Runner, root string) (Context, error) {
	defaultBranch := readDefaultBranch(ctx, runner, root)
	repoURL := readOriginURL(ctx, runner, root, true)
	owner, name, _ := ParseRepoURL(repoURL)
	return Context{
		Root:          root,
		WorkDir:       filepath.Join(root, defaultBranch),
		Layout:        LayoutBare,
		DefaultBranch: defaultBranch,
		RepoOwner:     owner,
		RepoName:      name,
		RepoURL:       repoURL,
	}, nil
}

func detectPlain(ctx context.Context, runner git.Runner, cwd string) (Context, error) {
	res, err := runner.Run(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return Context{}, ui.Errorf(ui.ExitUser, "not inside a git repository: %v", err)
	}
	root := strings.TrimSpace(res.Stdout)
	if root == "" {
		return Context{}, ui.Errorf(ui.ExitUser, "not inside a git repository")
	}
	defaultBranch := readPlainDefaultBranch(ctx, runner, root)
	repoURL := readOriginURL(ctx, runner, root, false)
	owner, name, _ := ParseRepoURL(repoURL)
	return Context{
		Root:          root,
		WorkDir:       root,
		Layout:        LayoutPlain,
		DefaultBranch: defaultBranch,
		RepoOwner:     owner,
		RepoName:      name,
		RepoURL:       repoURL,
	}, nil
}

func findBareRoot(start string) (string, bool) {
	dir := start
	for {
		stat, err := os.Stat(filepath.Join(dir, ".bare"))
		if err == nil && stat.IsDir() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func readDefaultBranch(ctx context.Context, runner git.Runner, root string) string {
	if res, err := runner.Run(ctx, root, "--git-dir=.bare", "symbolic-ref", "--short", "HEAD"); err == nil {
		if branch := strings.TrimSpace(res.Stdout); branch != "" {
			return branch
		}
	}
	if res, err := runner.Run(ctx, root, "--git-dir=.bare", "remote", "show", "origin"); err == nil {
		if branch := git.ParseDefaultBranch(res.Stdout); branch != "" {
			return branch
		}
	}
	return "main"
}

func readPlainDefaultBranch(ctx context.Context, runner git.Runner, root string) string {
	if res, err := runner.Run(ctx, root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		ref := strings.TrimSpace(res.Stdout)
		if idx := strings.LastIndex(ref, "/"); idx >= 0 {
			return ref[idx+1:]
		}
		if ref != "" {
			return ref
		}
	}
	if res, err := runner.Run(ctx, root, "symbolic-ref", "--short", "HEAD"); err == nil {
		if branch := strings.TrimSpace(res.Stdout); branch != "" {
			return branch
		}
	}
	return "main"
}

func readOriginURL(ctx context.Context, runner git.Runner, root string, bare bool) string {
	args := []string{"config", "--get", "remote.origin.url"}
	if bare {
		args = append([]string{"--git-dir=.bare"}, args...)
	}
	if res, err := runner.Run(ctx, root, args...); err == nil {
		return strings.TrimSpace(res.Stdout)
	}
	return ""
}

// ParseRepoURL extracts (owner, name) from common git URL forms:
//
//	git@host:owner/repo.git
//	ssh://git@host/owner/repo.git
//	https://host/owner/repo[.git]
//
// Returns an empty owner/name with no error when the URL is empty.
func ParseRepoURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil
	}
	if owner, name, ok := parseSCPLikeURL(raw); ok {
		return owner, name, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse repo url %q: %w", raw, err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo url %q does not look like <host>/<owner>/<repo>", raw)
	}
	owner := parts[0]
	name := strings.TrimSuffix(parts[1], ".git")
	return owner, name, nil
}

// SelectOptions controls which templates Select returns.
type SelectOptions struct {
	Names   []string // explicit names from --setup a,b,c (in order)
	NoSetup bool     // --no-setup short-circuit
}

// Select returns the templates that should run for repoURL, in the order they
// will execute. With Names set, exactly those templates run (errors on unknown
// names). Otherwise, every template whose match patterns match repoURL is
// returned in config-file order.
func Select(s config.Setup, repoURL string, opts SelectOptions) ([]config.Template, error) {
	if opts.NoSetup {
		return nil, nil
	}
	if len(opts.Names) > 0 {
		byName := make(map[string]config.Template, len(s.Templates))
		for _, t := range s.Templates {
			byName[t.Name] = t
		}
		out := make([]config.Template, 0, len(opts.Names))
		for _, n := range opts.Names {
			t, ok := byName[n]
			if !ok {
				return nil, ui.Errorf(ui.ExitUser, "unknown setup template %q", n)
			}
			out = append(out, t)
		}
		return out, nil
	}
	var out []config.Template
	for _, t := range s.Templates {
		if matchTemplate(t, repoURL) {
			out = append(out, t)
		}
	}
	return out, nil
}

func matchTemplate(t config.Template, repoURL string) bool {
	for _, pattern := range t.Match {
		if MatchURL(pattern, repoURL) {
			return true
		}
	}
	return false
}

// MatchURL applies a glob pattern to repoURL as a substring match. `*` matches
// any sequence of characters; `?` matches a single character. Other regex
// metacharacters are escaped.
func MatchURL(pattern, repoURL string) bool {
	var b strings.Builder
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(repoURL)
}

func parseSCPLikeURL(raw string) (string, string, bool) {
	if strings.Contains(raw, "://") {
		return "", "", false
	}
	idx := strings.Index(raw, ":")
	if idx <= 0 {
		return "", "", false
	}
	at := strings.Index(raw[:idx], "@")
	if at < 0 {
		return "", "", false
	}
	path := raw[idx+1:]
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	owner := parts[0]
	name := strings.TrimSuffix(parts[1], ".git")
	return owner, name, true
}

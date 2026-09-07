package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	WorktreeTypes []string   `yaml:"worktree_types"`
	SSH           SSH        `yaml:"ssh"`
	Setup         Setup      `yaml:"setup"`
	Profiles      []Profile  `yaml:"profiles"`
	AutoUpdate    AutoUpdate `yaml:"auto_update"`
}

// Profile is a named set of environment variables that gt exports from a
// repository's .envrc. gt attaches no meaning to the names or the values: it
// exists so that tooling shelling out from a checkout resolves the same
// credentials no matter which shell launched it. A profile with no match
// patterns never applies on its own and is reachable only via --profile.
type Profile struct {
	Name  string            `yaml:"name"`
	Match []string          `yaml:"match,omitempty"`
	Env   map[string]string `yaml:"env"`
}

type AutoUpdate struct {
	Enabled       bool          `yaml:"enabled"`
	CheckInterval time.Duration `yaml:"check_interval"`
}

type SSH struct {
	HostAliases map[string]string            `yaml:"host_aliases"`
	UserAliases map[string]map[string]string `yaml:"user_aliases"`
}

type Setup struct {
	Templates []Template `yaml:"templates"`
}

type Template struct {
	Name   string   `yaml:"name"`
	Match  []string `yaml:"match,omitempty"`
	Run    string   `yaml:"run,omitempty"`
	Script string   `yaml:"script,omitempty"`
}

func Default() Config {
	return Config{
		WorktreeTypes: []string{"feature", "fix", "chore"},
		SSH: SSH{
			HostAliases: map[string]string{},
			UserAliases: map[string]map[string]string{},
		},
		AutoUpdate: AutoUpdate{
			Enabled:       true,
			CheckInterval: 24 * time.Hour,
		},
	}
}

func Load(cwd string) (Config, error) {
	path, err := GlobalPath()
	if err != nil {
		return Config{}, err
	}
	if err := ensureGlobal(path); err != nil {
		return Config{}, err
	}

	cfg, err := read(path)
	if err != nil {
		return Config{}, err
	}

	if root, ok := findManagedRoot(cwd); ok {
		repoPath := filepath.Join(root, ".gt.yaml")
		if _, err := os.Stat(repoPath); err == nil {
			repoCfg, err := read(repoPath)
			if err != nil {
				return Config{}, err
			}
			cfg = Merge(cfg, repoCfg)
		} else if !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("stat per-repo config: %w", err)
		}
	}

	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func GlobalPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gt", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "gt", "config.yaml"), nil
}

func Merge(base, override Config) Config {
	if len(override.WorktreeTypes) > 0 {
		base.WorktreeTypes = override.WorktreeTypes
	}
	if override.SSH.HostAliases != nil {
		base.SSH.HostAliases = override.SSH.HostAliases
	}
	if base.SSH.HostAliases == nil {
		base.SSH.HostAliases = map[string]string{}
	}
	if override.SSH.UserAliases != nil {
		base.SSH.UserAliases = override.SSH.UserAliases
	}
	if base.SSH.UserAliases == nil {
		base.SSH.UserAliases = map[string]map[string]string{}
	}
	base.Setup.Templates = MergeTemplates(base.Setup.Templates, override.Setup.Templates)
	base.Profiles = MergeProfiles(base.Profiles, override.Profiles)
	return base
}

// MergeTemplates layers override templates on top of base. A template in
// override that shares a name with one in base replaces it in place (the base
// entry keeps its position; its body is replaced). Templates whose names are
// not already present are appended in order. base templates first, then the
// new override templates. This is the precedence used both when a per-repo
// .gt.yaml extends the global config and when a repo's committed .gt.yaml
// extends the resolved config.
func MergeTemplates(base, override []Template) []Template {
	out := make([]Template, len(base))
	copy(out, base)
	index := make(map[string]int, len(out))
	for i, t := range out {
		index[t.Name] = i
	}
	for _, t := range override {
		if i, ok := index[t.Name]; ok {
			out[i] = t
			continue
		}
		index[t.Name] = len(out)
		out = append(out, t)
	}
	return out
}

// MergeProfiles layers override profiles on top of base with the same
// precedence MergeTemplates uses: a same-named profile replaces the base entry
// in place, keeping its position, and new names are appended in order. Position
// matters because profile selection is first-match-wins.
func MergeProfiles(base, override []Profile) []Profile {
	out := make([]Profile, len(base))
	copy(out, base)
	index := make(map[string]int, len(out))
	for i, p := range out {
		index[p.Name] = i
	}
	for _, p := range override {
		if i, ok := index[p.Name]; ok {
			out[i] = p
			continue
		}
		index[p.Name] = len(out)
		out = append(out, p)
	}
	return out
}

func Validate(cfg Config) error {
	if len(cfg.WorktreeTypes) == 0 {
		return fmt.Errorf("worktree_types cannot be empty")
	}
	reserved := map[string]bool{"scratch": true, "main": true, "master": true}
	seen := map[string]bool{}
	for _, typ := range cfg.WorktreeTypes {
		if typ == "" {
			return fmt.Errorf("worktree_types cannot contain empty values")
		}
		if reserved[typ] {
			return fmt.Errorf("worktree type %q is reserved", typ)
		}
		if seen[typ] {
			return fmt.Errorf("worktree type %q is duplicated", typ)
		}
		seen[typ] = true
	}

	if cfg.AutoUpdate.Enabled && cfg.AutoUpdate.CheckInterval < time.Hour {
		return fmt.Errorf("auto_update.check_interval must be at least 1h")
	}

	if err := ValidateSSH(cfg.SSH); err != nil {
		return err
	}

	if err := ValidateProfiles(cfg.Profiles); err != nil {
		return err
	}

	return ValidateSetup(cfg.Setup)
}

func ValidateSSH(s SSH) error {
	for host, alias := range s.HostAliases {
		if strings.TrimSpace(host) == "" {
			return fmt.Errorf("ssh.host_aliases: host cannot be empty")
		}
		if strings.TrimSpace(alias) == "" {
			return fmt.Errorf("ssh.host_aliases[%q]: alias cannot be empty", host)
		}
	}
	for user, hosts := range s.UserAliases {
		if strings.TrimSpace(user) == "" {
			return fmt.Errorf("ssh.user_aliases: user cannot be empty")
		}
		if len(hosts) == 0 {
			return fmt.Errorf("ssh.user_aliases[%q]: at least one host mapping is required", user)
		}
		for host, alias := range hosts {
			if strings.TrimSpace(host) == "" {
				return fmt.Errorf("ssh.user_aliases[%q]: host cannot be empty", user)
			}
			if strings.TrimSpace(alias) == "" {
				return fmt.Errorf("ssh.user_aliases[%q][%q]: alias cannot be empty", user, host)
			}
		}
	}
	return nil
}

// ProfileNone is the reserved profile name meaning "export nothing". It lets a
// repository opt out of a catch-all match pattern without deleting it, and it
// is what --no-profile resolves to.
const ProfileNone = "none"

func ValidateProfiles(profiles []Profile) error {
	names := map[string]bool{}
	for i, p := range profiles {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return fmt.Errorf("profiles[%d]: name is required", i)
		}
		if !validProfileName(name) {
			return fmt.Errorf("profiles[%d]: invalid profile name %q; use letters, digits, '.', '_' or '-'", i, p.Name)
		}
		if name == ProfileNone {
			return fmt.Errorf("profiles[%d]: %q is reserved; it always exports nothing", i, ProfileNone)
		}
		if names[name] {
			return fmt.Errorf("profiles[%d]: duplicate profile name %q", i, name)
		}
		names[name] = true
		if len(p.Env) == 0 {
			return fmt.Errorf("profiles[%q]: env must declare at least one variable", name)
		}
		for j, pat := range p.Match {
			if strings.TrimSpace(pat) == "" {
				return fmt.Errorf("profiles[%q].match[%d]: pattern cannot be empty", name, j)
			}
		}
		for key, value := range p.Env {
			if !validEnvName(key) {
				return fmt.Errorf("profiles[%q].env: invalid variable name %q", name, key)
			}
			if strings.ContainsAny(value, "\n\r") {
				return fmt.Errorf("profiles[%q].env[%q]: value cannot contain a newline", name, key)
			}
		}
	}
	return nil
}

// validProfileName keeps names to what can appear unambiguously in a --profile
// flag, an error message and the generated .envrc comment.
func validProfileName(name string) bool {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// validEnvName is POSIX's name rule. The value is escaped before it reaches the
// .envrc, but the name is emitted bare on the left of the '=' and cannot be.
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func ValidateSetup(s Setup) error {
	names := map[string]bool{}
	for i, tpl := range s.Templates {
		if strings.TrimSpace(tpl.Name) == "" {
			return fmt.Errorf("setup.templates[%d]: name is required", i)
		}
		if names[tpl.Name] {
			return fmt.Errorf("setup.templates[%d]: duplicate template name %q", i, tpl.Name)
		}
		names[tpl.Name] = true
		hasRun := strings.TrimSpace(tpl.Run) != ""
		hasScript := strings.TrimSpace(tpl.Script) != ""
		if hasRun && hasScript {
			return fmt.Errorf("setup.templates[%q]: run and script are mutually exclusive", tpl.Name)
		}
		if !hasRun && !hasScript {
			return fmt.Errorf("setup.templates[%q]: one of run or script is required", tpl.Name)
		}
		for j, pat := range tpl.Match {
			if strings.TrimSpace(pat) == "" {
				return fmt.Errorf("setup.templates[%q].match[%d]: pattern cannot be empty", tpl.Name, j)
			}
		}
	}
	return nil
}

func ensureGlobal(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat global config: %w", err)
	}
	// 0700/0600: this is the user's own config under their home directory.
	// Nothing else needs to read it, so nothing else should be able to.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		return fmt.Errorf("write default config: %w", err)
	}
	return nil
}

func read(path string) (Config, error) {
	// #nosec G304 -- reads gt's own config from the resolved config path.
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.SSH.HostAliases == nil {
		cfg.SSH.HostAliases = map[string]string{}
	}
	if cfg.SSH.UserAliases == nil {
		cfg.SSH.UserAliases = map[string]map[string]string{}
	}
	return cfg, nil
}

func findManagedRoot(cwd string) (string, bool) {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return "", false
	}
	for {
		if stat, err := os.Stat(filepath.Join(dir, ".bare")); err == nil && stat.IsDir() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

const seed = `# gt config - generated automatically; safe to edit.

# Worktree types accepted by 'gt wt add <type/name>'. Add types here
# (e.g. release, hotfix, chore) without recompiling. The 'scratch'
# worktree is special-cased and not part of this list.
worktree_types:
  - feature
  - fix
  - chore

# Optional SSH aliases. When 'gt clone' converts an HTTPS URL to SSH
# (or 'gt set-ssh-remote' rewrites an existing origin), the URL host
# is looked up here and the alias replaces it. Useful with per-account
# SSH aliases in ~/.ssh/config (e.g. github-personal vs github-work).
#
# host_aliases is the default per-host map. user_aliases is a per-user
# override layer used when --user is passed (or selected interactively
# when multiple users are configured for the same host). When --user
# resolves to no entry, gt warns and falls back to host_aliases.
ssh:
  host_aliases: {}
  user_aliases: {}
  # Example:
  #   host_aliases:
  #     github.com: github-personal
  #   user_aliases:
  #     pedromvgomes:
  #       github.com: github-personal
  #     pedro-work:
  #       github.com: github-work

# Auto-update behavior. When enabled, gt checks the GitHub releases
# API at most once per check_interval and offers to install newer
# versions via 'gt update'. Set GT_NO_UPDATE_CHECK=1 to disable for a
# single invocation. Disabled automatically for non-interactive runs.
auto_update:
  enabled: true
  check_interval: 24h

# Optional environment profiles. 'gt set-auth' (and the post-clone auth step)
# appends the selected profile's variables to the repository's .envrc, next to
# GH_TOKEN, so anything launched from the checkout resolves the same
# credentials regardless of which shell started it. The motivating case is
# coding-agent CLIs, which pick their credential store from an env var
# (CLAUDE_CONFIG_DIR, CODEX_HOME), but gt attaches no meaning to the names or
# values - it exports what you list here and nothing else.
#
# Selection is first-match-wins over this list, using the same glob syntax as
# setup template 'match' patterns, against the repo's origin URL. A profile
# with no 'match' never applies on its own; select it with
# 'gt set-auth --profile <name>' or 'gt clone --profile <name>'. When nothing
# matches, the .envrc is exactly what gt wrote before profiles existed. The
# name 'none' is reserved and exports nothing, so a catch-all pattern can be
# escaped per repo with '--no-profile'.
#
# Values get a leading '~/' or '$HOME' expanded and are then emitted literally;
# no other shell expansion happens. gt does not check that a path exists.
#
# profiles:
#   - name: work
#     match:
#       - "github.com:acme/*"
#       - "github.com/acme/*"
#     env:
#       CLAUDE_CONFIG_DIR: ~/.claude
#       CODEX_HOME: ~/.codex
#   - name: personal
#     env:
#       CLAUDE_CONFIG_DIR: ~/.claude-personal
profiles: []

# Optional setup templates run after 'gt clone' (and on demand via
# 'gt setup'). Templates are evaluated in this order; every template
# whose 'match' globs match the repo URL runs. Use match: ["*"] for a
# template that always applies. Each template defines exactly one of
# 'run:' (inline shell) or 'script:' (path to an executable file).
#
# Available env vars in scripts and substituted into 'script:' paths:
#   GT_ROOT, GT_WORKDIR, GT_LAYOUT (bare|plain),
#   GT_SETUP_PHASE (clone|worktree|manual),
#   GT_DEFAULT_BRANCH, GT_REPO_OWNER, GT_REPO_NAME, GT_REPO_URL.
#
# A repo can also commit its own .gt.yaml (in the repo tree, not at the gt
# root) with a 'setup:' block. Those templates run unconditionally after
# 'gt clone' (phase=clone, in the default-branch checkout) and after
# 'gt wt add' (phase=worktree, in the new worktree).
#
# setup:
#   templates:
#     - name: agentic-toolkit
#       match:
#         - "github.com:pedromvgomes/*"
#         - "github.com/pedromvgomes/*"
#       run: |
#         git clone git@github.com:pedromvgomes/agentic-toolkit.git "${GT_WORKDIR}/.agentic"
#         ln -sfn "${GT_WORKDIR}/.agentic/CLAUDE.md" "${GT_WORKDIR}/CLAUDE.md"
#     - name: golang-extras
#       match: ["*"]
#       script: ${HOME}/.config/gt/setup-scripts/golang-extras.sh
#
# To govern every repo you clone, add a template that runs 'gt repo sync'.
# It renders the files declared by the repo's committed .gt-repo.yaml
# (Dependabot config, the PR gate, auto-merge). Repos without that file are
# left alone, so this is safe to match broadly:
#
#     - name: repo-governance
#       match:
#         - "github.com:pedromvgomes/*"
#         - "github.com/pedromvgomes/*"
#       run: gt repo sync --yes
setup:
  templates: []
`

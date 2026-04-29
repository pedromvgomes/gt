package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	WorktreeTypes []string `yaml:"worktree_types"`
	SSH           SSH      `yaml:"ssh"`
	Setup         Setup    `yaml:"setup"`
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
			if len(repoCfg.Setup.Templates) > 0 {
				return Config{}, fmt.Errorf("per-repo config %s must not define setup templates; declare them in the global config only", repoPath)
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
	return base
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
	if err := ValidateSSH(cfg.SSH); err != nil {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		return fmt.Errorf("write default config: %w", err)
	}
	return nil
}

func read(path string) (Config, error) {
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

# Optional setup templates run after 'gt clone' (and on demand via
# 'gt setup'). Templates are evaluated in this order; every template
# whose 'match' globs match the repo URL runs. Use match: ["*"] for a
# template that always applies. Each template defines exactly one of
# 'run:' (inline shell) or 'script:' (path to an executable file).
#
# Available env vars in scripts and substituted into 'script:' paths:
#   GT_ROOT, GT_WORKDIR, GT_LAYOUT (bare|plain),
#   GT_DEFAULT_BRANCH, GT_REPO_OWNER, GT_REPO_NAME, GT_REPO_URL.
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
setup:
  templates: []
`

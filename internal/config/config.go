package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	vconfig "go.vipengele.org/config"
)

// Config is gt's configuration, loaded by go.vipengele.org/config from the
// defaults, the global config file, a managed root's .gt.yaml overlay and GT_*
// environment variables, in that order.
//
// profiles and setup.templates merge across layers by name, as MergeProfiles
// and MergeTemplates do: a same-named entry replaces the earlier one where it
// stands, and new names are appended. A shadowed name is never run twice, and
// profile selection, which is first-match-wins, keeps its order. worktree_types
// and the ssh maps are replaced whole by the last layer that sets them.
//
// Load reads the config tags. The yaml tags serve code that decodes these
// types with gopkg.in/yaml.v3 directly, such as setup.LoadRepoTemplates.
type Config struct {
	WorktreeTypes []string   `yaml:"worktree_types" config:"worktree_types" doc:"Worktree types accepted by 'gt wt add <type/name>'. 'scratch', 'main' and\n'master' are reserved."`
	SSH           SSH        `yaml:"ssh" config:"ssh" doc:"SSH host aliases. When 'gt clone' converts an HTTPS URL to SSH, or\n'gt set-ssh-remote' rewrites origin, the URL host is replaced by its alias,\nas with per-account aliases in ~/.ssh/config."`
	Setup         Setup      `yaml:"setup" config:"setup" doc:"Setup templates run after 'gt clone' and on demand via 'gt setup'."`
	Profiles      []Profile  `yaml:"profiles" config:"profiles" merge:"name" doc:"Environment profiles. 'gt set-auth' and the post-clone auth step append the\nselected profile's variables to the repository's .envrc next to GH_TOKEN.\nSelection is first-match-wins against the origin URL; a profile with no\nmatch applies only via --profile. The name 'none' is reserved and exports\nnothing. Values get a leading '~/' or '$HOME' expanded and are otherwise\nemitted literally."`
	AutoUpdate    AutoUpdate `yaml:"auto_update" config:"auto_update" doc:"Background update check. GT_NO_UPDATE_CHECK=1 disables it for one\ninvocation."`
}

// Profile is a named set of environment variables that gt exports from a
// repository's .envrc. gt attaches no meaning to the names or the values: it
// exists so that tooling shelling out from a checkout resolves the same
// credentials no matter which shell launched it. A profile with no match
// patterns never applies on its own and is reachable only via --profile.
type Profile struct {
	Name  string            `yaml:"name" config:"name" doc:"Profile name: letters, digits, '.', '_' or '-'."`
	Match []string          `yaml:"match,omitempty" config:"match" doc:"Globs matched against the origin URL, as setup template match patterns are."`
	Env   map[string]string `yaml:"env" config:"env" doc:"Variables to export, by POSIX name."`
}

type AutoUpdate struct {
	Enabled       bool          `yaml:"enabled" config:"enabled" doc:"Check the GitHub releases API for a newer gt."`
	CheckInterval time.Duration `yaml:"check_interval" config:"check_interval" doc:"Time between two checks, at least 1h when enabled."`
}

type SSH struct {
	HostAliases map[string]string            `yaml:"host_aliases" config:"host_aliases" doc:"Alias per URL host, such as github.com: github-personal."`
	UserAliases map[string]map[string]string `yaml:"user_aliases" config:"user_aliases" doc:"Per-user host aliases, used over host_aliases when --user names an entry.\nA user with no entry falls back to host_aliases with a warning."`
}

type Setup struct {
	Templates []Template `yaml:"templates" config:"templates" merge:"name" doc:"Templates run in order; every template whose match globs match the repo URL\nruns. Each sets exactly one of run (inline shell) or script (an executable\npath). Scripts see GT_ROOT, GT_WORKDIR, GT_LAYOUT, GT_SETUP_PHASE,\nGT_DEFAULT_BRANCH, GT_REPO_OWNER, GT_REPO_NAME and GT_REPO_URL.\nTemplates a repository commits in its own .gt.yaml ignore match and run,\nbehind a confirmation, after 'gt clone' and 'gt wt add'."`
}

type Template struct {
	Name   string   `yaml:"name" config:"name" doc:"Template name, unique; a later layer's template of the same name replaces it."`
	Match  []string `yaml:"match,omitempty" config:"match" doc:"Globs matched against the repo URL; [\"*\"] always applies, none only via --setup."`
	Run    string   `yaml:"run,omitempty" config:"run" doc:"Inline shell."`
	Script string   `yaml:"script,omitempty" config:"script" doc:"Path to an executable file."`
}

// OverlayAllow lists the keys a managed root's .gt.yaml may set. auto_update is
// left out: how gt updates itself is the user's choice, never a repository's.
var OverlayAllow = []string{"worktree_types", "ssh", "setup", "profiles"}

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

// Load resolves the configuration for a command run from cwd: the defaults,
// then the global config file, then the .gt.yaml at cwd's managed root
// restricted to OverlayAllow, then GT_* environment variables. The global file
// is seeded first when it does not exist.
func Load(cwd string) (Config, error) {
	path, err := GlobalPath()
	if err != nil {
		return Config{}, err
	}
	if err := ensureGlobal(path); err != nil {
		return Config{}, err
	}
	overlay, err := overlayPathFrom(cwd)
	if err != nil {
		return Config{}, err
	}

	providers := []vconfig.Provider{vconfig.File(path)}
	if overlay != "" {
		providers = append(providers, vconfig.File(overlay).Allow(OverlayAllow...))
	}
	providers = append(providers, vconfig.Env("GT", os.LookupEnv))

	res, err := vconfig.Load(Default(), providers...)
	if err != nil {
		return Config{}, err
	}
	return res.Value(), nil
}

// OverlayPath returns the path of the .gt.yaml overlay for the working
// directory: <root>/.gt.yaml, where root is the nearest directory at or above
// it holding a .bare directory. It returns "" outside a managed root. The root
// is found from the working directory alone; lookup is not consulted.
func OverlayPath(_ func(string) (string, bool)) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return overlayPathFrom(wd)
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

// Validate implements go.vipengele.org/config's Validator, so Load reports
// each problem with the value and the file line it came from.
func (c Config) Validate() error {
	return Validate(c)
}

// Validate reports the first problem in cfg as a *config.FieldError naming its
// key, or its path within a collection such as profiles[2].env.
func Validate(cfg Config) error {
	if len(cfg.WorktreeTypes) == 0 {
		return fieldError("worktree_types", "cannot be empty")
	}
	reserved := map[string]bool{"scratch": true, "main": true, "master": true}
	seen := map[string]bool{}
	for _, typ := range cfg.WorktreeTypes {
		if typ == "" {
			return fieldError("worktree_types", "cannot contain empty values")
		}
		if reserved[typ] {
			return fieldError("worktree_types", "worktree type %q is reserved", typ)
		}
		if seen[typ] {
			return fieldError("worktree_types", "worktree type %q is duplicated", typ)
		}
		seen[typ] = true
	}

	if cfg.AutoUpdate.Enabled && cfg.AutoUpdate.CheckInterval < time.Hour {
		return fieldError("auto_update.check_interval", "must be at least 1h")
	}

	if err := ValidateSSH(cfg.SSH); err != nil {
		return err
	}

	if err := ValidateProfiles(cfg.Profiles); err != nil {
		return err
	}

	return ValidateSetup(cfg.Setup)
}

// fieldError is a problem with the value at key, in the form Load resolves to
// the key's value and origin. Called outside Load its message is "key: problem".
func fieldError(key, format string, args ...any) error {
	return &vconfig.FieldError{Key: key, Err: fmt.Errorf(format, args...)}
}

func ValidateSSH(s SSH) error {
	for host, alias := range s.HostAliases {
		if strings.TrimSpace(host) == "" {
			return fieldError("ssh.host_aliases", "host cannot be empty")
		}
		if strings.TrimSpace(alias) == "" {
			return fieldError(fmt.Sprintf("ssh.host_aliases[%q]", host), "alias cannot be empty")
		}
	}
	for user, hosts := range s.UserAliases {
		if strings.TrimSpace(user) == "" {
			return fieldError("ssh.user_aliases", "user cannot be empty")
		}
		if len(hosts) == 0 {
			return fieldError(fmt.Sprintf("ssh.user_aliases[%q]", user), "at least one host mapping is required")
		}
		for host, alias := range hosts {
			if strings.TrimSpace(host) == "" {
				return fieldError(fmt.Sprintf("ssh.user_aliases[%q]", user), "host cannot be empty")
			}
			if strings.TrimSpace(alias) == "" {
				return fieldError(fmt.Sprintf("ssh.user_aliases[%q][%q]", user, host), "alias cannot be empty")
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
		at := fmt.Sprintf("profiles[%d]", i)
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return fieldError(at+".name", "name is required")
		}
		if !validProfileName(name) {
			return fieldError(at+".name", "invalid profile name %q; use letters, digits, '.', '_' or '-'", p.Name)
		}
		if name == ProfileNone {
			return fieldError(at+".name", "%q is reserved; it always exports nothing", ProfileNone)
		}
		if names[name] {
			return fieldError(at+".name", "duplicate profile name %q", name)
		}
		names[name] = true
		if len(p.Env) == 0 {
			return fieldError(at+".env", "profile %q: env must declare at least one variable", name)
		}
		for j, pat := range p.Match {
			if strings.TrimSpace(pat) == "" {
				return fieldError(at+".match", "profile %q: match[%d]: pattern cannot be empty", name, j)
			}
		}
		for key, value := range p.Env {
			if !validEnvName(key) {
				return fieldError(at+".env", "profile %q: invalid variable name %q", name, key)
			}
			if strings.ContainsAny(value, "\n\r") {
				return fieldError(fmt.Sprintf("%s.env[%q]", at, key), "profile %q: value cannot contain a newline", name)
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
		at := fmt.Sprintf("setup.templates[%d]", i)
		if strings.TrimSpace(tpl.Name) == "" {
			return fieldError(at+".name", "name is required")
		}
		if names[tpl.Name] {
			return fieldError(at+".name", "duplicate template name %q", tpl.Name)
		}
		names[tpl.Name] = true
		hasRun := strings.TrimSpace(tpl.Run) != ""
		hasScript := strings.TrimSpace(tpl.Script) != ""
		if hasRun && hasScript {
			return fieldError(at, "template %q: run and script are mutually exclusive", tpl.Name)
		}
		if !hasRun && !hasScript {
			return fieldError(at, "template %q: one of run or script is required", tpl.Name)
		}
		for j, pat := range tpl.Match {
			if strings.TrimSpace(pat) == "" {
				return fieldError(at+".match", "template %q: match[%d]: pattern cannot be empty", tpl.Name, j)
			}
		}
	}
	return nil
}

// ensureGlobal writes the documented defaults to path when no file is there.
func ensureGlobal(path string) error {
	// 0700/0600: this is the user's own config under their home directory.
	// Nothing else needs to read it, so nothing else should be able to.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if _, err := vconfig.Seed(path, Default()); err != nil {
		return fmt.Errorf("write default config: %w", err)
	}
	return nil
}

// overlayPathFrom returns <root>/.gt.yaml for the nearest directory at or
// above dir holding a .bare directory, or "" when there is none. The file
// itself need not exist; a missing overlay sets nothing.
func overlayPathFrom(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	for {
		if stat, err := os.Stat(filepath.Join(dir, ".bare")); err == nil && stat.IsDir() {
			return filepath.Join(dir, ".gt.yaml"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

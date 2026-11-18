package clone

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/setauth"
	"github.com/pedromvgomes/gt/internal/setup"
	"go.vipengele.org/cli/spec"
)

type Options struct {
	RepoURL     string
	Folder      string
	ForceSSH    bool
	NoSSH       bool
	NoSetupAuth bool
	User        string
	Profile     string
	NoProfile   bool
	AuthRunner  setauth.Runner

	SetupNames  []string
	NoSetup     bool
	YesSetup    bool
	ShowSetup   bool
	DryRunSetup bool
}

// checkProfileSelection rejects a profile selection before anything lands on
// disk. Cloning is not idempotent: a name validated only at the post-clone auth
// step fails with the folder already created, and the obvious retry then dies
// on "folder already exists" instead of repeating the real error.
func checkProfileSelection(cfg config.Config, opts Options) error {
	selected := strings.TrimSpace(opts.Profile)
	if opts.NoSetupAuth && (selected != "" || opts.NoProfile) {
		return errors.New("--profile and --no-profile need the post-clone auth step; drop --no-setup-auth")
	}
	if selected == "" {
		return nil
	}
	_, err := setauth.ResolveProfile(cfg.Profiles, "", selected, opts.NoProfile)
	return err
}

func Run(ctx context.Context, runner git.Runner, inv *spec.Invocation, cfg config.Config, opts Options) error {
	if strings.TrimSpace(opts.RepoURL) == "" {
		return errors.New("repository URL is required")
	}
	if opts.ForceSSH && opts.NoSSH {
		return errors.New("--ssh and --no-ssh are mutually exclusive")
	}
	if err := checkProfileSelection(cfg, opts); err != nil {
		return err
	}
	repoURL, err := ResolveRepoURL(inv, cfg, opts)
	if err != nil {
		return err
	}
	folder := opts.Folder
	if folder == "" {
		derived, err := DeriveFolder(repoURL)
		if err != nil {
			return err
		}
		folder = derived
		inv.Info("Using folder name: %s", folder)
	}
	if err := ValidateFolder(folder); err != nil {
		return err
	}
	if _, err := os.Stat(folder); err == nil {
		return fmt.Errorf("folder %q already exists", folder)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat folder %s: %w", folder, err)
	}

	barePath := filepath.Join(folder, ".bare")
	inv.Info("Cloning %s into %s...", repoURL, barePath)
	if _, err := runner.Run(ctx, "", "clone", "--bare", repoURL, barePath); err != nil {
		return err
	}

	// #nosec G306 -- the .git pointer file must be readable by git and any tool that follows it.
	if err := os.WriteFile(filepath.Join(folder, ".git"), []byte("gitdir: ./.bare\n"), 0o644); err != nil {
		return fmt.Errorf("write .git pointer: %w", err)
	}

	commands := [][]string{
		{"--git-dir=.bare", "config", "core.bare", "false"},
		{"--git-dir=.bare", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"--git-dir=.bare", "fetch", "origin"},
	}
	for _, args := range commands {
		if _, err := runner.Run(ctx, folder, args...); err != nil {
			return err
		}
	}

	defaultBranch := "main"
	if result, err := runner.Run(ctx, folder, "--git-dir=.bare", "remote", "show", "origin"); err == nil {
		if parsed := git.ParseDefaultBranch(result.Stdout); parsed != "" {
			defaultBranch = parsed
		}
	} else {
		inv.Warning("Could not detect default branch, using 'main'")
	}
	inv.Info("Default branch: %s", defaultBranch)

	if result, err := runner.Run(ctx, folder, "--git-dir=.bare", "rev-parse", "refs/heads/"+defaultBranch); err == nil {
		commit := strings.TrimSpace(result.Stdout)
		if commit != "" {
			if _, err := runner.Run(ctx, folder, "--git-dir=.bare", "update-ref", "--no-deref", "HEAD", commit); err != nil {
				return err
			}
		}
	}

	inv.Info("Creating %s worktree...", defaultBranch)
	if _, err := runner.Run(ctx, folder, "--git-dir=.bare", "worktree", "add", defaultBranch, defaultBranch); err != nil {
		return err
	}

	for _, typ := range cfg.WorktreeTypes {
		// #nosec G301 -- worktree directories hold checked-out source; 0750 would break tooling run under another account.
		if err := os.MkdirAll(filepath.Join(folder, typ), 0o755); err != nil {
			return fmt.Errorf("create %s worktree directory: %w", typ, err)
		}
	}

	inv.Success("Repository cloned successfully")
	_, _ = fmt.Fprintf(inv.Env.Stdout, "\nStructure:\n  %s/\n  |-- .bare/\n  |-- .git\n  |-- %s/\n", folder, defaultBranch)
	for _, typ := range cfg.WorktreeTypes {
		_, _ = fmt.Fprintf(inv.Env.Stdout, "  |-- %s/\n", typ)
	}
	_, _ = fmt.Fprintf(inv.Env.Stdout, "\nNext steps:\n  cd %s/%s\n", folder, defaultBranch)
	if !opts.NoSetupAuth {
		authRunner := opts.AuthRunner
		if authRunner == nil {
			authRunner = setauth.ExecRunner{}
		}
		if err := setauth.Run(ctx, authRunner, inv, setauth.Options{
			CWD:       folder,
			User:      opts.User,
			Profiles:  cfg.Profiles,
			RepoURL:   repoURL,
			Profile:   opts.Profile,
			NoProfile: opts.NoProfile,
		}); err != nil {
			return err
		}
	}

	if !opts.NoSetup {
		rcontext, err := setup.FromBareClone(folder, defaultBranch, repoURL)
		if err != nil {
			return err
		}
		templates, err := setup.Select(cfg.Setup, rcontext.RepoURL, setup.SelectOptions{
			Names:   opts.SetupNames,
			NoSetup: opts.NoSetup,
		})
		if err != nil {
			return err
		}
		// Templates committed in the repo's own .gt.yaml (now checked out on the
		// default branch) run unconditionally. Skip them when --setup names an
		// explicit subset, which selects from the global config only.
		untrusted := false
		if len(opts.SetupNames) == 0 {
			repoTemplates, err := setup.LoadRepoTemplates(rcontext.WorkDir)
			if err != nil {
				return err
			}
			untrusted = len(repoTemplates) > 0
			templates = setup.MergeTemplates(templates, repoTemplates)
		}
		if len(templates) == 0 {
			return nil
		}
		plan := setup.Plan{Templates: templates, Ctx: rcontext, Untrusted: untrusted}
		if err := setup.Execute(ctx, inv, plan, setup.RunOptions{
			Yes:    opts.YesSetup,
			Show:   opts.ShowSetup,
			DryRun: opts.DryRunSetup,
		}); err != nil {
			return err
		}
	}
	return nil
}

func ResolveRepoURL(inv *spec.Invocation, cfg config.Config, opts Options) (string, error) {
	if opts.NoSSH {
		return opts.RepoURL, nil
	}

	if host, owner, repo, ok := ParseSSHURL(opts.RepoURL); ok {
		canonical := CanonicalHost(cfg.SSH, host)
		alias, err := ResolveAlias(inv, cfg.SSH, canonical, opts.User)
		if err != nil {
			return "", err
		}
		if alias == host {
			return opts.RepoURL, nil
		}
		return fmt.Sprintf("git@%s:%s/%s.git", alias, owner, repo), nil
	}

	parsed, isHTTPS, err := parseHTTPSRepo(opts.RepoURL)
	if err != nil {
		return "", err
	}
	if !isHTTPS {
		return opts.RepoURL, nil
	}
	alias, err := ResolveAlias(inv, cfg.SSH, parsed.host, opts.User)
	if err != nil {
		return "", err
	}
	sshURL := fmt.Sprintf("git@%s:%s/%s.git", alias, parsed.owner, parsed.repo)
	if opts.ForceSSH {
		return sshURL, nil
	}
	inv.Warning("HTTPS repository URL detected; equivalent SSH URL: %s", sshURL)
	useSSH, err := inv.Confirm("Use SSH instead?", spec.ConfirmOptions{Default: spec.DefaultYes, Flag: "--ssh"})
	if err != nil {
		return "", err
	}
	if useSSH {
		return sshURL, nil
	}
	return opts.RepoURL, nil
}

// ParseSSHURL parses git@host:owner/repo[.git] into its components.
func ParseSSHURL(raw string) (host, owner, repo string, ok bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "git@") {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(raw, "git@")
	colonIdx := strings.Index(rest, ":")
	if colonIdx <= 0 {
		return "", "", "", false
	}
	host = rest[:colonIdx]
	path := strings.TrimSuffix(rest[colonIdx+1:], ".git")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	return host, parts[0], parts[1], true
}

// CanonicalHost reverses the alias maps so a URL like git@github-personal:...
// resolves back to its canonical host (e.g. github.com) for the next lookup.
// Returns the input host unchanged if no mapping points to it.
func CanonicalHost(ssh config.SSH, host string) string {
	for h, alias := range ssh.HostAliases {
		if alias == host {
			return h
		}
	}
	for _, hosts := range ssh.UserAliases {
		for h, alias := range hosts {
			if alias == host {
				return h
			}
		}
	}
	return host
}

// HTTPToSSH converts an HTTPS repo URL into its SSH equivalent using a fixed
// host->alias map. Kept for callers that don't need the user-aliases layer or
// interactive prompting; ResolveRepoURL goes through ResolveAlias instead.
func HTTPToSSH(raw string, aliases map[string]string) (string, bool, error) {
	parsed, isHTTPS, err := parseHTTPSRepo(raw)
	if err != nil {
		return "", false, err
	}
	if !isHTTPS {
		return raw, false, nil
	}
	alias := parsed.host
	if aliases != nil && aliases[parsed.host] != "" {
		alias = aliases[parsed.host]
	}
	return fmt.Sprintf("git@%s:%s/%s.git", alias, parsed.owner, parsed.repo), true, nil
}

type repoParts struct {
	host  string
	owner string
	repo  string
}

func parseHTTPSRepo(raw string) (repoParts, bool, error) {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return repoParts{}, false, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return repoParts{}, false, fmt.Errorf("parse repository URL: %v", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return repoParts{}, false, nil
	}
	host := parsed.Hostname()
	if host == "" {
		return repoParts{}, false, errors.New("repository URL host is required")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return repoParts{}, false, errors.New("HTTPS repository URL must look like https://<host>/<owner>/<repo>")
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	if repo == "" {
		return repoParts{}, false, errors.New("repository URL repo name is required")
	}
	return repoParts{host: host, owner: parts[0], repo: repo}, true, nil
}

// ResolveAlias picks the SSH alias for host given the SSH config and an
// optional --user flag. Resolution order:
//
//  1. user != "" and ssh.user_aliases[user][host] is set -> use it.
//  2. user != "" but no entry -> warn and fall back to host_aliases[host],
//     then to ~/.ssh/config.
//  3. user == "" and exactly one user has an entry for host -> use silently.
//  4. user == "" and multiple users have entries -> interactive prompt.
//  5. Otherwise -> host_aliases[host], then ~/.ssh/config, else host itself.
func ResolveAlias(inv *spec.Invocation, ssh config.SSH, host, user string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", errors.New("host is required")
	}
	user = strings.TrimSpace(user)
	if user != "" {
		if hosts, ok := ssh.UserAliases[user]; ok {
			if alias := strings.TrimSpace(hosts[host]); alias != "" {
				return alias, nil
			}
		}
		inv.Warning("user %q has no SSH alias configured for host %q; falling back to host_aliases", user, host)
		if alias := strings.TrimSpace(ssh.HostAliases[host]); alias != "" {
			return alias, nil
		}
		return resolveFromSSHConfig(inv, host)
	}

	var users []string
	for u, hosts := range ssh.UserAliases {
		if alias := strings.TrimSpace(hosts[host]); alias != "" {
			users = append(users, u)
		}
	}
	sort.Strings(users)

	switch len(users) {
	case 0:
		if alias := strings.TrimSpace(ssh.HostAliases[host]); alias != "" {
			return alias, nil
		}
		return resolveFromSSHConfig(inv, host)
	case 1:
		return ssh.UserAliases[users[0]][host], nil
	default:
		return promptForUserAlias(inv, ssh.UserAliases, host, users)
	}
}

func resolveFromSSHConfig(inv *spec.Invocation, host string) (string, error) {
	entries, err := findSSHAliasesFor(host)
	if err != nil {
		inv.Warning("could not read %s: %v", SSHConfigPath, err)
		return host, nil
	}
	if len(entries) == 0 {
		return host, nil
	}
	if len(entries) == 1 {
		inv.Info("using SSH alias %q from ~/.ssh/config (Hostname matches %s)", entries[0].Alias, host)
		return entries[0].Alias, nil
	}
	if !inv.Env.IsTerminal(spec.Stdin) {
		inv.Warning("multiple SSH aliases for %s in ~/.ssh/config but no gt alias configured; not rewriting URL", host)
		return host, nil
	}
	return promptForSSHConfigAlias(inv, host, entries)
}

func promptForSSHConfigAlias(inv *spec.Invocation, host string, entries []SSHHostEntry) (string, error) {
	_, _ = fmt.Fprintf(inv.Env.Stdout, "No gt alias configured for host %q.\n", host)
	_, _ = fmt.Fprintf(inv.Env.Stdout, "Found these SSH aliases in ~/.ssh/config:\n")
	for i, e := range entries {
		identity := e.Identity
		if identity == "" {
			identity = "<no IdentityFile>"
		}
		_, _ = fmt.Fprintf(inv.Env.Stdout, "  [%d] %s  (IdentityFile: %s)\n", i+1, e.Alias, identity)
	}
	keepIdx := len(entries) + 1
	_, _ = fmt.Fprintf(inv.Env.Stdout, "  [%d] keep %s (no rewrite)\n", keepIdx, host)
	answer, err := inv.Prompt(fmt.Sprintf("Pick one [1-%d]", keepIdx), spec.PromptOptions{Default: "1"})
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || n < 1 || n > keepIdx {
		return "", fmt.Errorf("invalid choice %q", answer)
	}
	if n == keepIdx {
		return host, nil
	}
	chosen := entries[n-1].Alias
	inv.Info("To persist this, add the following under ssh: in your gt config:\n  host_aliases:\n    %s: %s", host, chosen)
	return chosen, nil
}

func promptForUserAlias(inv *spec.Invocation, userAliases map[string]map[string]string, host string, users []string) (string, error) {
	_, _ = fmt.Fprintf(inv.Env.Stdout, "Multiple SSH aliases configured for host %s:\n", host)
	for i, u := range users {
		_, _ = fmt.Fprintf(inv.Env.Stdout, "  [%d] %s -> %s\n", i+1, u, userAliases[u][host])
	}
	answer, err := inv.Prompt(fmt.Sprintf("Pick a user [1-%d]", len(users)), spec.PromptOptions{
		Default: "1",
		Flag:    "--user",
	})
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || n < 1 || n > len(users) {
		return "", fmt.Errorf("invalid choice %q", answer)
	}
	return userAliases[users[n-1]][host], nil
}

func DeriveFolder(raw string) (string, error) {
	raw = strings.TrimSpace(strings.TrimSuffix(raw, "/"))
	if raw == "" {
		return "", errors.New("repository URL is required")
	}
	trimmed := strings.TrimSuffix(raw, ".git")
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Host != "" {
		name := strings.Trim(filepath.Base(parsed.Path), "/")
		if name != "" && name != "." {
			return name, nil
		}
	}
	name := filepath.Base(trimmed)
	if idx := strings.LastIndex(name, ":"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "", fmt.Errorf("could not derive folder name from %q", raw)
	}
	return name, nil
}

func ValidateFolder(folder string) error {
	if strings.TrimSpace(folder) == "" {
		return errors.New("folder cannot be empty")
	}
	if filepath.IsAbs(folder) {
		return errors.New("folder must be relative")
	}
	clean := filepath.Clean(folder)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("folder must stay within the current directory")
	}
	return nil
}

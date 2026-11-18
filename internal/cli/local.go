package cli

import (
	"context"

	"github.com/pedromvgomes/gt/internal/clone"
	"github.com/pedromvgomes/gt/internal/git"
	"github.com/pedromvgomes/gt/internal/scratch"
	"github.com/pedromvgomes/gt/internal/setauth"
	"github.com/pedromvgomes/gt/internal/setssh"
	"github.com/pedromvgomes/gt/internal/setup"
	"github.com/pedromvgomes/gt/internal/worktree"
	"go.vipengele.org/cli/spec"
)

func newCloneCommand() *spec.Command {
	var (
		ssh         = spec.BoolFlag("ssh", "force HTTPS repository URLs to SSH")
		noSSH       = spec.BoolFlag("no-ssh", "keep HTTPS repository URLs without prompting")
		noSetupAuth = spec.BoolFlag("no-setup-auth", "skip post-clone direnv authentication setup")
		user        = spec.StringFlag("user", "GitHub user (selects ssh.user_aliases entry and post-clone GH_TOKEN)")
		profile     = spec.StringFlag("profile", "environment profile to export from .envrc (overrides match patterns)")
		noProfile   = spec.BoolFlag("no-profile", "export no environment profile even if a match pattern applies")
		setupNames  = spec.StringFlag("setup", "comma-separated setup template names to run after clone")
		noSetup     = spec.BoolFlag("no-setup", "skip post-clone setup templates")
		yes         = spec.BoolFlag("yes", "skip the setup confirmation prompt")
		showSetup   = spec.BoolFlag("show-setup", "show full template bodies before prompting")
		dryRunSetup = spec.BoolFlag("dry-run-setup", "print the setup plan without executing it")
	)
	return &spec.Command{
		Name:    "clone",
		Summary: "Clone a repository into a bare repo + worktree layout",
		Args:    spec.RangeArgs(1, 2),
		Flags: []spec.Flag{
			ssh, noSSH, noSetupAuth, user, profile, noProfile,
			setupNames, noSetup, yes, showSetup, dryRunSetup,
		},
		FlagGroups: []spec.FlagGroup{
			exclusive("ssh", "no-ssh"),
			exclusive("setup", "no-setup"),
			exclusive("profile", "no-profile"),
		},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			cfg, err := configFrom(ctx)
			if err != nil {
				return err
			}
			opts := clone.Options{
				RepoURL:     inv.Args[0],
				ForceSSH:    ssh.Get(inv),
				NoSSH:       noSSH.Get(inv),
				NoSetupAuth: noSetupAuth.Get(inv),
				User:        user.Get(inv),
				Profile:     profile.Get(inv),
				NoProfile:   noProfile.Get(inv),
				SetupNames:  parseNames(setupNames.Get(inv)),
				NoSetup:     noSetup.Get(inv),
				YesSetup:    yes.Get(inv),
				ShowSetup:   showSetup.Get(inv),
				DryRunSetup: dryRunSetup.Get(inv),
			}
			if len(inv.Args) == 2 {
				opts.Folder = inv.Args[1]
			}
			return clone.Run(ctx, git.ExecRunner{}, inv, cfg, opts)
		},
	}
}

func newWorktreeCommand() *spec.Command {
	return &spec.Command{
		Name:    "wt",
		Aliases: []string{"worktree"},
		Summary: "Manage gt worktrees",
		Commands: []*spec.Command{
			newWorktreeAddCommand(),
			newWorktreeRemoveCommand(),
			newWorktreeListCommand(),
			newWorktreeNukeCommand(),
			newWorktreePruneBranchesCommand(),
		},
	}
}

func newWorktreeAddCommand() *spec.Command {
	var (
		from        = spec.StringFlag("from", "create the branch from this source branch")
		noSetup     = spec.BoolFlag("no-setup", "skip the repo's .gt.yaml setup templates")
		yes         = spec.BoolFlag("yes", "skip the setup confirmation prompt")
		showSetup   = spec.BoolFlag("show-setup", "show full template bodies before prompting")
		dryRunSetup = spec.BoolFlag("dry-run-setup", "print the setup plan without executing it")
	)
	return &spec.Command{
		Name:    "add",
		Summary: "Create a worktree for a typed branch",
		Args:    spec.ExactArgs(1),
		Flags:   []spec.Flag{from, noSetup, yes, showSetup, dryRunSetup},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			cfg, err := configFrom(ctx)
			if err != nil {
				return err
			}
			cwd, err := workDir(inv)
			if err != nil {
				return err
			}
			return worktree.Add(ctx, git.ExecRunner{}, inv, cfg, worktree.AddOptions{
				Spec:        inv.Args[0],
				CWD:         cwd,
				From:        from.Get(inv),
				NoSetup:     noSetup.Get(inv),
				YesSetup:    yes.Get(inv),
				ShowSetup:   showSetup.Get(inv),
				DryRunSetup: dryRunSetup.Get(inv),
			})
		},
	}
}

func newWorktreeRemoveCommand() *spec.Command {
	var (
		branch = spec.BoolFlag("branch", "also delete the local branch", spec.Shorthand("b"))
		force  = spec.BoolFlag("force", "remove without prompting, even with uncommitted changes", spec.Shorthand("f"))
	)
	return &spec.Command{
		Name:    "rm",
		Aliases: []string{"remove", "delete"},
		Summary: "Remove a worktree by name",
		Args:    spec.ExactArgs(1),
		Flags:   []spec.Flag{branch, force},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			cfg, err := configFrom(ctx)
			if err != nil {
				return err
			}
			cwd, err := workDir(inv)
			if err != nil {
				return err
			}
			return worktree.Remove(ctx, git.ExecRunner{}, inv, cfg, worktree.RemoveOptions{
				Name:         inv.Args[0],
				CWD:          cwd,
				DeleteBranch: branch.Get(inv),
				Force:        force.Get(inv),
			})
		},
	}
}

func newWorktreeListCommand() *spec.Command {
	return &spec.Command{
		Name:    "list",
		Summary: "List gt worktrees",
		Args:    spec.NoArgs(),
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			cfg, err := configFrom(ctx)
			if err != nil {
				return err
			}
			cwd, err := workDir(inv)
			if err != nil {
				return err
			}
			return worktree.List(ctx, git.ExecRunner{}, inv, cfg, cwd)
		},
	}
}

func newWorktreeNukeCommand() *spec.Command {
	var (
		branches = spec.BoolFlag("branches", "also delete corresponding local branches", spec.Shorthand("b"))
		force    = spec.BoolFlag("force", "force removal, discarding uncommitted changes in dirty worktrees", spec.Shorthand("f"))
	)
	return &spec.Command{
		Name:    "nuke",
		Summary: "Remove all typed worktrees",
		Args:    spec.NoArgs(),
		Flags:   []spec.Flag{branches, force},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			cfg, err := configFrom(ctx)
			if err != nil {
				return err
			}
			cwd, err := workDir(inv)
			if err != nil {
				return err
			}
			return worktree.Nuke(ctx, git.ExecRunner{}, inv, cfg, worktree.NukeOptions{
				CWD:            cwd,
				DeleteBranches: branches.Get(inv),
				Force:          force.Get(inv),
			})
		},
	}
}

func newWorktreePruneBranchesCommand() *spec.Command {
	dryRun := spec.BoolFlag("dry-run", "show what would be deleted without deleting", spec.Shorthand("n"))
	return &spec.Command{
		Name:    "prune-branches",
		Summary: "Delete local branches without active worktrees",
		Args:    spec.NoArgs(),
		Flags:   []spec.Flag{dryRun},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			cwd, err := workDir(inv)
			if err != nil {
				return err
			}
			return worktree.PruneBranches(ctx, git.ExecRunner{}, inv, worktree.PruneBranchesOptions{
				CWD:    cwd,
				DryRun: dryRun.Get(inv),
			})
		},
	}
}

func newScratchCommand() *spec.Command {
	var (
		reset = spec.BoolFlag("reset", "reset scratch to a different branch", spec.Shorthand("r"))
		del   = spec.BoolFlag("delete", "remove the scratch worktree and scratch branch", spec.Shorthand("d"))
	)
	return &spec.Command{
		Name:    "scratch",
		Summary: "Create or manage the scratch worktree",
		Args:    spec.NoArgs(),
		Flags:   []spec.Flag{reset, del},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			cwd, err := workDir(inv)
			if err != nil {
				return err
			}
			return scratch.Run(ctx, git.ExecRunner{}, inv, scratch.Options{
				CWD:    cwd,
				Reset:  reset.Get(inv),
				Delete: del.Get(inv),
			})
		},
	}
}

func newSetAuthCommand() *spec.Command {
	var (
		user      = spec.StringFlag("user", "GitHub user to bind GH_TOKEN to (default: the user already in .envrc)")
		profile   = spec.StringFlag("profile", "environment profile to export from .envrc (overrides match patterns)")
		noProfile = spec.BoolFlag("no-profile", "export no environment profile even if a match pattern applies")
		yes       = spec.BoolFlag("yes", "overwrite a differing .envrc without confirming")
	)
	return &spec.Command{
		Name:       "set-auth",
		Summary:    "Scope gh authentication and an environment profile to this repository with direnv",
		Args:       spec.NoArgs(),
		Flags:      []spec.Flag{user, profile, noProfile, yes},
		FlagGroups: []spec.FlagGroup{exclusive("profile", "no-profile")},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			cfg, err := configFrom(ctx)
			if err != nil {
				return err
			}
			cwd, err := workDir(inv)
			if err != nil {
				return err
			}
			return setauth.Run(ctx, setauth.ExecRunner{}, inv, setauth.Options{
				CWD:       cwd,
				Profiles:  cfg.Profiles,
				User:      user.Get(inv),
				Profile:   profile.Get(inv),
				NoProfile: noProfile.Get(inv),
				Yes:       yes.Get(inv),
			})
		},
	}
}

func newSetSSHRemoteCommand() *spec.Command {
	user := spec.StringFlag("user", "GitHub user whose SSH alias should be applied to origin")
	return &spec.Command{
		Name:    "set-ssh-remote",
		Summary: "Rewrite origin to use the SSH alias for a configured user",
		Args:    spec.NoArgs(),
		Flags:   []spec.Flag{user},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			cfg, err := configFrom(ctx)
			if err != nil {
				return err
			}
			cwd, err := workDir(inv)
			if err != nil {
				return err
			}
			return setssh.Run(ctx, git.ExecRunner{}, inv, cfg, setssh.Options{
				CWD:  cwd,
				User: user.Get(inv),
			})
		},
	}
}

func newSetupCommand() *spec.Command {
	var (
		names   = spec.StringFlag("setup", "comma-separated template names to run (overrides match)")
		noSetup = spec.BoolFlag("no-setup", "skip all templates")
		yes     = spec.BoolFlag("yes", "skip the confirmation prompt")
		from    = spec.StringFlag("from", "resume from this template name")
		show    = spec.BoolFlag("show", "show full template bodies before prompting")
		dryRun  = spec.BoolFlag("dry-run", "print the plan without executing it")
	)
	return &spec.Command{
		Name:       "setup",
		Summary:    "Run configured setup templates against the current git repository",
		Args:       spec.NoArgs(),
		Flags:      []spec.Flag{names, noSetup, yes, from, show, dryRun},
		FlagGroups: []spec.FlagGroup{exclusive("setup", "no-setup")},
		Run: func(ctx context.Context, inv *spec.Invocation) error {
			cfg, err := configFrom(ctx)
			if err != nil {
				return err
			}
			cwd, err := workDir(inv)
			if err != nil {
				return err
			}
			rcontext, err := setup.DetectFromCWD(ctx, git.ExecRunner{}, cwd)
			if err != nil {
				return err
			}
			templates, err := setup.Select(cfg.Setup, rcontext.RepoURL, setup.SelectOptions{
				NoSetup: noSetup.Get(inv),
				Names:   parseNames(names.Get(inv)),
			})
			if err != nil {
				return err
			}
			plan := setup.Plan{Templates: templates, Ctx: rcontext}
			return setup.Execute(ctx, inv, plan, setup.RunOptions{
				Yes:    yes.Get(inv),
				Show:   show.Get(inv),
				DryRun: dryRun.Get(inv),
				From:   from.Get(inv),
			})
		},
	}
}

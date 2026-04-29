package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/ui"
)

// Plan is the resolved set of templates to run, with variables already
// computed from the repository context.
type Plan struct {
	Templates []config.Template
	Ctx       Context
}

// RunOptions controls how Execute behaves.
type RunOptions struct {
	Yes    bool   // skip the confirmation prompt
	Show   bool   // jump straight to the detailed view
	DryRun bool   // print plan, do not execute
	From   string // start with this template (skip earlier ones)
}

// Execute runs the plan, printing it first and (for interactive sessions)
// prompting the user to confirm. On the first non-zero exit it stops and
// returns a directive to resume with `gt setup --from <name>`.
func Execute(ctx context.Context, printer *ui.UI, plan Plan, opts RunOptions) error {
	templates := plan.Templates
	if opts.From != "" {
		idx := -1
		for i, t := range templates {
			if t.Name == opts.From {
				idx = i
				break
			}
		}
		if idx == -1 {
			return ui.Errorf(ui.ExitUser, "template %q is not in the resolved plan", opts.From)
		}
		templates = templates[idx:]
	}

	if len(templates) == 0 {
		printer.Info("No setup templates apply.")
		return nil
	}

	if opts.Show {
		printDetailed(printer, templates, plan.Ctx.Vars())
	} else {
		printTerse(printer, templates, plan.Ctx.Root)
	}

	if opts.DryRun {
		return nil
	}

	if !opts.Yes {
		ok, err := promptConfirm(printer, templates, plan.Ctx.Vars())
		if err != nil {
			return err
		}
		if !ok {
			printer.Info("Aborted before running setup")
			return nil
		}
	}

	for i, t := range templates {
		_, _ = fmt.Fprintf(printer.Out, "\n==> [%d/%d] %s\n", i+1, len(templates), t.Name)
		if err := runTemplate(ctx, printer, t, plan.Ctx); err != nil {
			return ui.Errorf(ui.ExitGeneral,
				"setup template %q failed: %v\n  resume with: gt setup --from %s",
				t.Name, err, t.Name)
		}
	}
	printer.Success("Setup complete")
	return nil
}

func printTerse(printer *ui.UI, templates []config.Template, root string) {
	location := root
	if location == "" {
		location = "(current repo)"
	}
	_, _ = fmt.Fprintf(printer.Out, "Will run %d setup template(s) at %s:\n", len(templates), location)
	width := nameWidth(templates)
	for i, t := range templates {
		match := strings.Join(t.Match, ", ")
		if match == "" {
			match = "(explicit)"
		}
		body := "run"
		if t.Script != "" {
			body = "script: " + t.Script
		}
		_, _ = fmt.Fprintf(printer.Out, "  %d. %-*s  (match: %s)  %s\n", i+1, width, t.Name, match, body)
	}
}

func printDetailed(printer *ui.UI, templates []config.Template, vars map[string]string) {
	_, _ = fmt.Fprintf(printer.Out, "Will run %d setup template(s):\n", len(templates))
	for i, t := range templates {
		_, _ = fmt.Fprintf(printer.Out, "\n%d. %s\n", i+1, t.Name)
		if len(t.Match) > 0 {
			_, _ = fmt.Fprintf(printer.Out, "   match: %s\n", strings.Join(t.Match, ", "))
		}
		if t.Run != "" {
			_, _ = fmt.Fprintln(printer.Out, "   run: |")
			for line := range strings.SplitSeq(strings.TrimRight(t.Run, "\n"), "\n") {
				_, _ = fmt.Fprintf(printer.Out, "     %s\n", line)
			}
			continue
		}
		resolved := substituteVars(t.Script, vars)
		_, _ = fmt.Fprintf(printer.Out, "   script: %s\n", resolved)
		if data, err := os.ReadFile(resolved); err == nil {
			for line := range strings.SplitSeq(strings.TrimRight(string(data), "\n"), "\n") {
				_, _ = fmt.Fprintf(printer.Out, "     %s\n", line)
			}
		} else {
			_, _ = fmt.Fprintf(printer.Out, "     (could not read: %v)\n", err)
		}
	}
}

func promptConfirm(printer *ui.UI, templates []config.Template, vars map[string]string) (bool, error) {
	if !printer.Interactive {
		return true, nil
	}
	for {
		answer, err := printer.Prompt("Run these? [Y/n/d] (d = show details)", "Y", false, "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "d", "details":
			printDetailed(printer, templates, vars)
		default:
			printer.Warn("Please answer y, n, or d")
		}
	}
}

func runTemplate(ctx context.Context, printer *ui.UI, t config.Template, c Context) error {
	var cmd *exec.Cmd
	switch {
	case strings.TrimSpace(t.Run) != "":
		cmd = exec.CommandContext(ctx, "sh", "-c", t.Run)
	case strings.TrimSpace(t.Script) != "":
		path := substituteVars(t.Script, c.Vars())
		if path == "" {
			return fmt.Errorf("script path resolved to empty string")
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("stat script %s: %w", path, err)
		}
		cmd = exec.CommandContext(ctx, "sh", path)
	default:
		return fmt.Errorf("template has neither run nor script")
	}
	cmd.Stdout = printer.Out
	cmd.Stderr = printer.Err
	cmd.Env = c.Env()
	if c.WorkDir != "" {
		if _, err := os.Stat(c.WorkDir); err == nil {
			cmd.Dir = c.WorkDir
		} else {
			cmd.Dir = c.Root
		}
	}
	return cmd.Run()
}

func substituteVars(s string, vars map[string]string) string {
	return os.Expand(s, func(key string) string {
		if v, ok := vars[key]; ok {
			return v
		}
		return os.Getenv(key)
	})
}

func nameWidth(templates []config.Template) int {
	w := 0
	for _, t := range templates {
		if len(t.Name) > w {
			w = len(t.Name)
		}
	}
	return w
}

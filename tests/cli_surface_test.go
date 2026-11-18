package tests

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	gtcli "github.com/pedromvgomes/gt/internal/cli"
	"go.vipengele.org/cli/spec"
)

// testdata/cli-surface.golden is the command-line surface of gt's own
// commands: every command path with its aliases, summary, argument policy,
// flags (shorthand, kind, default, usage) and mutually exclusive flag groups.
// It is the contract scripts and muscle memory depend on, so the file is
// edited by hand when the surface is meant to change, never regenerated from
// the tree it checks. The root, its standard flags and the "config",
// "version" and "completion" commands belong to the vipengele App, not to
// internal/cli, and are not part of it.
func TestCLISurfaceMatchesGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/cli-surface.golden")
	if err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	cmds := gtcli.Commands(gtcli.Deps{Version: "dev"})
	sortCommands(cmds)
	for _, cmd := range cmds {
		writeSurface(&b, cmd, "gt "+cmd.Name)
	}

	if got := b.String(); got != string(want) {
		t.Fatalf("command surface differs from testdata/cli-surface.golden:\n%s", lineDiff(string(want), got))
	}
}

// TestCLICommandsValidate checks the tree the way the App checks it before
// every run, so an invalid tree fails here rather than at a user's prompt.
func TestCLICommandsValidate(t *testing.T) {
	root := &spec.Command{Name: "gt", Commands: gtcli.Commands(gtcli.Deps{Version: "dev"})}
	if err := root.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func sortCommands(cmds []*spec.Command) {
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
}

func writeSurface(b *strings.Builder, cmd *spec.Command, path string) {
	fmt.Fprintf(b, "command %s\n", path)
	if len(cmd.Aliases) == 0 {
		fmt.Fprintf(b, "  aliases: -\n")
	} else {
		fmt.Fprintf(b, "  aliases: %s\n", strings.Join(cmd.Aliases, ", "))
	}
	fmt.Fprintf(b, "  summary: %s\n", cmd.Summary)
	if cmd.Run == nil {
		fmt.Fprintf(b, "  args: -\n")
	} else {
		fmt.Fprintf(b, "  args: %s\n", cmd.Args)
	}

	flags := slices.Clone(cmd.Flags)
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name() < flags[j].Name() })
	for _, f := range flags {
		name := "--" + f.Name()
		if f.Shorthand() != "" {
			name = "-" + f.Shorthand() + ", " + name
		}
		def := fmt.Sprint(f.DefaultValue())
		if f.Kind() == spec.StringKind {
			def = fmt.Sprintf("%q", def)
		}
		inherited := ""
		if f.Inherited() {
			inherited = " inherited"
		}
		fmt.Fprintf(b, "  flag %s %s default=%s%s %q\n", name, f.Kind(), def, inherited, f.Usage())
	}

	var groups []string
	for _, g := range cmd.FlagGroups {
		names := slices.Clone(g.Flags)
		sort.Strings(names)
		kind := "exclusive"
		if g.Kind != spec.MutuallyExclusive {
			kind = g.Kind.String()
		}
		groups = append(groups, kind+": "+strings.Join(names, " "))
	}
	sort.Strings(groups)
	for _, g := range groups {
		fmt.Fprintf(b, "  %s\n", g)
	}

	subs := slices.Clone(cmd.Commands)
	sortCommands(subs)
	for _, sub := range subs {
		writeSurface(b, sub, path+" "+sub.Name)
	}
}

// lineDiff lists the lines only one side has, each marked with the side it
// comes from, so a failure names the commands and flags that moved.
func lineDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	var out strings.Builder
	for _, l := range wantLines {
		if !slices.Contains(gotLines, l) {
			fmt.Fprintf(&out, "- %s\n", l)
		}
	}
	for _, l := range gotLines {
		if !slices.Contains(wantLines, l) {
			fmt.Fprintf(&out, "+ %s\n", l)
		}
	}
	if out.Len() == 0 {
		return "(same lines, different order)"
	}
	return out.String()
}

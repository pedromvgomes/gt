package repogov

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pedromvgomes/gt/internal/repospec"
	"gopkg.in/yaml.v3"
)

// WorkflowDir is where GitHub looks for workflow definitions.
const WorkflowDir = ".github/workflows"

// maxUsesDepth bounds reusable-workflow resolution. Callers calling callers is
// legal but rare; the cap stops a cyclic `uses:` graph from hanging the lint.
const maxUsesDepth = 3

// Finding is one lint problem. The gate's aggregation is only as trustworthy
// as these: a required check that can never appear would otherwise be
// indistinguishable at runtime from one that simply has not started yet.
type Finding struct {
	// Check is the check name the finding concerns, when it concerns one.
	Check string
	// Path is the workflow file at fault, when there is one.
	Path    string
	Message string
}

func (f Finding) String() string {
	switch {
	case f.Path != "" && f.Check != "":
		return fmt.Sprintf("%s (check %q): %s", f.Path, f.Check, f.Message)
	case f.Path != "":
		return fmt.Sprintf("%s: %s", f.Path, f.Message)
	case f.Check != "":
		return fmt.Sprintf("check %q: %s", f.Check, f.Message)
	default:
		return f.Message
	}
}

// workflow is the subset of a workflow file the lint needs.
type workflow struct {
	Path string
	Name string         `yaml:"name"`
	On   any            `yaml:"on"`
	Jobs map[string]job `yaml:"jobs"`
}

type job struct {
	Name string `yaml:"name"`
	Uses string `yaml:"uses"`
}

// prTrigger is the parsed pull_request trigger of a workflow.
type prTrigger struct {
	Present     bool
	Paths       []string
	PathsIgnore []string
}

// Lint checks that every name in checks.required can actually appear on a PR.
//
// Two failure modes matter, and both are invisible at runtime:
//
//   - A name no workflow produces (a typo, or a job renamed out from under the
//     spec) makes the gate wait the full timeout and then fail.
//   - A workflow gated by a top-level paths: filter produces no check run at
//     all when the filter does not match, which the gate cannot distinguish
//     from "not started yet". The fix is wardnet's shape: trigger the workflow
//     unconditionally and skip individual jobs with job-level if:, so every
//     job still reports a conclusion (skipped counts as a pass).
//
// checks.optional is exempt from both — declaring a check optional is exactly
// the statement that it may legitimately not appear.
func Lint(workdir string, spec repospec.Spec) ([]Finding, error) {
	workflows, err := loadWorkflows(workdir)
	if err != nil {
		return nil, err
	}

	// producers maps a check name to the workflow that emits it.
	producers := map[string]*workflow{}
	unresolved := false
	for _, wf := range workflows {
		names, hadUnresolved := checkNames(wf, workflows, 0)
		unresolved = unresolved || hadUnresolved
		for _, n := range names {
			if _, taken := producers[n]; !taken {
				producers[n] = wf
			}
		}
	}

	var findings []Finding
	for _, name := range spec.Checks.Required {
		// A matrix job produces one check per matrix value, each with the
		// expression already expanded ("Build (arm64)"). The literal template
		// text is never a real check name, so the gate would wait the full
		// timeout for something that cannot arrive.
		if IsTemplated(name) {
			findings = append(findings, Finding{
				Check: name,
				Message: "check name contains an unexpanded ${{ }} expression; a matrix job reports one check per " +
					"value with the expression already substituted, so list the expanded names instead",
			})
			continue
		}
		wf, ok := producers[name]
		if !ok {
			// A remote `uses:` we could not read might be the producer, so a
			// missing name is only reported as certainly-wrong when every
			// reference resolved.
			if unresolved {
				continue
			}
			findings = append(findings, Finding{
				Check:   name,
				Message: "no workflow produces this check; the gate would wait for it and then fail at timeout",
			})
			continue
		}
		trigger := parsePRTrigger(wf.On)
		if !trigger.Present {
			findings = append(findings, Finding{
				Check: name, Path: wf.Path,
				Message: "workflow does not trigger on pull_request, so this check never appears on a PR",
			})
			continue
		}
		if len(trigger.Paths) > 0 || len(trigger.PathsIgnore) > 0 {
			filter := "paths"
			if len(trigger.PathsIgnore) > 0 {
				filter = "paths-ignore"
			}
			findings = append(findings, Finding{
				Check: name, Path: wf.Path,
				Message: fmt.Sprintf(
					"workflow has a top-level pull_request %s filter, so this check is absent (not skipped) when the filter misses; "+
						"drop the filter and gate the jobs with job-level if:, or move this check to checks.optional", filter),
			})
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Check != findings[j].Check {
			return findings[i].Check < findings[j].Check
		}
		return findings[i].Path < findings[j].Path
	})
	return findings, nil
}

// IsTemplated reports whether a check name still carries a GitHub Actions
// expression, meaning it is a template for a family of real check names rather
// than one itself.
func IsTemplated(name string) bool {
	return strings.Contains(name, "${{")
}

// checkNames returns the check-run names a workflow produces, and whether any
// reference could not be resolved.
//
// A plain job reports under its own name — wardnet's required check is
// "All checks passed", the name of the job, with no workflow prefix. A job
// that `uses:` a reusable workflow instead reports one check per job in the
// called workflow, named "<caller job> / <called job>".
func checkNames(wf *workflow, all map[string]*workflow, depth int) ([]string, bool) {
	var names []string
	unresolved := false
	for id, j := range wf.Jobs {
		caller := j.Name
		if caller == "" {
			caller = id
		}
		if j.Uses == "" {
			names = append(names, caller)
			continue
		}
		called, ok := resolveLocalUses(j.Uses, all)
		if !ok || depth >= maxUsesDepth {
			// Remote or too-deep reference: we cannot enumerate its jobs.
			unresolved = true
			continue
		}
		inner, innerUnresolved := checkNames(called, all, depth+1)
		unresolved = unresolved || innerUnresolved
		for _, n := range inner {
			names = append(names, caller+" / "+n)
		}
	}
	sort.Strings(names)
	return names, unresolved
}

// resolveLocalUses resolves a `uses:` value that points at a workflow in this
// repository. Remote references (owner/repo/.github/workflows/x.yml@ref) are
// not resolvable without fetching, so they report as unresolved.
func resolveLocalUses(uses string, all map[string]*workflow) (*workflow, bool) {
	if !strings.HasPrefix(uses, "./") {
		return nil, false
	}
	rel := strings.TrimPrefix(uses, "./")
	wf, ok := all[rel]
	return wf, ok
}

// parsePRTrigger extracts the pull_request trigger. `on:` may be a bare string,
// a list of event names, or a mapping; only the mapping form can carry filters.
func parsePRTrigger(on any) prTrigger {
	switch v := on.(type) {
	case string:
		return prTrigger{Present: v == "pull_request"}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == "pull_request" {
				return prTrigger{Present: true}
			}
		}
		return prTrigger{}
	case map[string]any:
		raw, ok := v["pull_request"]
		if !ok {
			return prTrigger{}
		}
		t := prTrigger{Present: true}
		body, ok := raw.(map[string]any)
		if !ok {
			// `pull_request:` with an empty body means all defaults, no filters.
			return t
		}
		t.Paths = stringList(body["paths"])
		t.PathsIgnore = stringList(body["paths-ignore"])
		return t
	default:
		return prTrigger{}
	}
}

func stringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// loadWorkflows parses every workflow under .github/workflows, keyed by the
// repo-relative path so `uses: ./.github/workflows/x.yml` resolves directly.
// A file that fails to parse is skipped rather than fatal: the lint's job is
// to report check-name problems, and GitHub will surface a malformed workflow
// on its own.
func loadWorkflows(workdir string) (map[string]*workflow, error) {
	dir := filepath.Join(workdir, filepath.FromSlash(WorkflowDir))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]*workflow{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", WorkflowDir, err)
	}

	out := make(map[string]*workflow, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yml" && ext != ".yaml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read workflow %s: %w", e.Name(), err)
		}
		var wf workflow
		if err := yaml.Unmarshal(data, &wf); err != nil {
			continue
		}
		wf.Path = WorkflowDir + "/" + e.Name()
		out[wf.Path] = &wf
	}
	return out, nil
}

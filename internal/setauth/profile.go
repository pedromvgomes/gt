package setauth

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pedromvgomes/gt/internal/config"
	"github.com/pedromvgomes/gt/internal/setup"
	"github.com/pedromvgomes/gt/internal/ui"
)

// EnvVar is one resolved export line. Values are already expanded; escaping
// happens at render time.
type EnvVar struct {
	Name  string
	Value string
}

// Profile is a resolved selection: a name for the .envrc comment and the
// variables to export. The zero value exports nothing, which is what every
// repository that has not opted in resolves to.
type Profile struct {
	Name string
	Env  []EnvVar
}

// Empty reports whether the profile contributes nothing to the .envrc. It is
// the case that must keep producing byte-identical output to pre-profile gt.
func (p Profile) Empty() bool {
	return len(p.Env) == 0
}

// ResolveProfile picks the profile for a repository.
//
// Precedence: --no-profile, then an explicit --profile name, then the first
// configured profile whose match patterns accept repoURL. Anything else
// resolves to the empty profile. There is deliberately no prompt: set-auth
// runs from clone hooks and CI, where a question is a hang, and the config
// already holds the answer.
func ResolveProfile(profiles []config.Profile, repoURL, explicit string, noProfile bool) (Profile, error) {
	if noProfile {
		return Profile{}, nil
	}
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		if explicit == config.ProfileNone {
			return Profile{}, nil
		}
		for _, p := range profiles {
			if p.Name == explicit {
				return resolve(p)
			}
		}
		return Profile{}, ui.Errorf(ui.ExitUser, "unknown profile %q", explicit)
	}
	for _, p := range profiles {
		for _, pattern := range p.Match {
			if setup.MatchURL(pattern, repoURL) {
				return resolve(p)
			}
		}
	}
	return Profile{}, nil
}

func resolve(p config.Profile) (Profile, error) {
	// Sorted so a config edit that only reorders a YAML map cannot make the
	// .envrc "change" and re-prompt for an overwrite.
	names := make([]string, 0, len(p.Env))
	for name := range p.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	out := Profile{Name: p.Name, Env: make([]EnvVar, 0, len(names))}
	for _, name := range names {
		value, err := expandHome(p.Env[name])
		if err != nil {
			return Profile{}, fmt.Errorf("profile %q: %s: %w", p.Name, name, err)
		}
		out.Env = append(out.Env, EnvVar{Name: name, Value: value})
	}
	return out, nil
}

// expandHome resolves a leading ~/ or $HOME so the .envrc carries an absolute
// path that reads the same from any working directory. It is the only
// expansion gt performs; everything else is emitted literally.
func expandHome(value string) (string, error) {
	var rest string
	switch {
	case value == "~" || value == "$HOME":
		rest = ""
	case strings.HasPrefix(value, "~/"):
		rest = value[2:]
	case strings.HasPrefix(value, "$HOME/"):
		rest = value[len("$HOME/"):]
	default:
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if rest == "" {
		return home, nil
	}
	return filepath.Join(home, rest), nil
}

// shellDoubleQuote renders value as a double-quoted shell word. Profile values
// are arbitrary strings, so the characters the shell still reads inside double
// quotes are escaped rather than refused.
func shellDoubleQuote(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + replacer.Replace(value) + `"`
}

// Names lists the variable names the profile exports, for the summary line.
func (p Profile) Names() []string {
	out := make([]string, 0, len(p.Env))
	for _, v := range p.Env {
		out = append(out, v.Name)
	}
	return out
}

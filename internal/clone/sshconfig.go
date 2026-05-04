package clone

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// SSHHostEntry represents a single 'Host <alias>' block from ~/.ssh/config.
type SSHHostEntry struct {
	Alias    string
	Hostname string
	Identity string
}

// SSHConfigPath is the path to the user's SSH client config. Tests may
// override it; an empty value disables the lookup.
var SSHConfigPath = defaultSSHConfigPath()

func defaultSSHConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

// findSSHAliasesFor scans SSHConfigPath and returns Host blocks whose
// resolved Hostname matches target (case-insensitive). Wildcard Host
// patterns and Match/Include directives are ignored.
func findSSHAliasesFor(target string) ([]SSHHostEntry, error) {
	if SSHConfigPath == "" {
		return nil, nil
	}
	f, err := os.Open(SSHConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var (
		entries []SSHHostEntry
		current []string
		entry   SSHHostEntry
		inBlock bool
	)
	flush := func() {
		if !inBlock {
			return
		}
		for _, alias := range current {
			if alias == "" || strings.ContainsAny(alias, "*?!") {
				continue
			}
			resolved := entry.Hostname
			if resolved == "" {
				resolved = alias
			}
			if !strings.EqualFold(resolved, target) {
				continue
			}
			entries = append(entries, SSHHostEntry{
				Alias:    alias,
				Hostname: resolved,
				Identity: entry.Identity,
			})
		}
		inBlock = false
		current = nil
		entry = SSHHostEntry{}
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value := splitSSHConfigLine(line)
		if key == "" {
			continue
		}
		switch strings.ToLower(key) {
		case "host":
			flush()
			current = strings.Fields(value)
			inBlock = true
		case "match", "include":
			// Match-blocks have their own scoping rules and Include can
			// pull in arbitrary files. Closing the current block keeps
			// us from attaching unrelated keywords to it; we don't
			// follow the directive.
			flush()
		case "hostname":
			if inBlock {
				entry.Hostname = value
			}
		case "identityfile":
			if inBlock && entry.Identity == "" {
				entry.Identity = expandHome(value)
			}
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// splitSSHConfigLine splits a config line on the first whitespace or '='
// run. SSH treats both as the keyword/argument separator.
func splitSSHConfigLine(line string) (string, string) {
	idx := strings.IndexAny(line, " \t=")
	if idx < 0 {
		return line, ""
	}
	key := line[:idx]
	rest := strings.TrimLeft(line[idx:], " \t=")
	return key, strings.TrimSpace(rest)
}

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

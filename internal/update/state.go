package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	LastCheckedAt   time.Time `json:"last_checked_at"`
	LatestSeen      string    `json:"latest_seen"`
	LatestSeenAtRaw time.Time `json:"latest_seen_at"`
}

func StatePath() (string, error) {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "gt", "update.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".cache", "gt", "update.json"), nil
}

func LoadState(path string) (State, error) {
	var s State
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("read state: %w", err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, nil
	}
	return s, nil
}

func SaveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// DueForCheck reports whether enough time has elapsed since the last
// check to perform another one.
func DueForCheck(s State, now time.Time, interval time.Duration) bool {
	if s.LastCheckedAt.IsZero() {
		return true
	}
	return now.Sub(s.LastCheckedAt) >= interval
}

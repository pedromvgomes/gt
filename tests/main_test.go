package tests

import (
	"os"
	"testing"

	"github.com/pedromvgomes/gt/internal/clone"
)

// TestMain disables the SSH-config fallback by default so tests don't
// pick up the developer's real ~/.ssh/config. Tests that exercise the
// fallback set clone.SSHConfigPath explicitly.
func TestMain(m *testing.M) {
	clone.SSHConfigPath = ""
	os.Exit(m.Run())
}

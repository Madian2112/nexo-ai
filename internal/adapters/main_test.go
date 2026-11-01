package adapters

import (
	"os"
	"testing"
)

// Tests expect OpenCode under ~/.config/opencode: ignore the developer's XDG_CONFIG_HOME.
func TestMain(m *testing.M) {
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Exit(m.Run())
}

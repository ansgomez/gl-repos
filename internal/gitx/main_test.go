package gitx

import (
	"os"
	"testing"
)

// Tests must not depend on the developer's git config (commit signing,
// hooks, default branch names).
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}

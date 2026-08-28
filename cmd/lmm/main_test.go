package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMain makes every test in this package hermetic regardless of run order
// or -run subset (issue #115). The configDir/dataDir package globals default
// to the user's real ~/.config/lmm and ~/.local/share/lmm when empty, so
// tests that never assign them (the "no game specified" early-return tests)
// would otherwise read — and could write — the developer's real lmm state.
// Tests that need isolated state still assign their own t.TempDir() dirs;
// these shared throwaway dirs are only the floor under everything else.
func TestMain(m *testing.M) {
	cfg, err := os.MkdirTemp("", "lmm-test-config-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating test config dir: %v\n", err)
		os.Exit(1)
	}
	data, err := os.MkdirTemp("", "lmm-test-data-")
	if err != nil {
		_ = os.RemoveAll(cfg)
		fmt.Fprintf(os.Stderr, "creating test data dir: %v\n", err)
		os.Exit(1)
	}
	configDir = cfg
	dataDir = data

	code := m.Run()

	_ = os.RemoveAll(cfg)
	_ = os.RemoveAll(data)
	os.Exit(code)
}

// TestPackageGlobals_HermeticDefaults pins the test-isolation guard for this
// package (issue #115). getServiceConfig treats empty configDir/dataDir as
// "use the real ~/.config/lmm and ~/.local/share/lmm", so any test that runs
// while the globals are unset escapes the sandbox — a subset run like
// `go test ./cmd/lmm/... -run TestInstallCmd` used to hit the user's real
// config and stall on an interactive source-picker prompt. TestMain must
// point both globals at throwaway temp dirs before any test runs; if this
// test fails in a subset run, that guard has been removed or broken.
func TestPackageGlobals_HermeticDefaults(t *testing.T) {
	require.NotEmpty(t, configDir,
		"configDir is empty: getServiceConfig would fall back to the real $XDG_CONFIG_HOME/lmm (~/.config/lmm)")
	require.NotEmpty(t, dataDir,
		"dataDir is empty: getServiceConfig would fall back to the real $XDG_DATA_HOME/lmm (~/.local/share/lmm)")
}

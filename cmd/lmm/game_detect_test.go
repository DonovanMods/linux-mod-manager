package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunGameDetect_OpensServiceOnFreshInstall guards C1 (#279 Unit B final
// review): runGameDetect used to open its *core.Service via a raw
// core.NewService call, bypassing app.Open's directory preparation. On a
// fresh install - where DataDir does not exist yet - that made 'lmm game
// detect' fail with "unable to open database file", even though it is
// typically the first command a new user runs. configDir/dataDir here point
// at nested paths t.TempDir() never created, reproducing a fresh install;
// HOME is isolated to a fresh temp dir so the test never picks up a real
// Steam library on the machine running it, which would divert the flow into
// reading stdin for a selection.
func TestRunGameDetect_OpensServiceOnFreshInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configDir = filepath.Join(t.TempDir(), "config", "nested")
	dataDir = filepath.Join(t.TempDir(), "data", "nested")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var buf strings.Builder
	cmd.SetOut(&buf)

	err := runGameDetect(cmd, nil)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No moddable Steam games found.")
}

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

// TestRunGameDetect_OnlyUncuratedGamesInstalled_NamesTheFlag pins Important
// 3 of the unit9 review (#206): a library whose only installed games are
// uncurated made plain `lmm game detect` end at "No moddable Steam games
// found." with nothing pointing at --include-unknown - the literal scenario
// the flag exists for, and the one place the CLI and `lmm serve` (which
// always scans wide) genuinely diverged in what a user could discover.
func TestRunGameDetect_OnlyUncuratedGamesInstalled_NamesTheFlag(t *testing.T) {
	configDir = filepath.Join(t.TempDir(), "config", "nested")
	dataDir = filepath.Join(t.TempDir(), "data", "nested")
	fakeSteamGame(t, "777777", "Uncurated Game", "UncuratedGame")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var buf strings.Builder
	cmd.SetOut(&buf)

	err := runGameDetect(cmd, nil)

	require.NoError(t, err)
	out := buf.String()
	assert.NotContains(t, out, "No moddable Steam games found.",
		"the game is right there; this must not read like nothing was found")
	assert.Contains(t, out,
		"1 installed game(s) are not in the known-games list; run `lmm game detect --include-unknown` to add them.")
}

package main

import (
	"bufio"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/steam"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func detectedGamesFixture() []steam.DetectedGame {
	return []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
		{Slug: "starrupture", Name: "Star Rupture", InstallPath: "/games/starrupture", NexusID: "starrupture"},
		{Slug: "icarus", Name: "Icarus", InstallPath: "/games/icarus", Sources: map[string]string{"icarus": "icarus"}},
	}
}

// TestGameDetectSelectionIndices_AllExcludesConfigured pins #205 item 2:
// "all" defaults to only the NOT-yet-configured games.
func TestGameDetectSelectionIndices_AllExcludesConfigured(t *testing.T) {
	games := detectedGamesFixture()
	existing := map[string]*domain.Game{"skyrim-se": {ID: "skyrim-se"}}

	indices, err := gameDetectSelectionIndices("all", games, existing)
	require.NoError(t, err)
	assert.Equal(t, []int{2, 3}, indices)
}

// TestGameDetectSelectionIndices_AllWhenNoneConfigured is the pre-#205
// baseline: nothing configured yet, "all" still selects everything.
func TestGameDetectSelectionIndices_AllWhenNoneConfigured(t *testing.T) {
	games := detectedGamesFixture()

	indices, err := gameDetectSelectionIndices("all", games, map[string]*domain.Game{})
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, indices)
}

// TestGameDetectSelectionIndices_AllWhenEveryGameConfigured pins the empty
// result when every detected game is already configured.
func TestGameDetectSelectionIndices_AllWhenEveryGameConfigured(t *testing.T) {
	games := detectedGamesFixture()
	existing := map[string]*domain.Game{
		"skyrim-se":   {ID: "skyrim-se"},
		"starrupture": {ID: "starrupture"},
		"icarus":      {ID: "icarus"},
	}

	indices, err := gameDetectSelectionIndices("all", games, existing)
	require.NoError(t, err)
	assert.Empty(t, indices)
}

// TestGameDetectSelectionIndices_ExplicitSelectionIncludesConfigured pins
// the repair path (#205 item 2): explicitly naming an already-configured
// game's number still selects it - this is how a user deliberately
// re-adds/repairs a game, mirroring 'lmm game add's unconditional overwrite.
func TestGameDetectSelectionIndices_ExplicitSelectionIncludesConfigured(t *testing.T) {
	games := detectedGamesFixture()
	existing := map[string]*domain.Game{"skyrim-se": {ID: "skyrim-se"}}

	indices, err := gameDetectSelectionIndices("1,2", games, existing)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, indices)
}

func TestGameDetectSelectionIndices_NoneAndEmpty(t *testing.T) {
	games := detectedGamesFixture()
	for _, in := range []string{"", "n", "none", "NONE"} {
		indices, err := gameDetectSelectionIndices(in, games, nil)
		require.NoError(t, err)
		assert.Empty(t, indices, "input %q", in)
	}
}

func TestGameDetectSelectionIndices_InvalidSelection(t *testing.T) {
	games := detectedGamesFixture()
	_, err := gameDetectSelectionIndices("99", games, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid selection")
}

// TestDoGameDetect_MarksConfiguredGamesAndExcludesFromAll drives the full
// interactive flow with a stubbed detected-games list (no real Steam scan)
// against a configDir that already has skyrim-se configured: the printed
// list marks it [configured], and answering "all" adds only starrupture.
func TestDoGameDetect_MarksConfiguredGamesAndExcludesFromAll(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Skyrim Special Edition"}))

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
		{Slug: "starrupture", Name: "Star Rupture", InstallPath: "/games/starrupture", NexusID: "starrupture"},
	}

	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader("all\n"))

	err := doGameDetect(cmd, reader, configDir, games)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, lineContaining(out, "skyrim-se"), "[configured]")
	assert.NotContains(t, lineContaining(out, "starrupture"), "[configured]")
	assert.Contains(t, out, "Added: Star Rupture (starrupture)")
	assert.NotContains(t, out, "Added: Skyrim Special Edition")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	_, ok := saved["starrupture"]
	assert.True(t, ok)
}

// TestDoGameDetect_ExplicitSelectionRepairsConfiguredGame pins the repair
// path end to end: naming an already-configured game's number re-saves it.
func TestDoGameDetect_ExplicitSelectionRepairsConfiguredGame(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Stale Name"}))

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
	}

	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader("1\n"))

	err := doGameDetect(cmd, reader, configDir, games)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Added: Skyrim Special Edition (skyrim-se)")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "skyrim-se")
	assert.Equal(t, "Skyrim Special Edition", saved["skyrim-se"].Name)
}

// TestDoGameDetect_AllExcludedPrintsFriendlyMessage guards the fully-
// configured case: "all" selects nothing, and the message says so instead
// of the generic "No games added." (which reads as if the user declined).
func TestDoGameDetect_AllExcludedPrintsFriendlyMessage(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Skyrim Special Edition"}))

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
	}

	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader("all\n"))

	err := doGameDetect(cmd, reader, configDir, games)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "already configured")
}

func TestDoGameDetect_NoGamesFound(t *testing.T) {
	configDir = t.TempDir()

	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader(""))

	err := doGameDetect(cmd, reader, configDir, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No moddable Steam games found.")
}

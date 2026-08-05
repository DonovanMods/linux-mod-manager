package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGameListCmd_Structure(t *testing.T) {
	assert.Equal(t, "list", gameListCmd.Use)
	assert.NotEmpty(t, gameListCmd.Short)
	assert.NotEmpty(t, gameListCmd.Long)
}

// TestDoGameList_EmptyState pins the friendly empty-state message (#205):
// no games configured should point the user at 'game add'/'game detect',
// not just print nothing or a bare "no games" line.
func TestDoGameList_EmptyState(t *testing.T) {
	svc := setupGameAddTest(t)

	out := captureStdout(t, func() error {
		return doGameList(&cobra.Command{}, svc)
	})

	assert.Contains(t, out, "game add")
	assert.Contains(t, out, "game detect")
}

// TestDoGameList_ShowsConfiguredGames pins the table's column set (#205):
// ID, name, install path, mod path, deploy mode, sources.
func TestDoGameList_ShowsConfiguredGames(t *testing.T) {
	svc := setupGameAddTest(t)
	require.NoError(t, svc.AddGame(&domain.Game{
		ID:          "skyrim-se",
		Name:        "Skyrim Special Edition",
		InstallPath: "/games/skyrim",
		ModPath:     "/games/skyrim/Data",
		SourceIDs:   map[string]string{"nexusmods": "skyrimspecialedition"},
		DeployMode:  domain.DeployExtract,
	}))

	out := captureStdout(t, func() error {
		return doGameList(&cobra.Command{}, svc)
	})

	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "INSTALL PATH")
	assert.Contains(t, out, "MOD PATH")
	assert.Contains(t, out, "DEPLOY MODE")
	assert.Contains(t, out, "SOURCES")
	assert.Contains(t, out, "skyrim-se")
	assert.Contains(t, out, "Skyrim Special Edition")
	assert.Contains(t, out, "/games/skyrim")
	assert.Contains(t, out, "/games/skyrim/Data")
	assert.Contains(t, out, "extract")
	assert.Contains(t, out, "nexusmods:skyrimspecialedition")
}

// TestDoGameList_MarksDefaultGame pins the "(default)" marker (#205 item 1).
func TestDoGameList_MarksDefaultGame(t *testing.T) {
	svc := setupGameAddTest(t)
	require.NoError(t, svc.AddGame(&domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}))
	require.NoError(t, svc.AddGame(&domain.Game{ID: "starrupture", Name: "Star Rupture"}))

	cfg := &config.Config{DefaultGame: "starrupture"}
	require.NoError(t, cfg.Save(svc.ConfigDir()))

	out := captureStdout(t, func() error {
		return doGameList(&cobra.Command{}, svc)
	})

	starLine := lineContaining(out, "starrupture")
	skyrimLine := lineContaining(out, "skyrim-se")
	assert.Contains(t, starLine, "(default)")
	assert.NotContains(t, skyrimLine, "(default)")
}

// TestDoGameList_MultipleSourcesCompactKV pins the "k:v,k:v" compact
// rendering, sorted by key for determinism (#205 item 1).
func TestDoGameList_MultipleSourcesCompactKV(t *testing.T) {
	svc := setupGameAddTest(t)
	require.NoError(t, svc.AddGame(&domain.Game{
		ID:   "icarus",
		Name: "Icarus",
		SourceIDs: map[string]string{
			"icarus":     "icarus",
			"curseforge": "12345",
		},
	}))

	out := captureStdout(t, func() error {
		return doGameList(&cobra.Command{}, svc)
	})

	line := lineContaining(out, "icarus")
	assert.Contains(t, line, "curseforge:12345,icarus:icarus")
}

// TestDoGameList_NoSourcesShowsPlaceholder guards against an empty
// SourceIDs map rendering as a blank cell (ambiguous with a parse error);
// a "-" placeholder makes "no sources configured" explicit.
func TestDoGameList_NoSourcesShowsPlaceholder(t *testing.T) {
	svc := setupGameAddTest(t)
	require.NoError(t, svc.AddGame(&domain.Game{ID: "bare-game", Name: "Bare Game"}))

	out := captureStdout(t, func() error {
		return doGameList(&cobra.Command{}, svc)
	})

	line := lineContaining(out, "bare-game")
	assert.Contains(t, line, "-")
}

// TestDoGameList_JSONOutput pins the --json contract: an array (never
// null, even when empty) with an explicit "default" boolean per entry.
func TestDoGameList_JSONOutput(t *testing.T) {
	svc := setupGameAddTest(t)
	require.NoError(t, svc.AddGame(&domain.Game{
		ID:          "skyrim-se",
		Name:        "Skyrim SE",
		InstallPath: "/games/skyrim",
		ModPath:     "/games/skyrim/Data",
		SourceIDs:   map[string]string{"nexusmods": "skyrimspecialedition"},
	}))
	cfg := &config.Config{DefaultGame: "skyrim-se"}
	require.NoError(t, cfg.Save(svc.ConfigDir()))

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doGameList(&cobra.Command{}, svc)
	})

	var rows []gameListJSON
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "skyrim-se", rows[0].ID)
	assert.True(t, rows[0].Default)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, rows[0].Sources)
}

// TestDoGameList_JSONOutput_EmptyIsArrayNotNull mirrors search/list's
// contract: --json with nothing configured still emits "[]", not "null".
func TestDoGameList_JSONOutput_EmptyIsArrayNotNull(t *testing.T) {
	svc := setupGameAddTest(t)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doGameList(&cobra.Command{}, svc)
	})

	assert.Equal(t, "[]", strings.TrimSpace(out))
}

// TestDoGameList_JSONOutput_NoSourcesEmitsEmptyObject tests that a game
// with no sources configured emits "sources": {} in JSON, not null.
func TestDoGameList_JSONOutput_NoSourcesEmitsEmptyObject(t *testing.T) {
	svc := setupGameAddTest(t)
	require.NoError(t, svc.AddGame(&domain.Game{
		ID:          "bare-game",
		Name:        "Bare Game",
		InstallPath: "/games/bare",
		ModPath:     "/games/bare/Mods",
		// SourceIDs is nil, intentionally
	}))

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doGameList(&cobra.Command{}, svc)
	})

	var rows []gameListJSON
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "bare-game", rows[0].ID)
	assert.Equal(t, map[string]string{}, rows[0].Sources)
}

func lineContaining(out, needle string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

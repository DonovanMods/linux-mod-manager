package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGameEditTest is setupGameAddTest plus the two registered sources
// `game edit` needs to be able to map (core refuses an unregistered one),
// and a game already mapping one of them.
func setupGameEditTest(t *testing.T) *core.Service {
	t.Helper()
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockAuthSource{id: "nexusmods", name: "NexusMods"})
	svc.RegisterSource(&mockAuthSource{id: "local-mods", name: "Local Mods"})
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:          "skyrim-se",
		Name:        "Skyrim Special Edition",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		SourceIDs:   map[string]string{"nexusmods": "skyrimspecialedition"},
	}))
	resetGameEditFlags(t)
	return svc
}

// resetGameEditFlags zeroes this command's cobra flag variables for the
// duration of one test - they are package globals bound by init(), so a
// test that sets one would otherwise leak it into every test after it.
func resetGameEditFlags(t *testing.T) {
	t.Helper()
	sources, remove := gameEditSources, gameEditRemove
	t.Cleanup(func() { gameEditSources, gameEditRemove = sources, remove })
	gameEditSources, gameEditRemove = nil, nil
}

func TestGameEditCmd_Structure(t *testing.T) {
	assert.Equal(t, "edit <game-id>", gameEditCmd.Use)
	assert.NotEmpty(t, gameEditCmd.Short)
	assert.NotEmpty(t, gameEditCmd.Long)
}

// TestDoGameEdit_AddsASourceKeepingTheRest is the flow the epic review's
// C-4 exists for: a custom source created in the sources editor becomes
// one this game maps, without disturbing the mapping it already had.
func TestDoGameEdit_AddsASourceKeepingTheRest(t *testing.T) {
	svc := setupGameEditTest(t)
	gameEditSources = []string{"local-mods=skyrim"}

	out := captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "skyrim-se")
	})
	assert.Contains(t, out, "local-mods")

	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition", "local-mods": "skyrim"}, game.SourceIDs)
}

// TestDoGameEdit_OverwritesAnExistingMapping: --source on an id the game
// already maps replaces that identifier rather than being refused.
func TestDoGameEdit_OverwritesAnExistingMapping(t *testing.T) {
	svc := setupGameEditTest(t)
	gameEditSources = []string{"nexusmods=skyrim"}

	captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "skyrim-se")
	})

	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "skyrim"}, game.SourceIDs)
}

// TestDoGameEdit_AcceptsAnEmptyIdentifier: `--source local-mods=` maps a
// source that keys the game by nothing else, which is how a directory
// source is normally configured.
func TestDoGameEdit_AcceptsAnEmptyIdentifier(t *testing.T) {
	svc := setupGameEditTest(t)
	gameEditSources = []string{"local-mods="}

	captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "skyrim-se")
	})

	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, "", game.SourceIDs["local-mods"])
}

// TestDoGameEdit_RemovesASource pins --remove-source, and that a remove
// and an add in the same run compose (the remove is applied first).
func TestDoGameEdit_RemovesASource(t *testing.T) {
	svc := setupGameEditTest(t)
	gameEditSources = []string{"local-mods=skyrim"}
	gameEditRemove = []string{"nexusmods"}

	captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "skyrim-se")
	})

	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"local-mods": "skyrim"}, game.SourceIDs)
}

// TestDoGameEdit_RefusesRemovingASourceTheGameDoesNotMap: a typo'd id must
// say so rather than silently doing nothing.
func TestDoGameEdit_RefusesRemovingASourceTheGameDoesNotMap(t *testing.T) {
	svc := setupGameEditTest(t)
	gameEditRemove = []string{"curseforge"}

	err := doGameEdit(context.Background(), svc, "skyrim-se")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "curseforge")
}

// TestDoGameEdit_RefusesAMalformedSourceFlag: the flag's whole contract is
// the "=" - without it there is no identifier to map.
func TestDoGameEdit_RefusesAMalformedSourceFlag(t *testing.T) {
	svc := setupGameEditTest(t)
	gameEditSources = []string{"local-mods"}

	err := doGameEdit(context.Background(), svc, "skyrim-se")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "=")
}

// TestDoGameEdit_RefusesWithNoFlags: an edit that edits nothing is a
// mistake, not a no-op.
func TestDoGameEdit_RefusesWithNoFlags(t *testing.T) {
	svc := setupGameEditTest(t)

	err := doGameEdit(context.Background(), svc, "skyrim-se")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--source")
}

// TestDoGameEdit_UnknownGame refuses before any write, with the same
// wording every other game subcommand uses.
func TestDoGameEdit_UnknownGame(t *testing.T) {
	svc := setupGameEditTest(t)
	gameEditSources = []string{"local-mods=x"}

	err := doGameEdit(context.Background(), svc, "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "game not found")
}

// TestDoGameEdit_UnregisteredSourceIsRefused pins that core's registry
// check reaches the CLI rather than being duplicated here.
func TestDoGameEdit_UnregisteredSourceIsRefused(t *testing.T) {
	svc := setupGameEditTest(t)
	gameEditSources = []string{"nope=x"}

	err := doGameEdit(context.Background(), svc, "skyrim-se")
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "sources", specErr.Field)
}

// TestDoGameEdit_JSONEmitsTheGameListRow is Ruling 15: exactly one
// document on stdout, and it is the same `lmm game list --json` row.
func TestDoGameEdit_JSONEmitsTheGameListRow(t *testing.T) {
	svc := setupGameEditTest(t)
	gameEditSources = []string{"local-mods=skyrim"}
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	out := captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "skyrim-se")
	})

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal([]byte(out), &entry))
	assert.Equal(t, "skyrim-se", entry.ID)
	assert.Equal(t, "skyrim", entry.SourceIDs["local-mods"])
}

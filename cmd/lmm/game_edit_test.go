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
	// local-mods declares that its per-game mapped value addresses nothing
	// - what a directory source does - which is what makes the empty
	// mapping below a configuration rather than a value the user left out
	// (#387 / T1 review #3).
	svc.RegisterSource(&mockIdentifierIgnoringSource{mockGameAddSource{id: "local-mods", name: "Local Mods"}})
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

// TestDoGameEdit_RefusesRemovingASourceStillReferenced is M5's CLI half
// (epic review M-4): `--remove-source` shows the same GameSourceInUseError
// refusal PUT /api/v1/games/{id} does - core owns the rule, so the CLI
// gets it for free by calling the same seam, but nothing pinned that yet.
func TestDoGameEdit_RefusesRemovingASourceStillReferenced(t *testing.T) {
	svc := setupGameEditTest(t)
	_, err := svc.NewProfileManager().Create(context.Background(), "skyrim-se", "default")
	require.NoError(t, err)
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m1", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim-se"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	gameEditSources = []string{"local-mods=skyrim"}
	gameEditRemove = []string{"nexusmods"}

	err = doGameEdit(context.Background(), svc, "skyrim-se")
	var inUse *core.GameSourceInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, "nexusmods", inUse.SourceID)
	assert.Contains(t, err.Error(), "uninstall them first")

	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, game.SourceIDs)
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

// TestReportError_JSON_GameSourceInUseError pins core.GameSourceInUseError's
// --json error envelope wire shape (M5's Details() any, the
// detailsCoverage AST ratchet's entry for it - details_coverage_test.go).
func TestReportError_JSON_GameSourceInUseError(t *testing.T) {
	withJSONOutput(t)

	err := &core.GameSourceInUseError{SourceID: "nexusmods", GameID: "skyrim-se", Count: 1, Mods: []string{"nexusmods:m1"}}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"1 installed mod(s) still come from \\\"nexusmods\\\"; uninstall them first\",\n"+
		"  \"details\": {\n"+
		"    \"source_id\": \"nexusmods\",\n"+
		"    \"game_id\": \"skyrim-se\",\n"+
		"    \"count\": 1,\n"+
		"    \"mods\": [\n"+
		"      \"nexusmods:m1\"\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
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

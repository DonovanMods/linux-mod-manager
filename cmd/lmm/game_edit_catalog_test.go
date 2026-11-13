package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setCatalogFlag passes --catalog for one test.
func setCatalogFlag(t *testing.T, value string) {
	t.Helper()
	was, wasSet := gameEditCatalog, gameEditCatalogSet
	t.Cleanup(func() { gameEditCatalog, gameEditCatalogSet = was, wasSet })
	gameEditCatalog, gameEditCatalogSet = value, true
}

// #606 owner request: --catalog says which known game an entry is - a GOG
// or Epic copy under an id of its own - by the catalog's slug. An unknown
// slug is refused naming the known ones, and `""` clears the key.
func TestDoGameEdit_Catalog(t *testing.T) {
	svc := setupGameEditTest(t)
	app.RegisterKnownGames(svc, configDir)
	ctx := context.Background()

	setCatalogFlag(t, "skyrim-se")
	out := captureStdout(t, func() error { return doGameEdit(ctx, svc, "skyrim-se", false) })
	assert.Contains(t, out, "catalog entry set to skyrim-se (Skyrim Special Edition)")
	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, "skyrim-se", game.Catalog)

	setCatalogFlag(t, "skyrim-sx")
	err = doGameEdit(ctx, svc, "skyrim-se", false)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "catalog", specErr.Field)
	assert.Contains(t, err.Error(), "skyrim-vr")

	setCatalogFlag(t, "")
	out = captureStdout(t, func() error { return doGameEdit(ctx, svc, "skyrim-se", false) })
	assert.Contains(t, out, "catalog entry cleared")
	game, err = svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Empty(t, game.Catalog)
}

// The --json document carries the new key.
func TestDoGameEdit_CatalogJSON(t *testing.T) {
	svc := setupGameEditTest(t)
	app.RegisterKnownGames(svc, configDir)
	withJSONOutput(t)
	setCatalogFlag(t, "starfield")
	out := captureStdout(t, func() error { return doGameEdit(context.Background(), svc, "skyrim-se", false) })
	assert.Contains(t, out, `"catalog": "starfield"`)
}

// `lmm game show` names the catalog entry when games.yaml sets one, with the
// game's name, and says nothing when it does not.
func TestDoGameShow_PrintsTheCatalogEntry(t *testing.T) {
	svc := setupGameEditTest(t)
	app.RegisterKnownGames(svc, configDir)
	ctx := context.Background()

	out := captureStdout(t, func() error { return doGameShow(ctx, svc, "skyrim-se") })
	assert.NotContains(t, out, "Catalog:")

	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	game.Catalog = "skyrim-se"
	require.NoError(t, svc.SaveGame(ctx, game))
	out = captureStdout(t, func() error { return doGameShow(ctx, svc, "skyrim-se") })
	assert.Contains(t, out, "Catalog:      skyrim-se (Skyrim Special Edition)")
}

// verify's catalog_unknown note prints a line, with the way out.
func TestRenderVerifyFinding_CatalogUnknown(t *testing.T) {
	out := captureStdout(t, func() error {
		renderVerifyFinding(core.VerifyEvent{Finding: core.VerifyFinding{
			Status:        core.VerifyStatusCatalogUnknown,
			Note:          `games.yaml says this game is catalog entry "skyrim-sx"`,
			FixableReason: "run: lmm game edit gog --catalog <entry>",
		}})
		return nil
	})
	assert.Contains(t, out, `~ catalog - games.yaml says this game is catalog entry "skyrim-sx"`)
	assert.Contains(t, out, "  run: lmm game edit gog --catalog <entry>")
}

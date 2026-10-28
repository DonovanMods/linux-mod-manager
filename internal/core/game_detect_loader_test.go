package core_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// detectedBepInExGame is the candidate a curated BepInEx entry produces once
// the known-games catalog can declare a loader (#416): the game-root mod path
// T1 needs, plus the declaration T2's precondition asks for.
func detectedBepInExGame(install string) domain.DetectedGame {
	return domain.DetectedGame{
		SteamAppID: "892970", Slug: "valheim", Name: "Valheim",
		InstallPath: install, ModPath: install,
		NexusID: "valheim", Known: true,
		Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5"},
	}
}

// TestGameFromDetected_CarriesTheLoaderDeclaration is #416's whole point: a
// curated BepInEx game added by detection must arrive with `loader:` set, or
// its FIRST plugin install is refused by #359's precondition and the flow is
// worse than it was before the loader existed.
func TestGameFromDetected_CarriesTheLoaderDeclaration(t *testing.T) {
	install := t.TempDir()
	game, err := core.GameFromDetected(detectedBepInExGame(install))
	require.NoError(t, err)

	require.NotNil(t, game.Loader)
	assert.Equal(t, domain.LoaderKindBepInEx, game.Loader.Kind)
	assert.Equal(t, "5.4.23.5", game.Loader.Version)
	assert.True(t, game.DeclaresBepInEx(), "the rules #358/#359 gate on must fire for it")
	assert.Equal(t, install, game.ModPath, "a BepInEx game's mod path IS its install path")
}

// A candidate with no declaration writes no `loader:` block at all - which is
// every game in the shipped catalog today.
func TestGameFromDetected_NoLoaderWithoutADeclaration(t *testing.T) {
	game, err := core.GameFromDetected(detectedSkyrim(t.TempDir()))
	require.NoError(t, err)
	assert.Nil(t, game.Loader)
}

// TestGameSpecFromDetected_CarriesTheLoaderDeclaration is the other door into
// the same games.yaml block: `lmm game add --from-detected` and POST
// /api/v1/games' from_steam_app_id prefill go through the SPEC, so the
// declaration has to survive that conversion too or the two ways of adding
// the same curated game disagree.
func TestGameSpecFromDetected_CarriesTheLoaderDeclaration(t *testing.T) {
	install := t.TempDir()
	spec := core.GameSpecFromDetected(detectedBepInExGame(install), core.GameSpec{})

	require.NotNil(t, spec.Loader)
	assert.Equal(t, domain.LoaderKindBepInEx, spec.Loader.Kind)
	assert.Equal(t, "5.4.23.5", spec.Loader.Version)
}

// An explicit loader on the overrides wins, exactly as every other prefilled
// field does: a user who typed `--loader ""`-worth of intent is not overruled
// by the catalog.
func TestGameSpecFromDetected_ExplicitLoaderWins(t *testing.T) {
	spec := core.GameSpecFromDetected(detectedBepInExGame(t.TempDir()), core.GameSpec{
		Loader: &core.LoaderSpec{Kind: "melonloader"},
	})
	require.NotNil(t, spec.Loader)
	assert.Equal(t, "melonloader", spec.Loader.Kind)
}

// TestApplyGameDetect_WritesTheLoaderBlock drives the declaration all the way
// to games.yaml and back: detect -> save -> reload, which is the path a user
// takes before their first `lmm install`.
func TestApplyGameDetect_WritesTheLoaderBlock(t *testing.T) {
	svc := newFlowsTestService(t)
	install := t.TempDir()

	result, err := svc.ApplyGameDetect(context.Background(), []domain.DetectedGame{detectedBepInExGame(install)})
	require.NoError(t, err)
	assert.Equal(t, []string{"valheim"}, result.Saved)

	var found *domain.Game
	for _, g := range svc.ListGames() {
		if g.ID == "valheim" {
			found = g
		}
	}
	require.NotNil(t, found)
	require.NotNil(t, found.Loader, "the declaration must reach games.yaml")
	assert.Equal(t, domain.LoaderKindBepInEx, found.Loader.Kind)
	assert.Equal(t, "5.4.23.5", found.Loader.Version)
	assert.Equal(t, filepath.Clean(install), filepath.Clean(found.ModPath))
}

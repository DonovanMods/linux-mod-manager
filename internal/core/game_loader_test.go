package core_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAddGame_AcceptsALoaderDeclaration: `lmm game add --loader bepinex ...`
// and POST /api/v1/games both reach AddGame, so the loader block is part of
// the spec rather than a follow-up edit - a BepInEx game is configured in
// one step.
func TestAddGame_AcceptsALoaderDeclaration(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()

	entry, err := svc.AddGame(context.Background(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "lethalcompany", Name: "Lethal Company",
		InstallPath: install, ModPath: install,
		Loader: &core.LoaderSpec{Kind: "BepInEx", Version: "5.4.23.5", Runtime: "mono", Bootstrap: "proton"},
	})
	require.NoError(t, err)
	require.NotNil(t, entry.Loader)
	assert.Equal(t, domain.LoaderKindBepInEx, entry.Loader.Kind, "the kind is normalised to lower case")
	assert.Equal(t, domain.LoaderRuntimeMono, entry.Loader.Runtime)
	assert.Equal(t, domain.LoaderBootstrapProton, entry.Loader.Bootstrap)

	games, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	require.NotNil(t, games[entry.ID].Loader)
	assert.True(t, games[entry.ID].DeclaresBepInEx())
}

// A rejected loader value names the WIRE field, so an SPA form marks the
// offending select rather than substring-matching an English sentence.
func TestAddGame_RejectsAnUnknownLoaderValue(t *testing.T) {
	install := t.TempDir()
	for _, tc := range []struct {
		spec  core.LoaderSpec
		field string
	}{
		{core.LoaderSpec{Kind: "bepinex", Runtime: "coreclr"}, "loader.runtime"},
		{core.LoaderSpec{Kind: "bepinex", Bootstrap: "wine"}, "loader.bootstrap"},
		{core.LoaderSpec{Kind: ""}, "loader.kind"},
	} {
		svc := newGameAddService(t)
		spec := tc.spec
		_, err := svc.AddGame(context.Background(), core.GameSpec{
			SourceID: "nexusmods", Identifier: "g", Name: "G",
			InstallPath: install, Loader: &spec,
		})
		require.Error(t, err)
		var specErr *core.GameSpecError
		require.ErrorAs(t, err, &specErr)
		assert.Equal(t, tc.field, specErr.Field)
	}
}

// TestUpdateGameLoader is the EDIT seam `lmm game edit --loader` and
// PUT /api/v1/games/{id} share: a settings-class single gated write, like
// UpdateGameSources, returning the game's own `lmm game list --json` row
// re-read after it.
func TestUpdateGameLoader(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()
	_, err := svc.AddGame(context.Background(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "valheim", Name: "Valheim", InstallPath: install,
	})
	require.NoError(t, err)

	entry, err := svc.UpdateGameLoader(context.Background(), "valheim",
		&core.LoaderSpec{Kind: "bepinex", Version: "5.4.23.5", Runtime: "mono", Bootstrap: "proton"})
	require.NoError(t, err)
	require.NotNil(t, entry.Loader)
	assert.Equal(t, "5.4.23.5", entry.Loader.Version)

	// Persisted, and the in-memory game the rest of core reads agrees.
	games, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.True(t, games["valheim"].DeclaresBepInEx())
	live, err := svc.GetGame("valheim")
	require.NoError(t, err)
	assert.True(t, live.DeclaresBepInEx())

	// nil CLEARS it - the only way to say "this game has no loader after
	// all", which `lmm game edit --loader ""` sends.
	entry, err = svc.UpdateGameLoader(context.Background(), "valheim", nil)
	require.NoError(t, err)
	assert.Nil(t, entry.Loader)
	games, err = config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Nil(t, games["valheim"].Loader)
}

// An unknown game is domain.ErrGameNotFound, the 404 every other
// game-scoped route already answers with.
func TestUpdateGameLoader_UnknownGame(t *testing.T) {
	svc := newGameAddService(t)
	_, err := svc.UpdateGameLoader(context.Background(), "nope", nil)
	assert.ErrorIs(t, err, domain.ErrGameNotFound)
}

package core_test

// #527: the web UI's game editor is ONE panel with one Save, so EditGame
// takes every field a configured game's row edits - name, install path, mod
// path, sources, adapter and loader - checks all of them against the game
// they leave, and writes games.yaml once, or not at all.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEditGame_AppliesEveryFieldInOneEdit(t *testing.T) {
	svc := newGameAddService(t)
	_, root := seedInstallPathGame(t, svc, "mods")
	fixed := filepath.Join(root, "B")
	require.NoError(t, os.Mkdir(fixed, 0o755))
	name, modPath, adapter := "  Renamed Game ", "BepInEx/plugins", ""

	entry, err := svc.EditGame(t.Context(), "g1", core.GameEdit{
		Name:        &name,
		InstallPath: &fixed,
		ModPath:     &modPath,
		Sources:     map[string]string{"curseforge": "432"},
		Adapter:     &adapter,
		Loader:      &core.LoaderSpec{Kind: "bepinex", Version: "5.4.23.5", Bootstrap: "proton"},
		LoaderSet:   true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Renamed Game", entry.Name, "the name is trimmed")

	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	got := onDisk["g1"]
	assert.Equal(t, "Renamed Game", got.Name)
	assert.Equal(t, fixed, got.InstallPath)
	assert.Equal(t, filepath.Join(fixed, "BepInEx", "plugins"), got.ModPath)
	assert.Equal(t, map[string]string{"curseforge": "432"}, got.SourceIDs)
	require.NotNil(t, got.Loader)
	assert.Equal(t, domain.GameLoader{Kind: "bepinex", Version: "5.4.23.5", Bootstrap: domain.LoaderBootstrapProton}, *got.Loader)
}

// TestEditGame_ARefusedFieldWritesNothing: one bad field anywhere in the
// edit, and none of the good ones are written either.
func TestEditGame_ARefusedFieldWritesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		edit  func(root string) core.GameEdit
		field string
	}{
		"a blank name": {func(root string) core.GameEdit {
			blank, fixed := "  ", root
			return core.GameEdit{Name: &blank, InstallPath: &fixed}
		}, "name"},
		"a bad loader runtime": {func(root string) core.GameEdit {
			renamed, fixed := "Renamed", root
			return core.GameEdit{Name: &renamed, InstallPath: &fixed, Loader: &core.LoaderSpec{Kind: "bepinex", Runtime: "dotnet"}, LoaderSet: true}
		}, "loader.runtime"},
		"an unregistered source": {func(root string) core.GameEdit {
			renamed := "Renamed"
			return core.GameEdit{Name: &renamed, Sources: map[string]string{"no-such-source": "x"}}
		}, "sources"},
		"a missing install path": {func(root string) core.GameEdit {
			renamed, missing := "Renamed", filepath.Join(root, "nope")
			return core.GameEdit{Name: &renamed, InstallPath: &missing, Loader: nil, LoaderSet: true}
		}, "install_path"},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newGameAddService(t)
			game, root := seedInstallPathGame(t, svc, "mods")
			game.Loader = &domain.GameLoader{Kind: "bepinex"}
			require.NoError(t, svc.SaveGame(t.Context(), game))
			before, err := os.ReadFile(filepath.Join(svc.ConfigDir(), "games.yaml"))
			require.NoError(t, err)

			_, err = svc.EditGame(t.Context(), "g1", tc.edit(root))
			var spec *core.GameSpecError
			require.ErrorAs(t, err, &spec)
			assert.Equal(t, tc.field, spec.Field)

			after, err := os.ReadFile(filepath.Join(svc.ConfigDir(), "games.yaml"))
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "games.yaml is untouched")
			reloaded, err := svc.GetGame("g1")
			require.NoError(t, err)
			assert.Equal(t, game.Name, reloaded.Name)
			assert.Equal(t, game.InstallPath, reloaded.InstallPath)
			assert.NotNil(t, reloaded.Loader)
		})
	}
}

// TestEditGame_LoaderSetWithNilClearsTheDeclaration: the loader is a
// replacement, and LoaderSet with no spec is "this game has no loader".
func TestEditGame_LoaderSetWithNilClearsTheDeclaration(t *testing.T) {
	svc := newGameAddService(t)
	game, _ := seedInstallPathGame(t, svc, "mods")
	game.Loader = &domain.GameLoader{Kind: "bepinex"}
	require.NoError(t, svc.SaveGame(t.Context(), game))

	entry, err := svc.EditGame(t.Context(), "g1", core.GameEdit{LoaderSet: true})
	require.NoError(t, err)
	assert.Nil(t, entry.Loader)

	// And without LoaderSet a nil spec leaves the declaration alone.
	game.Loader = &domain.GameLoader{Kind: "bepinex"}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	name := "Renamed"
	entry, err = svc.EditGame(t.Context(), "g1", core.GameEdit{Name: &name})
	require.NoError(t, err)
	require.NotNil(t, entry.Loader)
	assert.Equal(t, "bepinex", entry.Loader.Kind)
}

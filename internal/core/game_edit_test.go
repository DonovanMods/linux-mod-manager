package core_test

// UpdateGameSources - the source<->game mapping neither frontend could
// reach before #326 (epic live review, C-4). Every assertion is on the
// END STATE: what games.yaml holds after the write, and what the returned
// `lmm game list` row says.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedSourcesGame saves a game mapping only "nexusmods", the state every
// test below edits from.
func seedSourcesGame(t *testing.T, svc *core.Service) *domain.Game {
	t.Helper()
	game := &domain.Game{
		ID:          "g1",
		Name:        "Fixture Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		SourceIDs:   map[string]string{"nexusmods": "fixturegame"},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	return game
}

// TestUpdateGameSources_ReplacesTheMapOnDisk is the whole point: the map
// handed over is the map games.yaml ends up with - added entries appear,
// omitted ones are gone.
func TestUpdateGameSources_ReplacesTheMapOnDisk(t *testing.T) {
	svc := newGameAddService(t)
	game := seedSourcesGame(t, svc)

	entry, err := svc.UpdateGameSources(t.Context(), game.ID, map[string]string{"curseforge": "432"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"curseforge": "432"}, entry.SourceIDs,
		"the returned row must carry the new map, not the old one")

	// The in-memory set and the file on disk must agree - a write that
	// only updated one of the two would still read back correct in the
	// same process.
	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"curseforge": "432"}, reloaded.SourceIDs)

	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"curseforge": "432"}, onDisk[game.ID].SourceIDs)
}

// TestUpdateGameSources_KeepsTheRestOfTheGame pins that this edits ONE
// field: the paths, name and link method a game was configured with
// survive it.
func TestUpdateGameSources_KeepsTheRestOfTheGame(t *testing.T) {
	svc := newGameAddService(t)
	game := seedSourcesGame(t, svc)

	entry, err := svc.UpdateGameSources(t.Context(), game.ID,
		map[string]string{"nexusmods": "fixturegame", "curseforge": "432"})
	require.NoError(t, err)

	assert.Equal(t, game.Name, entry.Name)
	assert.Equal(t, game.InstallPath, entry.InstallPath)
	assert.Equal(t, game.ModPath, entry.ModPath)
	assert.Len(t, entry.SourceIDs, 2)
}

// TestUpdateGameSources_TrimsAndAcceptsAnEmptyIdentifier: a directory
// source is routinely mapped with no identifier at all, so an empty VALUE
// is legal - for a source that says so - even though an empty KEY is not.
func TestUpdateGameSources_TrimsAndAcceptsAnEmptyIdentifier(t *testing.T) {
	svc := newGameAddService(t)
	svc.RegisterSource(&identifierIgnoringSource{catalogLessSource{id: "localmods", name: "Local Mods"}})
	game := seedSourcesGame(t, svc)

	entry, err := svc.UpdateGameSources(t.Context(), game.ID, map[string]string{"  curseforge  ": "  432  "})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"curseforge": "432"}, entry.SourceIDs)

	entry, err = svc.UpdateGameSources(t.Context(), game.ID, map[string]string{"  localmods  ": "   "})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"localmods": ""}, entry.SourceIDs)
}

// TestUpdateGameSources_RefusesAnEmptyIdentifierForASourceThatNeedsOne is
// T1 review #3's write half. `lmm game edit <id> --source nexusmods=` and
// PUT /api/v1/games/{id} both wrote a mapping that fails at first use -
// and, for Thunderstore, one that used to silently index whichever real
// community shared the lmm game's name. `game add` has refused it since
// #387; this is the same rule on the only other way in.
func TestUpdateGameSources_RefusesAnEmptyIdentifierForASourceThatNeedsOne(t *testing.T) {
	svc := newGameAddService(t)
	game := seedSourcesGame(t, svc)

	_, err := svc.UpdateGameSources(t.Context(), game.ID, map[string]string{"nexusmods": "  "})
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "sources", specErr.Field)
	assert.Equal(t, "nexusmods", specErr.Value)

	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "fixturegame"}, reloaded.SourceIDs,
		"a refused edit must leave the game exactly as it was")
}

// TestUpdateGameSources_RefusesAnUnregisteredSource is the typed rejection
// a form marks its own field from: Field is the WIRE key, Value the id at
// fault, and nothing is written.
func TestUpdateGameSources_RefusesAnUnregisteredSource(t *testing.T) {
	svc := newGameAddService(t)
	game := seedSourcesGame(t, svc)

	_, err := svc.UpdateGameSources(t.Context(), game.ID, map[string]string{"local-mods": "x"})
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "sources", specErr.Field)
	assert.Equal(t, "local-mods", specErr.Value)

	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "fixturegame"}, reloaded.SourceIDs,
		"a refused edit must leave the game exactly as it was")
}

// TestUpdateGameSources_RefusesAnEmptyMap: removing the last source leaves
// a game that can neither search nor install, so it is refused rather than
// silently written.
func TestUpdateGameSources_RefusesAnEmptyMap(t *testing.T) {
	svc := newGameAddService(t)
	game := seedSourcesGame(t, svc)

	_, err := svc.UpdateGameSources(t.Context(), game.ID, nil)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "sources", specErr.Field)
}

// TestUpdateGameSources_RefusesAnEmptySourceID guards the other empty: a
// key that is blank (or only whitespace) names no source at all.
func TestUpdateGameSources_RefusesAnEmptySourceID(t *testing.T) {
	svc := newGameAddService(t)
	game := seedSourcesGame(t, svc)

	_, err := svc.UpdateGameSources(t.Context(), game.ID, map[string]string{"   ": "x"})
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "sources", specErr.Field)
}

// TestUpdateGameSources_RefusesADuplicateAfterTrimming is M6 (epic review
// M-6): {" nexusmods":"a","nexusmods":"b"} is legal JSON with two distinct
// keys that trim to the same source id - before the fix this silently
// collapsed into one entry (the sorted-last value winning, no error) with
// nothing to say why the map does not have the shape the caller sent.
func TestUpdateGameSources_RefusesADuplicateAfterTrimming(t *testing.T) {
	svc := newGameAddService(t)
	game := seedSourcesGame(t, svc)

	_, err := svc.UpdateGameSources(t.Context(), game.ID, map[string]string{" nexusmods": "a", "nexusmods": "b"})
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "sources", specErr.Field)
	assert.Equal(t, "nexusmods", specErr.Value)

	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "fixturegame"}, reloaded.SourceIDs,
		"a refused edit must leave the game exactly as it was")
}

// TestUpdateGameSources_UnknownGameIsNotFound: the sentinel every
// game-scoped 404 is built on.
func TestUpdateGameSources_UnknownGameIsNotFound(t *testing.T) {
	svc := newGameAddService(t)
	seedSourcesGame(t, svc)

	_, err := svc.UpdateGameSources(t.Context(), "nope", map[string]string{"nexusmods": "x"})
	require.ErrorIs(t, err, domain.ErrGameNotFound)
}

// TestUpdateGameSources_RefusesRemovingASourceWithInstalledMods is M5
// (epic review M-4): dropping a source mapping while a profile still has
// an installed mod that came from it used to write silently, leaving that
// mod degrading quietly (update checks skip it, a re-link to the source is
// refused, archive imports warn it isn't configured) - the mirror of `lmm
// source remove`'s own SourceInUseError, one level down.
func TestUpdateGameSources_RefusesRemovingASourceWithInstalledMods(t *testing.T) {
	svc := newGameAddService(t)
	game := seedSourcesGame(t, svc)
	_, err := svc.NewProfileManager().Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m1", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	_, err = svc.UpdateGameSources(t.Context(), game.ID, map[string]string{"curseforge": "432"})
	var inUse *core.GameSourceInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, "nexusmods", inUse.SourceID)
	assert.Equal(t, game.ID, inUse.GameID)
	assert.Equal(t, 1, inUse.Count)
	assert.Equal(t, []string{"nexusmods:m1"}, inUse.Mods)
	assert.Contains(t, inUse.Error(), "1 installed mod")
	assert.Contains(t, inUse.Error(), "nexusmods")
	assert.Contains(t, inUse.Error(), "uninstall them first")

	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "fixturegame"}, reloaded.SourceIDs,
		"a refused edit must leave the game exactly as it was")
}

// TestUpdateGameSources_AllowsRemovingAnUnreferencedSource: the refusal is
// scoped to sources an installed mod actually references, not every
// removal - a source with nothing installed from it may still be dropped.
func TestUpdateGameSources_AllowsRemovingAnUnreferencedSource(t *testing.T) {
	svc := newGameAddService(t)
	game := seedSourcesGame(t, svc)
	entry, err := svc.UpdateGameSources(t.Context(), game.ID, map[string]string{"curseforge": "432"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"curseforge": "432"}, entry.SourceIDs)
}

// TestUpdateGameSources_MarksTheDefaultGame pins that the returned row is
// the same GameListEntry `lmm game list --json` emits, default flag and
// all - not a bare domain.Game.
func TestUpdateGameSources_MarksTheDefaultGame(t *testing.T) {
	svc := newGameAddService(t)
	game := seedSourcesGame(t, svc)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))

	entry, err := svc.UpdateGameSources(t.Context(), game.ID, map[string]string{"curseforge": "432"})
	require.NoError(t, err)
	assert.True(t, entry.Default)
}

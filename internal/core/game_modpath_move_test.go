package core_test

// #427 review F1: `lmm game edit --mod-path` refused to move a mod_path
// with files deployed under it, but `lmm game detect`'s repair of an
// already-configured game rewrote it anyway - the #451 orphaning the
// refusal exists to prevent, one command over. Every path that can change a
// configured game's mod_path applies the same check.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedDeployedCuratedGame configures the game a detected row repairs, with
// mod_path <install>/mods and one file deployed there, and returns it with
// the curated row that would move its mod_path to the install root.
func seedDeployedCuratedGame(t *testing.T, svc *core.Service) (*domain.Game, domain.DetectedGame) {
	t.Helper()
	install := t.TempDir()
	game := &domain.Game{
		ID: "human-host", Name: "Human Host",
		InstallPath: install, ModPath: filepath.Join(install, "mods"),
		SourceIDs: map[string]string{"nexusmods": "humanhost"},
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(t.Context(), game.ID)
	require.NoError(t, err)
	deployOneFile(t, svc, game, "default", "m1")
	require.NoError(t, svc.NewProfileManager().UpsertMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: "src", ModID: "m1", Version: "1.0"}))

	row := domain.DetectedGame{
		Slug: game.ID, Name: "Human Host", InstallPath: install, ModPath: install,
		NexusID: "humanhost", Known: true,
	}
	return game, row
}

// assertGameUntouched checks that the refused repair wrote nothing: the
// games.yaml entry, the in-memory game and the default profile's mod list.
func assertGameUntouched(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, game.ModPath, reloaded.ModPath)
	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Equal(t, game.ModPath, onDisk[game.ID].ModPath)
	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Len(t, profile.Mods, 1, "the refused repair did not reset the default profile either")
}

func TestApplyDetectSelection_RefusesToMoveAModPathWithFilesDeployed(t *testing.T) {
	svc := newGameAddService(t)
	game, row := seedDeployedCuratedGame(t, svc)

	_, result, err := svc.ApplyDetectSelection(context.Background(), []domain.DetectedGame{row})
	var inUse *core.GameModPathInUseError
	require.ErrorAs(t, err, &inUse, "the web detect apply and `lmm game detect --select` both run this")
	assert.Equal(t, row.ModPath, inUse.NewModPath)
	assert.Contains(t, err.Error(), "human-host")
	assert.Empty(t, result.Saved)
	assertGameUntouched(t, svc, game)
}

// TestApplyGameDetect_RefusesToMoveAModPathWithFilesDeployed: `lmm init`'s
// path into the same loop.
func TestApplyGameDetect_RefusesToMoveAModPathWithFilesDeployed(t *testing.T) {
	svc := newGameAddService(t)
	game, row := seedDeployedCuratedGame(t, svc)

	_, err := svc.ApplyGameDetect(context.Background(), []domain.DetectedGame{row})
	var inUse *core.GameModPathInUseError
	require.ErrorAs(t, err, &inUse)
	assertGameUntouched(t, svc, game)
}

// TestApplyDetectSelection_RepairsWhenTheModPathDoesNotMove: the check is
// about the MOVE, not about deployments - a repair that leaves the mod_path
// where it is goes ahead with files deployed.
func TestApplyDetectSelection_RepairsWhenTheModPathDoesNotMove(t *testing.T) {
	svc := newGameAddService(t)
	game, row := seedDeployedCuratedGame(t, svc)
	row.ModPath = game.ModPath
	row.Name = "Human Host (renamed)"

	_, result, err := svc.ApplyDetectSelection(context.Background(), []domain.DetectedGame{row})
	require.NoError(t, err)
	assert.Equal(t, []string{game.ID}, result.Saved)
	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "Human Host (renamed)", reloaded.Name)
}

// TestApplyDetectSelection_RepairsAModPathWithNothingDeployed: with nothing
// deployed there is nothing to strand, and the repair moves the mod_path -
// the fix #427 asked detection to be.
func TestApplyDetectSelection_RepairsAModPathWithNothingDeployed(t *testing.T) {
	svc := newGameAddService(t)
	game, row := seedDeployedCuratedGame(t, svc)
	require.NoError(t, svc.ExecForTest(t.Context(), `DELETE FROM deployed_files WHERE game_id = ?`, game.ID))

	_, _, err := svc.ApplyDetectSelection(context.Background(), []domain.DetectedGame{row})
	require.NoError(t, err)
	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, row.ModPath, reloaded.ModPath)
}

// TestAddGame_AnExistingIDKeepsItsModPath: `lmm game add` and POST
// /api/v1/games refuse an id that is already configured, so neither can
// move a mod_path either.
func TestAddGame_AnExistingIDKeepsItsModPath(t *testing.T) {
	svc := newGameAddService(t)
	game, _ := seedDeployedCuratedGame(t, svc)

	_, err := svc.AddGame(context.Background(), core.GameSpec{
		ID: game.ID, Name: game.Name, InstallPath: game.InstallPath, ModPath: game.InstallPath,
		SourceID: "nexusmods", Identifier: "humanhost",
	})
	require.ErrorIs(t, err, core.ErrGameExists)
	assertGameUntouched(t, svc, game)
}

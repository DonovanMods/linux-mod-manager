package core_test

// #528 follow-up: `lmm game detect`'s repair of an already-configured game
// rewrites its install_path (repairedGame), and the Service's own SaveGame
// can too - so both go through the install-path policy `lmm game edit
// --install-path` applies. A repair that is not a valid move is a per-game
// finding (GameDetectResult.Refused, with the same typed error): the other
// games in the selection still apply, and the refused one is left exactly
// as it was.

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

// seedDeployedGameDetectedElsewhere configures human-host at <root>/A with
// mod_path <A>/mods and one file deployed there, and returns it with root
// and the curated row detection now reports for it at <root>/B (its
// mod_path following, as the catalog lays it out).
func seedDeployedGameDetectedElsewhere(t *testing.T, svc *core.Service) (*domain.Game, string, domain.DetectedGame) {
	t.Helper()
	root := t.TempDir()
	install := filepath.Join(root, "A")
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

	moved := filepath.Join(root, "B")
	row := domain.DetectedGame{
		Slug: game.ID, Name: "Human Host", InstallPath: moved, ModPath: filepath.Join(moved, "mods"),
		NexusID: "humanhost", Known: true,
	}
	return game, root, row
}

// anotherDetectedGame is a curated row for a game nobody configured yet.
func anotherDetectedGame(t *testing.T) domain.DetectedGame {
	t.Helper()
	install := t.TempDir()
	return domain.DetectedGame{
		Slug: "other-game", Name: "Other Game", InstallPath: install, ModPath: filepath.Join(install, "mods"),
		NexusID: "othergame", Known: true,
	}
}

func deployedRoots(t *testing.T, svc *core.Service, gameID string) []string {
	t.Helper()
	rows, err := svc.QueryForTest(t.Context(), `SELECT DISTINCT COALESCE(mod_path, '') FROM deployed_files WHERE game_id = ?`, gameID)
	require.NoError(t, err)
	var roots []string
	for _, r := range rows {
		roots = append(roots, r[0])
	}
	return roots
}

// TestApplyDetectSelection_RefusesAnInstallPathRepairWhileTheOldFolderExists:
// the game was found somewhere new, but its old folder - with lmm's files
// deployed in it - is still there, so the repair is not a move. It is
// refused with GameInstallPathInUseError as a finding for THAT game; the
// other selected game is still added; games.yaml and the ledger are
// untouched for the refused one.
func TestApplyDetectSelection_RefusesAnInstallPathRepairWhileTheOldFolderExists(t *testing.T) {
	svc := newGameAddService(t)
	game, _, row := seedDeployedGameDetectedElsewhere(t, svc)
	require.NoError(t, os.MkdirAll(row.ModPath, 0o755))
	other := anotherDetectedGame(t)
	before, err := os.ReadFile(filepath.Join(svc.ConfigDir(), "games.yaml"))
	require.NoError(t, err)

	applied, result, err := svc.ApplyDetectSelection(context.Background(), []domain.DetectedGame{row, other})

	var inUse *core.GameInstallPathInUseError
	require.ErrorAs(t, err, &inUse, "the refusal is the same typed error `lmm game edit --install-path` gives")
	assert.True(t, inUse.OldInstallPathExists)
	assert.Equal(t, row.InstallPath, inUse.NewInstallPath)
	assert.Equal(t, 1, inUse.DeployedFiles)

	require.Len(t, result.Refused, 1, "a per-game finding")
	refused := result.Refused[0]
	assert.Equal(t, game.ID, refused.GameID)
	assert.Equal(t, row.Slug, refused.Slug)
	assert.Contains(t, refused.Error, "lmm purge --game human-host --profile default")
	assert.Same(t, inUse, refused.Details, "the finding carries the typed error's details")

	assert.Equal(t, []string{other.Slug}, result.Saved, "the other game still applies")
	assert.Equal(t, 1, result.Completed())
	require.Len(t, applied, 2)
	assert.Equal(t, other.Slug, applied[0].Slug, "completed rows lead `applied`, so Saved[i] still names applied[i]")
	assert.Equal(t, row.Slug, applied[1].Slug)

	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Equal(t, game.InstallPath, onDisk[game.ID].InstallPath)
	assert.Equal(t, game.ModPath, onDisk[game.ID].ModPath)
	assert.Contains(t, onDisk, other.Slug)
	after, err := os.ReadFile(filepath.Join(svc.ConfigDir(), "games.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(before), "install_path: "+game.InstallPath)
	assert.Contains(t, string(after), "install_path: "+game.InstallPath, "the refused game's entry is unchanged")
	assert.NotContains(t, string(after), row.InstallPath+"\n")
	assert.Equal(t, []string{game.ModPath}, deployedRoots(t, svc, game.ID), "the ledger is unchanged")
}

// TestApplyGameDetect_RefusesAnInstallPathRepairWhileTheOldFolderExists:
// `lmm init`'s path into the same loop gives the same finding.
func TestApplyGameDetect_RefusesAnInstallPathRepairWhileTheOldFolderExists(t *testing.T) {
	svc := newGameAddService(t)
	game, _, row := seedDeployedGameDetectedElsewhere(t, svc)
	require.NoError(t, os.MkdirAll(row.InstallPath, 0o755))

	result, err := svc.ApplyGameDetect(context.Background(), []domain.DetectedGame{row})
	var inUse *core.GameInstallPathInUseError
	require.ErrorAs(t, err, &inUse)
	require.Len(t, result.Refused, 1)
	assert.Empty(t, result.Saved)
	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, game.InstallPath, reloaded.InstallPath)
}

// TestApplyDetectSelection_ReRootsAMovedGame: the Steam library moved -
// the old folder is gone and the deployed files are at the new one - so the
// repair is a move, and records them there exactly as an edit would.
func TestApplyDetectSelection_ReRootsAMovedGame(t *testing.T) {
	svc := newGameAddService(t)
	game, _, row := seedDeployedGameDetectedElsewhere(t, svc)
	require.NoError(t, os.Rename(game.InstallPath, row.InstallPath))

	_, result, err := svc.ApplyDetectSelection(context.Background(), []domain.DetectedGame{row})
	require.NoError(t, err)
	assert.Equal(t, []string{game.ID}, result.Saved)
	assert.Empty(t, result.Refused)

	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, row.InstallPath, reloaded.InstallPath)
	assert.Equal(t, row.ModPath, reloaded.ModPath)
	assert.Equal(t, []string{row.ModPath}, deployedRoots(t, svc, game.ID), "the ledger followed the move")
	problem, err := svc.ModPathProblem(t.Context(), reloaded)
	require.NoError(t, err)
	assert.Nil(t, problem)
}

// TestSaveGame_AnInstallPathChangeFollowsThePolicy: the Service's whole-
// entry SaveGame is an install_path writer too.
func TestSaveGame_AnInstallPathChangeFollowsThePolicy(t *testing.T) {
	svc := newGameAddService(t)
	game, _, row := seedDeployedGameDetectedElsewhere(t, svc)
	require.NoError(t, os.MkdirAll(row.ModPath, 0o755))

	changed := *game
	changed.InstallPath, changed.ModPath = row.InstallPath, row.ModPath
	err := svc.SaveGame(t.Context(), &changed)
	var inUse *core.GameInstallPathInUseError
	require.ErrorAs(t, err, &inUse)
	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, game.InstallPath, reloaded.InstallPath)

	// And a real move through it re-roots the ledger.
	require.NoError(t, os.RemoveAll(row.InstallPath))
	require.NoError(t, os.Rename(game.InstallPath, row.InstallPath))
	require.NoError(t, svc.SaveGame(t.Context(), &changed))
	assert.Equal(t, []string{row.ModPath}, deployedRoots(t, svc, game.ID))
}

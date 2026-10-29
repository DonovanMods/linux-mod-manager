package core_test

// #544: GameEdit.CurseForgeVersionTypes sets, replaces and clears a game's
// curseforge.game_version_types - the edit `lmm game edit
// --curseforge-version-type` and PUT /api/v1/games/{id} share.

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedCurseForgeGame(t *testing.T, svc *core.Service) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, svc.SaveGame(t.Context(), &domain.Game{
		ID: "wow", Name: "WoW", InstallPath: dir, ModPath: dir,
		SourceIDs: map[string]string{"curseforge": "1"}, LinkMethod: domain.LinkSymlink,
	}))
}

func editVersionTypes(svc *core.Service, ids []int) (*core.GameListEntry, error) {
	return svc.EditGame(context.Background(), "wow", core.GameEdit{CurseForgeVersionTypes: &ids})
}

func TestEditGame_CurseForgeVersionTypes_SetsReplacesAndClears(t *testing.T) {
	svc := newFlowsTestService(t)
	seedCurseForgeGame(t, svc)

	entry, err := editVersionTypes(svc, []int{67408, 67408, 77522})
	require.NoError(t, err)
	require.NotNil(t, entry.CurseForge, "the answer is the row re-read after the write")
	assert.Equal(t, []int{67408, 77522}, entry.CurseForge.GameVersionTypes, "duplicates dropped, order kept")

	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	require.NotNil(t, onDisk["wow"].CurseForge)
	assert.Equal(t, []int{67408, 77522}, onDisk["wow"].CurseForge.GameVersionTypes)

	entry, err = editVersionTypes(svc, []int{517})
	require.NoError(t, err)
	assert.Equal(t, []int{517}, entry.CurseForge.GameVersionTypes, "a replacement, not a merge")

	entry, err = editVersionTypes(svc, []int{})
	require.NoError(t, err)
	assert.Nil(t, entry.CurseForge, "an empty list clears the setting")
	onDisk, err = config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Nil(t, onDisk["wow"].CurseForge)
}

// An edit that does not name the setting leaves it alone.
func TestEditGame_CurseForgeVersionTypes_UntouchedByOtherEdits(t *testing.T) {
	svc := newFlowsTestService(t)
	seedCurseForgeGame(t, svc)
	_, err := editVersionTypes(svc, []int{67408})
	require.NoError(t, err)

	name := "World of Warcraft Classic"
	entry, err := svc.EditGame(context.Background(), "wow", core.GameEdit{Name: &name})
	require.NoError(t, err)
	require.NotNil(t, entry.CurseForge)
	assert.Equal(t, []int{67408}, entry.CurseForge.GameVersionTypes)
}

// A non-positive id is a GameSpecError on the wire key, and nothing is
// written.
func TestEditGame_CurseForgeVersionTypes_RefusesANonPositiveID(t *testing.T) {
	svc := newFlowsTestService(t)
	seedCurseForgeGame(t, svc)

	_, err := editVersionTypes(svc, []int{67408, -1})
	var spec *core.GameSpecError
	require.True(t, errors.As(err, &spec), "got %v", err)
	assert.Equal(t, "curseforge.game_version_types", spec.Field)
	assert.Equal(t, "-1", spec.Value)
	assert.ErrorIs(t, err, domain.ErrInvalidCurseForgeVersionType)

	game, err := svc.GetGame("wow")
	require.NoError(t, err)
	assert.Nil(t, game.CurseForge)
}

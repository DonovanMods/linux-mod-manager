package db_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renameDB seeds one game with two profiles, each holding an installed mod
// with file ids (so installed_mod_files' foreign key is live) and a
// deployed-file ownership row.
func renameDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	ctx := context.Background()
	for _, profile := range []string{"default", "other"} {
		require.NoError(t, database.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod: domain.Mod{
				ID: "12345", SourceID: "nexusmods", Name: "Mod",
				Version: "1.0", GameID: "skyrim-se",
			},
			ProfileName:  profile,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			FileIDs:      []string{"f1", "f2"},
		}))
		require.NoError(t, database.SaveDeployedFile(ctx, "skyrim-se", profile, "meshes/test.nif", "nexusmods", "12345"))
	}
	return database
}

// TestRenameProfile_MovesEveryProfileKeyedTable: the install row, its file
// ids (whose FOREIGN KEY would reject the parent update without the
// deferred-constraint transaction) and the deployed-file owner all move
// together.
func TestRenameProfile_MovesEveryProfileKeyedTable(t *testing.T) {
	database := renameDB(t)
	ctx := context.Background()

	require.NoError(t, database.RenameProfile(ctx, "skyrim-se", "default", "survival"))

	mods, err := database.GetInstalledMods(ctx, "skyrim-se", "survival")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, "survival", mods[0].ProfileName)
	assert.Equal(t, []string{"f1", "f2"}, mods[0].FileIDs, "the file-id rows must have moved with their parent")

	old, err := database.GetInstalledMods(ctx, "skyrim-se", "default")
	require.NoError(t, err)
	assert.Empty(t, old)

	owner, err := database.GetFileOwner(ctx, "skyrim-se", "survival", "meshes/test.nif")
	require.NoError(t, err)
	require.NotNil(t, owner)
	assert.Equal(t, "12345", owner.ModID)

	oldOwner, err := database.GetFileOwner(ctx, "skyrim-se", "default", "meshes/test.nif")
	require.NoError(t, err)
	assert.Nil(t, oldOwner, "no ownership row may still name the old profile")
}

// TestRenameProfile_LeavesOtherProfilesAlone: the rename is scoped to one
// (game, profile) pair.
func TestRenameProfile_LeavesOtherProfilesAlone(t *testing.T) {
	database := renameDB(t)
	ctx := context.Background()

	require.NoError(t, database.RenameProfile(ctx, "skyrim-se", "default", "survival"))

	mods, err := database.GetInstalledMods(ctx, "skyrim-se", "other")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, "other", mods[0].ProfileName)
}

// TestRenameProfile_OtherGameUntouched: profile names are only unique
// within a game, so a same-named profile under another game must not move.
func TestRenameProfile_OtherGameUntouched(t *testing.T) {
	database := renameDB(t)
	ctx := context.Background()
	require.NoError(t, database.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: "999", SourceID: "nexusmods", Version: "1.0", GameID: "fallout4"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
	}))

	require.NoError(t, database.RenameProfile(ctx, "skyrim-se", "default", "survival"))

	mods, err := database.GetInstalledMods(ctx, "fallout4", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
}

// TestRenameProfile_NoRowsIsNotAnError: a profile with nothing installed is
// an ordinary profile, and its rename is purely a config-file operation.
func TestRenameProfile_NoRowsIsNotAnError(t *testing.T) {
	database := renameDB(t)

	require.NoError(t, database.RenameProfile(context.Background(), "skyrim-se", "empty", "still-empty"))
}

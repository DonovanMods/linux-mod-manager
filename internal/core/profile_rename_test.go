package core_test

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

// renameFixture seeds one game with a "default" profile holding one
// installed, deployed mod - so a rename has all three DB tables to move
// (installed_mods, its installed_mod_files rows, and deployed_files) as
// well as the profile file itself.
func renameFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	seedNamedInstalledMod(t, svc, game, "src", "modX", "Mod X", "1.0", true,
		map[string][]byte{"a.esp": []byte("A")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "modX", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return svc, game
}

// TestProfileRename_MovesTheFileAndEveryProfileKeyedRow is the headline:
// after a rename the profile answers under its new name with its mods
// intact, the old name is gone from both the config dir and the DB, and the
// deployed file's ownership row moved with it.
func TestProfileRename_MovesTheFileAndEveryProfileKeyedRow(t *testing.T) {
	svc, game := renameFixture(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()

	renamed, err := pm.Rename(ctx, game.ID, "default", "survival")
	require.NoError(t, err)
	assert.Equal(t, "survival", renamed.Name)
	require.Len(t, renamed.Mods, 1)
	assert.Equal(t, "modX", renamed.Mods[0].ModID)

	// The profile file moved, name inside it included.
	got, err := pm.Get(ctx, game.ID, "survival")
	require.NoError(t, err)
	assert.Equal(t, "survival", got.Name)
	require.Len(t, got.Mods, 1)
	_, err = pm.Get(ctx, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrProfileNotFound, "the old profile file must be gone")

	names, err := pm.ListNames(ctx, game.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"survival"}, names)

	// installed_mods (and, through GetInstalledMod's own read, the row's
	// file ids) moved.
	mods, err := svc.GetInstalledMods(ctx, game.ID, "survival")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, "modX", mods[0].ID)
	assert.Equal(t, "survival", mods[0].ProfileName)

	old, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, old, "no install row may still name the old profile")

	// deployed_files moved: the ownership map answers under the new name.
	sourceID, modID, found, err := svc.GetFileOwner(ctx, game.ID, "survival", "a.esp")
	require.NoError(t, err)
	require.True(t, found, "the deployed file's owner row must have moved with the rename")
	assert.Equal(t, "src", sourceID)
	assert.Equal(t, "modX", modID)

	_, _, found, err = svc.GetFileOwner(ctx, game.ID, "default", "a.esp")
	require.NoError(t, err)
	assert.False(t, found, "no ownership row may still name the old profile")

	// The deployment itself never moves - the game directory holds no
	// profile-derived paths.
	assert.FileExists(t, filepath.Join(game.ModPath, "a.esp"))
}

// TestProfileRename_KeepsTheDefaultFlag: the default-profile setting lives
// INSIDE the profile file, so a renamed default must still be the default.
func TestProfileRename_KeepsTheDefaultFlag(t *testing.T) {
	svc, game := renameFixture(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()

	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	_, err := pm.Rename(ctx, game.ID, "default", "survival")
	require.NoError(t, err)

	def, err := pm.GetDefault(ctx, game.ID)
	require.NoError(t, err)
	assert.Equal(t, "survival", def.Name)
	assert.True(t, def.IsDefault, "the renamed profile must still carry the default flag")
}

// TestProfileRename_KeepsHooksAndOverrides: everything that lives inside
// the profile file travels with it.
func TestProfileRename_KeepsHooksAndOverrides(t *testing.T) {
	svc, game := renameFixture(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()

	profile, err := pm.Get(ctx, game.ID, "default")
	require.NoError(t, err)
	profile.Overrides = map[string][]byte{"cfg/game.ini": []byte("[x]\n")}
	profile.Hooks.Install.BeforeAll = "echo before"
	profile.HooksExplicit.Install.BeforeAll = true
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	renamed, err := pm.Rename(ctx, game.ID, "default", "survival")
	require.NoError(t, err)
	assert.Equal(t, []byte("[x]\n"), renamed.Overrides["cfg/game.ini"])

	reread, err := pm.Get(ctx, game.ID, "survival")
	require.NoError(t, err)
	assert.Equal(t, []byte("[x]\n"), reread.Overrides["cfg/game.ini"])
	assert.Equal(t, "echo before", reread.Hooks.Install.BeforeAll)
}

// TestProfileRename_Refusals: every refusal happens before anything is
// written, so the world is exactly as it was afterwards.
func TestProfileRename_Refusals(t *testing.T) {
	tests := []struct {
		name    string
		old     string
		newName string
		wantErr error
	}{
		{"unknown source profile", "ghost", "survival", domain.ErrProfileNotFound},
		{"target already exists", "default", "taken", nil},
		{"rename onto itself", "default", "default", nil},
		{"invalid target name", "default", "../escape", domain.ErrInvalidProfileName},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, game := renameFixture(t)
			ctx := context.Background()
			pm := svc.NewProfileManager()
			_, err := pm.Create(ctx, game.ID, "taken")
			require.NoError(t, err)

			_, err = pm.Rename(ctx, game.ID, tc.old, tc.newName)
			require.Error(t, err)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}

			// Nothing moved: "default" still holds its mod and its row.
			profile, err := pm.Get(ctx, game.ID, "default")
			require.NoError(t, err)
			require.Len(t, profile.Mods, 1)
			mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
			require.NoError(t, err)
			require.Len(t, mods, 1)
		})
	}
}

// TestProfileRename_RefusalLeavesNoStrayFile pins the "refuse before
// writing" half concretely: an occupied target name must not have been
// overwritten with the source profile's contents.
func TestProfileRename_RefusalLeavesNoStrayFile(t *testing.T) {
	svc, game := renameFixture(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "taken")
	require.NoError(t, err)

	_, err = pm.Rename(ctx, game.ID, "default", "taken")
	require.Error(t, err)

	taken, err := pm.Get(ctx, game.ID, "taken")
	require.NoError(t, err)
	assert.Empty(t, taken.Mods, "the occupied target must be untouched")
}

// TestProfileRename_AlreadyCancelledContextWritesNothing: the entry guard
// refuses a cancelled ctx before any of the three writes, so a rename that
// never started leaves the world untouched. (The other half of Ruling 16 -
// that a rename which HAS started always finishes all three writes - is
// completeRename's own contract, pinned in
// cancellation_ruling16_internal_test.go.)
func TestProfileRename_AlreadyCancelledContextWritesNothing(t *testing.T) {
	svc, game := renameFixture(t)
	pm := svc.NewProfileManager()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pm.Rename(ctx, game.ID, "default", "survival")
	require.ErrorIs(t, err, context.Canceled)

	fresh := context.Background()
	profile, err := pm.Get(fresh, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	_, err = pm.Get(fresh, game.ID, "survival")
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
	require.NoFileExists(t, filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles", "survival.yaml"))
}

// TestServiceRenameProfile_ReturnsTheProfileResultDocument covers the gated
// Service seam the CLI and `lmm serve` both call.
func TestServiceRenameProfile_ReturnsTheProfileResultDocument(t *testing.T) {
	svc, game := renameFixture(t)
	ctx := context.Background()

	result, err := svc.RenameProfile(ctx, game.ID, "default", "survival")
	require.NoError(t, err)
	assert.Equal(t, "survival", result.Profile.Name)
	require.Len(t, result.Profile.Mods, 1)

	entries, err := os.ReadDir(filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "survival.yaml", entries[0].Name())
}

// TestServiceRenameProfile_UnknownProfile: the gate does not swallow the
// typed not-found error a caller branches on.
func TestServiceRenameProfile_UnknownProfile(t *testing.T) {
	svc, game := renameFixture(t)

	_, err := svc.RenameProfile(context.Background(), game.ID, "ghost", "survival")
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
}

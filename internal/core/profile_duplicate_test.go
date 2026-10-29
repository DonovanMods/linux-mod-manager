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

// duplicateFixture seeds one game whose active "default" profile has
// everything a faithful copy has to carry (#438): a non-default, explicit
// link method, a hook, an override, a locked mod, a disabled mod and a
// deliberate load order (b before a, not insertion order), with both mods
// deployed or recorded in the DB.
func duplicateFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(ctx, game))

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("A")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "2.0", true, map[string][]byte{"b.esp": []byte("B")})
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "c", SourceID: "src", Name: "Mod C", Version: "3.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdatePinned, Enabled: false,
	}))

	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	profile, err := pm.Get(ctx, game.ID, "default")
	require.NoError(t, err)
	profile.Mods = []domain.ModReference{
		{SourceID: "src", ModID: "b", Version: "2.0", FileIDs: []string{"fb"}, Locked: true},
		{SourceID: "src", ModID: "a", Version: "1.0"},
		{SourceID: "src", ModID: "c", Version: "3.0", Disabled: true},
	}
	profile.LinkMethod = domain.LinkCopy
	profile.LinkMethodExplicit = true
	profile.Overrides = map[string][]byte{"cfg/game.ini": []byte("[x]\n")}
	profile.IsDefault = true
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	// The hooks go on after the deploy: the copy carries them, nothing
	// here runs them.
	profile, err = pm.Get(ctx, game.ID, "default")
	require.NoError(t, err)
	profile.Hooks.Install.BeforeAll = "/hooks/before.sh"
	profile.HooksExplicit.Install.BeforeAll = true
	profile.HooksExplicit.Uninstall.AfterAll = true // explicitly disabled
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))
	return svc, game
}

// TestDuplicateProfile_IsAFaithfulCopy is #438's own test: every field of
// the profile document round-trips, the duplicate is never the default, the
// original is untouched, and the duplicate has DB rows of its own - shown
// as installed, not deployed, because it is not the active profile.
func TestDuplicateProfile_IsAFaithfulCopy(t *testing.T) {
	svc, game := duplicateFixture(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()

	before, err := pm.Get(ctx, game.ID, "default")
	require.NoError(t, err)
	beforeRows, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	for _, r := range beforeRows {
		require.Equal(t, r.ID != "c", r.Deployed, "fixture: %s's deployed flag", r.ID)
	}

	result, err := svc.DuplicateProfile(ctx, game.ID, "default", "copy")
	require.NoError(t, err)
	assert.Equal(t, "copy", result.Profile.Name)
	assert.False(t, result.Profile.IsDefault)

	dup, err := pm.Get(ctx, game.ID, "copy")
	require.NoError(t, err)
	assert.Equal(t, "copy", dup.Name)
	assert.False(t, dup.IsDefault, "a duplicate is never the active profile by the act of being made")
	assert.Equal(t, before.Mods, dup.Mods, "refs in load order, with version, file_ids, locked and disabled")
	assert.Equal(t, domain.LinkCopy, dup.LinkMethod)
	assert.True(t, dup.LinkMethodExplicit)
	assert.Equal(t, before.Hooks, dup.Hooks)
	assert.Equal(t, before.HooksExplicit, dup.HooksExplicit)
	assert.Equal(t, before.Overrides, dup.Overrides)

	// The original is untouched, default flag included.
	after, err := pm.Get(ctx, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, before, after)
	def, err := pm.GetDefault(ctx, game.ID)
	require.NoError(t, err)
	assert.Equal(t, "default", def.Name)
	afterRows, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, beforeRows, afterRows)

	// One row per source row, re-homed: same version, file ids, policy and
	// enabled flag; never deployed.
	rows, err := svc.GetInstalledMods(ctx, game.ID, "copy")
	require.NoError(t, err)
	require.Len(t, rows, len(beforeRows))
	byID := map[string]domain.InstalledMod{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	for _, src := range beforeRows {
		got, ok := byID[src.ID]
		require.True(t, ok, "duplicate must have a row for %s", src.ID)
		assert.Equal(t, "copy", got.ProfileName)
		assert.Equal(t, src.Version, got.Version)
		assert.Equal(t, src.FileIDs, got.FileIDs)
		assert.Equal(t, src.UpdatePolicy, got.UpdatePolicy)
		assert.Equal(t, src.Enabled, got.Enabled, "%s: the row's enabled flag is copied", src.ID)
		assert.False(t, got.Deployed, "%s: the duplicate is not active, so nothing is deployed for it", src.ID)
	}
	assert.False(t, byID["c"].Enabled, "the disabled mod stays disabled")

	// Nothing on disk is attributed to the duplicate.
	_, _, found, err := svc.GetFileOwner(ctx, game.ID, "copy", "a.esp")
	require.NoError(t, err)
	assert.False(t, found)
}

// TestDuplicateProfile_Refusals: every refusal happens before anything is
// written - the same refusals rename gives for the same reasons.
func TestDuplicateProfile_Refusals(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		newName string
		wantErr error
	}{
		{"unknown source profile", "ghost", "copy", domain.ErrProfileNotFound},
		{"target already exists", "default", "taken", core.ErrProfileExists},
		{"duplicate onto itself", "default", "default", core.ErrProfileExists},
		{"invalid target name", "default", "../escape", domain.ErrInvalidProfileName},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, game := duplicateFixture(t)
			ctx := context.Background()
			pm := svc.NewProfileManager()
			_, err := pm.Create(ctx, game.ID, "taken")
			require.NoError(t, err)

			_, err = svc.DuplicateProfile(ctx, game.ID, tc.source, tc.newName)
			require.ErrorIs(t, err, tc.wantErr)

			names, err := pm.ListNames(ctx, game.ID)
			require.NoError(t, err)
			assert.ElementsMatch(t, []string{"default", "taken"}, names)
			rows, err := svc.GetInstalledMods(ctx, game.ID, "taken")
			require.NoError(t, err)
			assert.Empty(t, rows)
		})
	}
}

// TestDuplicateProfile_RefusesANameOrphanedRowsStillClaim mirrors rename's
// #332 I2 guard: a name free on disk but still holding installed_mods rows
// would otherwise inherit them.
func TestDuplicateProfile_RefusesANameOrphanedRowsStillClaim(t *testing.T) {
	svc, game := duplicateFixture(t)
	ctx := context.Background()
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "z", SourceID: "src", Name: "Z", Version: "1", GameID: game.ID},
		ProfileName: "orphan", UpdatePolicy: domain.UpdateNotify,
	}))

	_, err := svc.DuplicateProfile(ctx, game.ID, "default", "orphan")
	require.ErrorIs(t, err, core.ErrProfileExists)
	_, err = svc.NewProfileManager().Get(ctx, game.ID, "orphan")
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
}

// TestDuplicateProfile_CancelledCtxWritesNothing: a cancellation that lands
// before the gate is free refuses before any write (Ruling 11), so there is
// never a profile file without its rows.
func TestDuplicateProfile_CancelledCtxWritesNothing(t *testing.T) {
	svc, game := duplicateFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.DuplicateProfile(ctx, game.ID, "default", "copy")
	require.ErrorIs(t, err, context.Canceled)
	_, err = svc.NewProfileManager().Get(context.Background(), game.ID, "copy")
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
	rows, err := svc.GetInstalledMods(context.Background(), game.ID, "copy")
	require.NoError(t, err)
	assert.Empty(t, rows)
}

package main

// #667: `lmm update --skip-all` / `--unskip-all` (#542's skip in bulk), and
// the one-line summary that stands in for the mods a collection locks.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withSkipAllFlags(t *testing.T, skip, unskip bool) {
	t.Helper()
	oldSkip, oldUnskip := updateSkipAll, updateUnskipAll
	updateSkipAll, updateUnskipAll = skip, unskip
	t.Cleanup(func() { updateSkipAll, updateUnskipAll = oldSkip, oldUnskip })
}

// lockByCollection makes "a" locked by the profile's collection, titled
// "Night City"; origin says whether it records somewhere to sync from.
func lockByCollection(t *testing.T, svc *core.Service, game *domain.Game, origin bool) {
	t.Helper()
	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	prof.Collection = &domain.CollectionInfo{ID: "night-city", Version: 1, Title: "Night City"}
	if origin {
		prof.Collection.Origin = &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc123", Revision: 1}
	}
	for i := range prof.Mods {
		if prof.Mods[i].ModID == "a" {
			prof.Mods[i].Locked, prof.Mods[i].FromCollection = true, true
		}
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), prof))
}

func TestDoUpdate_SkipAllSkipsEveryOfferedUpdate(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	withSkipAllFlags(t, true, false)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "Skipped 2 updates")
	assert.Contains(t, out, "lmm update --unskip-all")
	assert.Equal(t, "2.0", skippedVersion(t, svc, game, "a"))
	assert.Equal(t, "2.0", skippedVersion(t, svc, game, "b"))

	withSkipAllFlags(t, false, false)
	out, err = captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "No updates to apply.")
}

func TestDoUpdate_UnskipAllOffersThemAgain(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	for _, id := range []string{"a", "b"} {
		_, err := svc.SkipModUpdate(context.Background(), game, "test-src", id, "default", "2.0")
		require.NoError(t, err)
	}
	withSkipAllFlags(t, false, true)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "Cleared 2 skipped updates")
	assert.Empty(t, skippedVersion(t, svc, game, "a"))
	assert.Empty(t, skippedVersion(t, svc, game, "b"))
}

func TestDoUpdate_SkipAllNothingToSkip(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	for _, id := range []string{"a", "b"} {
		_, err := svc.SkipModUpdate(context.Background(), game, "test-src", id, "default", "2.0")
		require.NoError(t, err)
	}
	withSkipAllFlags(t, true, false)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "No updates to skip.")
}

func TestDoUpdate_SkipAllRefusesAModIDAndOtherModes(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	withSkipAllFlags(t, true, false)
	err := doUpdate(context.Background(), svc, game, []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--skip-all")

	withSkipAllFlags(t, true, true)
	err = doUpdate(context.Background(), svc, game, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--unskip-all")

	withSkipAllFlags(t, true, false)
	updateAll = true
	t.Cleanup(func() { updateAll = false })
	err = doUpdate(context.Background(), svc, game, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--all")
}

func TestDoUpdate_CollectionLockedModsAreOneLine(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	lockByCollection(t, svc, game, true)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "1 update(s) available.")
	assert.Contains(t, out, "1 mod locked by collection Night City (not checked) — 'lmm profile sync default --collection' updates it.")
	assert.NotContains(t, out, "Mod A", "a collection-locked mod gets no row")
}

func TestDoUpdate_CollectionLockedWithoutOriginNamesUnlock(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	lockByCollection(t, svc, game, false)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "1 mod locked by collection Night City (not checked) — the collection sets its version; 'lmm mod unlock <mod-id>' checks one yourself.")
}

func TestJSONGolden_UpdateSkipAll(t *testing.T) {
	t.Run("skip", func(t *testing.T) {
		svc, game := setupSkipUpdateTest(t)
		withSkipAllFlags(t, true, false)
		out := runJSONCommand(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
		assertJSONCLIGolden(t, "update_skip_all_result", out)
	})
	t.Run("unskip", func(t *testing.T) {
		svc, game := setupSkipUpdateTest(t)
		_, err := svc.SkipModUpdate(context.Background(), game, "test-src", "a", "default", "2.0")
		require.NoError(t, err)
		withSkipAllFlags(t, false, true)
		out := runJSONCommand(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
		assertJSONCLIGolden(t, "update_unskip_all_result", out)
	})
	t.Run("collection locked", func(t *testing.T) {
		withJSONOutput(t)
		svc, game := setupSkipUpdateTest(t)
		lockByCollection(t, svc, game, true)
		out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
		assertJSONCLIGolden(t, "update_bulk_collection_locked", out)
	})
}

func TestDoModUnlock_SaysTheCollectionLetsGo(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	lockByCollection(t, svc, game, true)

	out, err := captureStdoutErr(t, func() error { return doModUnlock(context.Background(), svc, game, "a") })
	require.NoError(t, err)
	assert.Contains(t, out, "The collection no longer manages its version; 'lmm update' checks it again.")

	out, err = captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "Mod A", "the unlocked mod is checked, and listed, again")
	assert.NotContains(t, out, "locked by collection")
}

package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// lastUpdatedFixture is an in-memory DB whose write clock the test steps
// by hand, so each stamp is a known instant.
func lastUpdatedFixture(t *testing.T) (*DB, *time.Time) {
	t.Helper()
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d.now = func() time.Time { return now }
	return d, &now
}

func lastUpdatedMod(version string) *domain.InstalledMod {
	return &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m", SourceID: "s", GameID: "g", Name: "n", Version: version},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}
}

func getLastUpdated(t *testing.T, d *DB) (installed, updated time.Time) {
	t.Helper()
	got, err := d.GetInstalledMod(context.Background(), "s", "m", "g", "default")
	require.NoError(t, err)
	listed, err := d.GetInstalledMods(context.Background(), "g", "default")
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.True(t, listed[0].LastUpdatedAt.Equal(got.LastUpdatedAt), "both readers decode last_updated_at")
	return got.InstalledAt, got.LastUpdatedAt
}

// TestLastUpdatedAt_MovesOnlyWhenTheVersionChanges pins #596: a row's
// last_updated_at is stamped at insert (equal to installed_at), kept by a
// re-save of the same version, and moved by every write that changes the
// version - the upsert, SetModVersion, SetExternalRevision and a relink.
// installed_at never moves.
func TestLastUpdatedAt_MovesOnlyWhenTheVersionChanges(t *testing.T) {
	ctx := context.Background()
	d, now := lastUpdatedFixture(t)
	t0 := *now

	require.NoError(t, d.SaveInstalledMod(ctx, lastUpdatedMod("1.0")))
	installed, updated := getLastUpdated(t, d)
	assert.True(t, installed.Equal(t0))
	assert.True(t, updated.Equal(t0), "a new row's Updated is its Installed")

	*now = t0.Add(time.Hour)
	require.NoError(t, d.SaveInstalledMod(ctx, lastUpdatedMod("1.0")))
	_, updated = getLastUpdated(t, d)
	assert.True(t, updated.Equal(t0), "re-saving the same version is not an update")

	t1 := t0.Add(2 * time.Hour)
	*now = t1
	require.NoError(t, d.SaveInstalledMod(ctx, lastUpdatedMod("2.0")))
	installed, updated = getLastUpdated(t, d)
	assert.True(t, installed.Equal(t0), "installed_at never moves")
	assert.True(t, updated.Equal(t1), "a new version moves Updated")

	*now = t0.Add(3 * time.Hour)
	require.NoError(t, d.SetModVersion(ctx, "s", "m", "g", "default", "2.0"))
	_, updated = getLastUpdated(t, d)
	assert.True(t, updated.Equal(t1), "SetModVersion to the same version keeps Updated")

	t2 := t0.Add(4 * time.Hour)
	*now = t2
	require.NoError(t, d.SetModVersion(ctx, "s", "m", "g", "default", "3.0"))
	_, updated = getLastUpdated(t, d)
	assert.True(t, updated.Equal(t2), "SetModVersion to a new version moves Updated")

	t3 := t0.Add(5 * time.Hour)
	*now = t3
	relinked := lastUpdatedMod("4.0")
	require.NoError(t, d.RelinkInstalledMod(ctx, "s", "m", relinked))
	_, updated = getLastUpdated(t, d)
	assert.True(t, updated.Equal(t3), "a relink that changes the version moves Updated")

	*now = t0.Add(6 * time.Hour)
	require.NoError(t, d.RelinkInstalledMod(ctx, "s", "m", lastUpdatedMod("4.0")))
	_, updated = getLastUpdated(t, d)
	assert.True(t, updated.Equal(t3), "a relink that keeps the version keeps Updated")
}

// TestLastUpdatedAt_ExternalRevisionMovesIt: Steam updating a Workshop item
// it owns is still the installed version changing (#538's
// SetExternalRevision), so Updated follows it.
func TestLastUpdatedAt_ExternalRevisionMovesIt(t *testing.T) {
	ctx := context.Background()
	d, now := lastUpdatedFixture(t)
	t0 := *now

	ext := lastUpdatedMod("111")
	ext.External = true
	require.NoError(t, d.SaveInstalledMod(ctx, ext))

	*now = t0.Add(time.Hour)
	require.NoError(t, d.SetExternalRevision(ctx, "s", "m", "g", "default", "111", time.Time{}))
	_, updated := getLastUpdated(t, d)
	assert.True(t, updated.Equal(t0), "the same revision is not an update")

	t1 := t0.Add(2 * time.Hour)
	*now = t1
	require.NoError(t, d.SetExternalRevision(ctx, "s", "m", "g", "default", "222", time.Time{}))
	_, updated = getLastUpdated(t, d)
	assert.True(t, updated.Equal(t1))
}

// TestMigrateV23_BackfillsLastUpdatedFromInstalledAt: a row from before the
// column existed reads Updated == Installed, never blank, and a re-run of
// the migration leaves a stamped row alone.
func TestMigrateV23_BackfillsLastUpdatedFromInstalledAt(t *testing.T) {
	ctx := context.Background()
	d, _ := lastUpdatedFixture(t)

	var version int
	require.NoError(t, d.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 26, version)

	_, err := d.Exec(`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, summary, source_url, link_method, installed_at, last_updated_at)
		VALUES ('s', 'm', 'g', 'default', 'n', '1', '', '', '', 0, '2025-05-06T07:08:09Z', NULL)`)
	require.NoError(t, err)

	require.NoError(t, migrateV23(ctx, d))
	installed, updated := getLastUpdated(t, d)
	assert.True(t, updated.Equal(installed), "backfilled from installed_at")

	_, err = d.Exec(`UPDATE installed_mods SET last_updated_at = '2026-01-01T00:00:00Z'`)
	require.NoError(t, err)
	require.NoError(t, migrateV23(ctx, d))
	_, updated = getLastUpdated(t, d)
	assert.True(t, updated.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), "a re-run keeps a stamped row")
}

// TestLastUpdatedAt_UpdateAndRollbackMoveIt covers the writes `lmm update`
// and `lmm update rollback` actually use - ApplyModUpdate, SwapModVersions
// and UpdateModVersion - which bypass the upsert (#596 review).
func TestLastUpdatedAt_UpdateAndRollbackMoveIt(t *testing.T) {
	ctx := context.Background()
	d, now := lastUpdatedFixture(t)
	t0 := *now

	m := lastUpdatedMod("1.0")
	m.FileIDs = []string{"f1"}
	require.NoError(t, d.SaveInstalledMod(ctx, m))

	t1 := t0.Add(time.Hour)
	*now = t1
	require.NoError(t, d.ApplyModUpdate(ctx, "s", "m", "g", "default", "2.0", "", domain.FomodChoices{}, []string{"f2"}))
	installed, updated := getLastUpdated(t, d)
	assert.True(t, installed.Equal(t0), "installed_at never moves")
	assert.True(t, updated.Equal(t1), "ApplyModUpdate moves Updated")

	t2 := t0.Add(2 * time.Hour)
	*now = t2
	require.NoError(t, swapErrOnly(d.SwapModVersions(ctx, "s", "m", "g", "default", domain.FomodChoices{})))
	_, updated = getLastUpdated(t, d)
	assert.True(t, updated.Equal(t2), "a rollback moves Updated")

	t3 := t0.Add(3 * time.Hour)
	*now = t3
	require.NoError(t, d.UpdateModVersion(ctx, "s", "m", "g", "default", "3.0", ""))
	_, updated = getLastUpdated(t, d)
	assert.True(t, updated.Equal(t3), "UpdateModVersion moves Updated")

	*now = t0.Add(4 * time.Hour)
	require.NoError(t, d.ApplyModUpdate(ctx, "s", "m", "g", "default", "3.0", "", domain.FomodChoices{}, []string{"f3"}))
	_, updated = getLastUpdated(t, d)
	assert.True(t, updated.Equal(t3), "re-applying the installed version keeps Updated")
}

// swapErrOnly drops SwapModVersions' restored choices.
func swapErrOnly(_ domain.FomodChoices, err error) error { return err }

package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

func seedCheckMod(t *testing.T, database *db.DB, modID string) {
	t.Helper()
	require.NoError(t, database.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: modID, SourceID: "nexusmods", Name: modID, Version: "1.0", GameID: "g1"},
		ProfileName: "default", Enabled: true, FileIDs: []string{"f1"}, UpdatePolicy: domain.UpdateNotify,
	}))
}

// TestModChecks_RecordReadTouchAndCascade pins #672's per-mod record: one
// per kind (the update check's answer, verify's confirmed version) for each
// installed mod, read back by game and profile, moved forward (never back)
// by a source vouching for it, and gone with the installed row.
func TestModChecks_RecordReadTouchAndCascade(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	ctx := context.Background()
	seedCheckMod(t, database, "1")
	seedCheckMod(t, database, "2")
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	require.NoError(t, database.SaveModCheck(ctx, db.ModCheckUpdate, "g1", "default", db.ModCheck{
		SourceID: "nexusmods", ModID: "1", Version: "1.0", FileIDs: []string{"f1"}, CheckedAt: at, Result: `{"new_version":"2.0"}`, Logic: 3,
	}))
	require.NoError(t, database.SaveModCheck(ctx, db.ModCheckVersion, "g1", "default", db.ModCheck{
		SourceID: "nexusmods", ModID: "1", Version: "1.0", FileIDs: []string{"f1"}, CheckedAt: at,
	}))
	require.NoError(t, database.SaveModCheck(ctx, db.ModCheckUpdate, "g1", "default", db.ModCheck{
		SourceID: "nexusmods", ModID: "2", Version: "1.0", FileIDs: []string{"f1"}, CheckedAt: at,
	}))

	got, err := database.ModChecks(ctx, db.ModCheckUpdate, "g1", "default")
	require.NoError(t, err)
	require.Len(t, got, 2)
	one := got["nexusmods:1"]
	assert.Equal(t, "1.0", one.Version)
	assert.Equal(t, []string{"f1"}, one.FileIDs)
	assert.True(t, at.Equal(one.CheckedAt))
	assert.JSONEq(t, `{"new_version":"2.0"}`, one.Result)
	assert.Equal(t, 3, one.Logic)
	assert.Empty(t, got["nexusmods:2"].Result, "up to date")

	later := at.Add(time.Hour)
	require.NoError(t, database.TouchModChecks(ctx, db.ModCheckUpdate, "g1", "default", "nexusmods",
		map[string]time.Time{"1": later, "2": at.Add(-time.Hour)}))
	got, err = database.ModChecks(ctx, db.ModCheckUpdate, "g1", "default")
	require.NoError(t, err)
	assert.True(t, later.Equal(got["nexusmods:1"].CheckedAt), "moved forward")
	assert.True(t, at.Equal(got["nexusmods:2"].CheckedAt), "never back")

	versions, err := database.ModChecks(ctx, db.ModCheckVersion, "g1", "default")
	require.NoError(t, err)
	assert.Len(t, versions, 1, "the kinds are kept apart")
	require.NoError(t, database.DeleteModCheck(ctx, db.ModCheckVersion, "g1", "default", "nexusmods", "1"))
	versions, err = database.ModChecks(ctx, db.ModCheckVersion, "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, versions)

	require.NoError(t, database.DeleteInstalledMod(ctx, "nexusmods", "1", "g1", "default"))
	got, err = database.ModChecks(ctx, db.ModCheckUpdate, "g1", "default")
	require.NoError(t, err)
	assert.NotContains(t, got, "nexusmods:1", "an uninstalled mod's record goes with it")

	assert.Error(t, database.SaveModCheck(ctx, db.ModCheckUpdate, "g1", "default", db.ModCheck{SourceID: "nexusmods", ModID: "9", CheckedAt: at}),
		"no record for a mod that is not installed")
}

// newCheckedDB is a database with mod "1" installed in g1/default and an
// update record for it.
func newCheckedDB(t *testing.T) (*db.DB, time.Time) {
	t.Helper()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	seedCheckMod(t, database, "1")
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	require.NoError(t, database.SaveModCheck(context.Background(), db.ModCheckUpdate, "g1", "default", db.ModCheck{
		SourceID: "nexusmods", ModID: "1", Version: "1.0", FileIDs: []string{"f1"}, CheckedAt: at,
	}))
	return database, at
}

// TestModChecks_AProfileRenameKeepsItsRecords (#672 review): the record's
// foreign key used to cascade on delete only, so renaming a profile with a
// record failed with FOREIGN KEY constraint failed. The records move with
// the profile.
func TestModChecks_AProfileRenameKeepsItsRecords(t *testing.T) {
	database, at := newCheckedDB(t)
	ctx := context.Background()

	require.NoError(t, database.RenameProfile(ctx, "g1", "default", "renamed"))

	moved, err := database.ModChecks(ctx, db.ModCheckUpdate, "g1", "renamed")
	require.NoError(t, err)
	require.Contains(t, moved, "nexusmods:1")
	assert.True(t, at.Equal(moved["nexusmods:1"].CheckedAt))
	left, err := database.ModChecks(ctx, db.ModCheckUpdate, "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, left)
}

// TestModChecks_ARelinkForgetsTheOldIdentitysRecord (#672 review): a relink
// with a record failed the same way. It succeeds, and the record - about
// the mod the row used to be - does not follow it.
func TestModChecks_ARelinkForgetsTheOldIdentitysRecord(t *testing.T) {
	database, _ := newCheckedDB(t)
	ctx := context.Background()

	require.NoError(t, database.RelinkInstalledMod(ctx, "nexusmods", "1", &domain.InstalledMod{
		Mod:         domain.Mod{ID: "2", SourceID: "nexusmods", Name: "Moved", Version: "1.0", GameID: "g1"},
		ProfileName: "default", Enabled: true, FileIDs: []string{"f1"}, UpdatePolicy: domain.UpdateNotify,
	}))

	got, err := database.ModChecks(ctx, db.ModCheckUpdate, "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, got, "neither the old ID's record nor a copy under the new one")
}

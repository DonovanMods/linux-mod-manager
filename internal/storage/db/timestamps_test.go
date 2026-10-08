package db

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #515: installed_at and updated_at were bound as raw time.Time, which the
// modernc.org/sqlite driver writes with time.Time.String() - local zone
// text, monotonic "m=+…" suffix and all. These tests pin the replacement: UTC,
// fixed-width RFC 3339, and a migration for the rows already written.

func openMemory(t *testing.T) *DB {
	t.Helper()
	database, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	return database
}

// rawText reads a DATETIME column untouched. The CAST drops the declared
// type, so the driver hands back the stored text instead of a time.Time.
func rawText(t *testing.T, d *DB, col, modID string) *string {
	t.Helper()
	var s *string
	require.NoError(t, d.QueryRow(
		"SELECT CAST("+col+" AS TEXT) FROM installed_mods WHERE mod_id = ?", modID).Scan(&s))
	return s
}

func timestampMod(id string, updated time.Time) *domain.InstalledMod {
	return &domain.InstalledMod{
		Mod: domain.Mod{
			ID: id, SourceID: "nexusmods", Name: "Mod " + id, Version: "1.0.0",
			GameID: "g", UpdatedAt: updated,
		},
		ProfileName: "default",
	}
}

func TestSaveInstalledMod_StoresUTCRFC3339WithoutMonotonicSuffix(t *testing.T) {
	ctx := context.Background()
	d := openMemory(t)

	// time.Now() carries a monotonic reading; a fixed zone stands in for the
	// user's local one whatever this machine's TZ is.
	edt := time.FixedZone("EDT", -4*3600)
	d.now = func() time.Time { return time.Now().In(edt) }
	updated := time.Now().In(edt)
	require.NoError(t, d.SaveInstalledMod(ctx, timestampMod("1", updated)))

	for _, col := range []string{"installed_at", "updated_at"} {
		raw := rawText(t, d, col, "1")
		require.NotNil(t, raw, col)
		assert.NotContains(t, *raw, "m=", col)
		assert.NotContains(t, *raw, "EDT", col)
		assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$`, *raw, col)
		_, err := time.Parse(time.RFC3339Nano, *raw)
		assert.NoError(t, err, col)
	}

	got, err := d.GetInstalledMod(ctx, "nexusmods", "1", "g", "default")
	require.NoError(t, err)
	assert.True(t, got.UpdatedAt.Equal(updated), "updated_at reads back the instant written")
	assert.Equal(t, "UTC", got.UpdatedAt.Location().String())
	assert.Equal(t, "UTC", got.InstalledAt.Location().String())
	assert.WithinDuration(t, time.Now(), got.InstalledAt, time.Minute)
}

func TestSaveInstalledMod_ZeroUpdatedAtStaysNull(t *testing.T) {
	d := openMemory(t)
	require.NoError(t, d.SaveInstalledMod(context.Background(), timestampMod("1", time.Time{})))
	assert.Nil(t, rawText(t, d, "updated_at", "1"))
}

// Text order of the old String() form followed the wall clock in whatever
// zone wrote it, so these rows came back out of order. Each pair below
// sorts wrongly as text and right as an instant.
func TestGetInstalledMods_OrdersByInstantAcrossZonesAndDST(t *testing.T) {
	ctx := context.Background()
	d := openMemory(t)
	edt := time.FixedZone("EDT", -4*3600)
	est := time.FixedZone("EST", -5*3600)

	// Listed in true chronological order.
	chrono := []struct {
		id string
		at time.Time
	}{
		{"utc-early", time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)},             // 01:00Z Oct 2
		{"edt-late", time.Date(2026, 10, 1, 23, 0, 0, 0, edt)},                  // 03:00Z Oct 2, but "10-01" as local text
		{"dst-edt", time.Date(2026, 11, 1, 1, 30, 0, 0, edt)},                   // 05:30Z
		{"dst-est-early", time.Date(2026, 11, 1, 1, 15, 0, 0, est)},             // 06:15Z, "01:15" < "01:30" as text
		{"dst-est-late", time.Date(2026, 11, 1, 1, 30, 0, 0, est)},              // 06:30Z
		{"fractional", time.Date(2026, 11, 1, 6, 30, 0, 500_000_000, time.UTC)}, // sorts after the whole second
		{"whole-second-later", time.Date(2026, 11, 1, 6, 30, 1, 0, time.UTC)},   // "…01Z" vs "…00.5Z"
	}
	// Written in a scrambled order so insertion order cannot pass the test.
	for _, i := range []int{4, 0, 6, 2, 5, 1, 3} {
		c := chrono[i]
		d.now = func() time.Time { return c.at }
		require.NoError(t, d.SaveInstalledMod(ctx, timestampMod(c.id, time.Time{})))
	}

	mods, err := d.GetInstalledMods(ctx, "g", "default")
	require.NoError(t, err)
	var got, want []string
	for _, m := range mods {
		got = append(got, m.ID)
	}
	for _, c := range chrono {
		want = append(want, c.id)
	}
	assert.Equal(t, want, got)
}

// A row the migration somehow missed still has to read.
func TestGetInstalledMod_ReadsTheLegacyStringForm(t *testing.T) {
	ctx := context.Background()
	d := openMemory(t)
	require.NoError(t, d.SaveInstalledMod(ctx, timestampMod("1", time.Time{})))
	_, err := d.Exec(`UPDATE installed_mods SET
		installed_at = '2026-10-01 13:30:57.086694056 -0400 EDT m=+1.598230846',
		updated_at = '2026-09-30 06:27:17.013 +0000 UTC'`)
	require.NoError(t, err)

	got, err := d.GetInstalledMod(ctx, "nexusmods", "1", "g", "default")
	require.NoError(t, err)
	assert.True(t, got.InstalledAt.Equal(time.Date(2026, 10, 1, 17, 30, 57, 86694056, time.UTC)))
	assert.Equal(t, "UTC", got.InstalledAt.Location().String())
	assert.True(t, got.UpdatedAt.Equal(time.Date(2026, 9, 30, 6, 27, 17, 13_000_000, time.UTC)))

	mods, err := d.GetInstalledMods(ctx, "g", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, "UTC", mods[0].InstalledAt.Location().String())
}

func TestMigrateV19_RewritesEveryLegacyForm(t *testing.T) {
	ctx := context.Background()
	d := openMemory(t)
	var logs bytes.Buffer
	d.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cases := []struct {
		name, id         string
		installed        string
		updated          *string // nil -> NULL
		wantInstalled    string
		wantUpdated      *string
		wantInstalledRaw bool // left untouched
	}{
		{name: "the issue's sample, monotonic suffix and EDT", id: "sample",
			installed:     "2026-10-01 13:30:57.086694056 -0400 EDT m=+1.598230846",
			updated:       ptr("2026-09-30 06:27:17.013 +0000 UTC"),
			wantInstalled: "2026-10-01T17:30:57.086694056Z",
			wantUpdated:   ptr("2026-09-30T06:27:17.013000000Z")},
		{name: "negative monotonic reading", id: "neg",
			installed:     "2026-10-01 13:30:57.5 -0400 EDT m=-0.000123",
			wantInstalled: "2026-10-01T17:30:57.500000000Z"},
		{name: "no monotonic suffix, EST", id: "est",
			installed:     "2026-12-01 08:00:00 -0500 EST",
			wantInstalled: "2026-12-01T13:00:00.000000000Z"},
		{name: "zone with no abbreviation", id: "noabbr",
			installed:     "2026-10-01 13:30:57.25 +0530 +0530 m=+2.0",
			wantInstalled: "2026-10-01T08:00:57.250000000Z"},
		{name: "CURRENT_TIMESTAMP default", id: "ct",
			installed:     "2026-09-30 06:27:17",
			wantInstalled: "2026-09-30T06:27:17.000000000Z"},
		{name: "driver sqlite format", id: "drv",
			installed:     "2026-09-30 06:27:17.5-04:00",
			wantInstalled: "2026-09-30T10:27:17.500000000Z"},
		{name: "already in the new form", id: "new",
			installed:     "2026-09-30T10:27:17.500000000Z",
			wantInstalled: "2026-09-30T10:27:17.500000000Z"},
		{name: "unparseable is kept as it was", id: "junk",
			installed:     "last tuesday",
			updated:       ptr("also junk"),
			wantInstalled: "last tuesday", wantUpdated: ptr("also junk")},
	}
	for _, c := range cases {
		_, err := d.Exec(`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, summary, source_url, link_method, installed_at, updated_at)
			VALUES ('s', ?, 'g', 'default', ?, '1', '', '', '', 0, ?, ?)`, c.id, c.name, c.installed, c.updated)
		require.NoError(t, err)
	}

	_, err := d.Exec("DELETE FROM schema_migrations WHERE version >= 19")
	require.NoError(t, err)
	require.NoError(t, d.migrate(ctx))

	var version int
	require.NoError(t, d.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 20, version)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotInstalled string
			require.NoError(t, d.QueryRow("SELECT CAST(installed_at AS TEXT) FROM installed_mods WHERE mod_id = ?", c.id).Scan(&gotInstalled))
			assert.Equal(t, c.wantInstalled, gotInstalled)
			var gotUpdated *string
			require.NoError(t, d.QueryRow("SELECT CAST(updated_at AS TEXT) FROM installed_mods WHERE mod_id = ?", c.id).Scan(&gotUpdated))
			assert.Equal(t, c.wantUpdated, gotUpdated)
		})
	}

	// Nothing was dropped, and the one it could not read was reported.
	var n int
	require.NoError(t, d.QueryRow("SELECT COUNT(*) FROM installed_mods").Scan(&n))
	assert.Equal(t, len(cases), n)
	assert.Contains(t, logs.String(), "last tuesday")
	assert.Contains(t, logs.String(), "junk")
	assert.NotContains(t, logs.String(), "sample", "a convertible row is not reported")

	// Ordering is right straight after the upgrade.
	got, err := d.GetInstalledMod(ctx, "s", "sample", "g", "default")
	require.NoError(t, err)
	assert.True(t, got.InstalledAt.Equal(time.Date(2026, 10, 1, 17, 30, 57, 86694056, time.UTC)))
}

func ptr[T any](v T) *T { return &v }

// A row the migration could not read stays unreadable rather than reading as
// a mod with no date, and the error names what is wrong with it.
func TestGetInstalledMod_UnreadableTimeIsAnError(t *testing.T) {
	ctx := context.Background()
	d := openMemory(t)
	require.NoError(t, d.SaveInstalledMod(ctx, timestampMod("1", time.Time{})))
	_, err := d.Exec(`UPDATE installed_mods SET installed_at = 'last tuesday'`)
	require.NoError(t, err)

	_, err = d.GetInstalledMod(ctx, "nexusmods", "1", "g", "default")
	require.ErrorContains(t, err, "last tuesday")
	_, err = d.GetInstalledMods(ctx, "g", "default")
	require.ErrorContains(t, err, "last tuesday")
}

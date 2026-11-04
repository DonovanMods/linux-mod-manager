package db_test

// #594: a row stored before source text was sanitized at ingestion can
// still carry raw control sequences; every installed-row read cleans the
// display fields so `lmm list` and the web UI never see them, without
// rewriting the database.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstalledModReads_SanitizePreFixRows(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	ctx := context.Background()

	const evil = "\x1b[2J\x1b]0;t\x07\u009b\u202E"
	require.NoError(t, database.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{
			ID: "1", SourceID: "nexusmods", GameID: "g", Name: "Name 日本" + evil, Version: "1.0" + evil,
			Author: "Auth" + evil, Summary: "Sum\n" + evil, SourceURL: "https://x/" + evil, CategoryName: "Cat" + evil,
		},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	// The raw bytes really are in the row: the write path does not clean.
	_, err = database.ExecContext(ctx, `UPDATE installed_mods SET previous_version = ?, skipped_version = ?`, "0.9"+evil, "1.1"+evil)
	require.NoError(t, err)

	want := func(t *testing.T, m domain.InstalledMod) {
		t.Helper()
		assert.Equal(t, "Name 日本[2J]0;t", m.Name)
		assert.Equal(t, "1.0[2J]0;t", m.Version)
		assert.Equal(t, "Auth[2J]0;t", m.Author)
		assert.Equal(t, "Sum [2J]0;t", m.Summary)
		assert.Equal(t, "https://x/[2J]0;t", m.SourceURL)
		assert.Equal(t, "Cat[2J]0;t", m.CategoryName)
		assert.Equal(t, "0.9[2J]0;t", m.PreviousVersion)
		assert.Equal(t, "1.1[2J]0;t", m.SkippedVersion)
		assert.Equal(t, "1", m.ID)
	}

	got, err := database.GetInstalledMod(ctx, "nexusmods", "1", "g", "default")
	require.NoError(t, err)
	want(t, *got)

	mods, err := database.GetInstalledMods(ctx, "g", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	want(t, mods[0])

	rows, err := database.ModVersionRows(ctx, "nexusmods", "1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "1.0[2J]0;t", rows[0].Version)
	assert.Equal(t, "0.9[2J]0;t", rows[0].PreviousVersion)

	require.NoError(t, database.SetModEnabled(ctx, "nexusmods", "1", "g", "default", false))
	disabled, err := database.DisabledModRows(ctx)
	require.NoError(t, err)
	require.Len(t, disabled, 1)
	assert.Equal(t, "Name 日本[2J]0;t", disabled[0].Name)
}

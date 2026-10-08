package main

// #542: `lmm mod skip-update` / `unskip-update`, and the bulk `lmm update`
// that leaves a skipped update out and names it in the skip summary.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupSkipUpdateTest installs "a" (Mod A) and "b" (Mod B) at 1.0 under
// test-src, which offers 2.0 for both.
func setupSkipUpdateTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, src := setupDoUpdateTest(t)
	ctx := context.Background()
	_, err := svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	for id, name := range map[string]string{"a": "Mod A", "b": "Mod B"} {
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: id, SourceID: "test-src", Name: name, Version: "1.0", GameID: game.ID},
			ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true,
		}))
		require.NoError(t, svc.NewProfileManager().UpsertMod(ctx, game.ID, "default",
			domain.ModReference{SourceID: "test-src", ModID: id, Version: "1.0"}))
		src.AddMod(&domain.Mod{ID: id, SourceID: "test-src", Name: name, Version: "2.0", GameID: game.ID}, nil)
	}

	oldSource, oldProfile, oldVersion := modSource, modProfile, modSkipVersion
	modSource, modProfile, modSkipVersion = "", "", ""
	t.Cleanup(func() { modSource, modProfile, modSkipVersion = oldSource, oldProfile, oldVersion })
	return svc, game
}

func skippedVersion(t *testing.T, svc *core.Service, game *domain.Game, modID string) string {
	t.Helper()
	row, err := svc.GetInstalledMod(context.Background(), "test-src", modID, game.ID, "default")
	require.NoError(t, err)
	return row.SkippedVersion
}

func TestDoModSkipUpdate_DefaultsToTheOfferedVersion(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)

	out, err := captureStdoutErr(t, func() error { return doModSkipUpdate(context.Background(), svc, game, "a") })
	require.NoError(t, err)
	assert.Contains(t, out, "Mod A: update 2.0 skipped")
	assert.Equal(t, "2.0", skippedVersion(t, svc, game, "a"))
}

func TestDoModSkipUpdate_ExplicitVersion(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	modSkipVersion = "1.5"

	_, err := captureStdoutErr(t, func() error { return doModSkipUpdate(context.Background(), svc, game, "a") })
	require.NoError(t, err)
	assert.Equal(t, "1.5", skippedVersion(t, svc, game, "a"))
}

func TestDoModUnskipUpdate_ClearsTheSkip(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "test-src", "a", "default", "2.0")
	require.NoError(t, err)

	out, err := captureStdoutErr(t, func() error { return doModUnskipUpdate(context.Background(), svc, game, "a") })
	require.NoError(t, err)
	assert.Contains(t, out, "Mod A: skipped update cleared")
	assert.Empty(t, skippedVersion(t, svc, game, "a"))
}

func TestDoUpdate_SkippedUpdateIsNamedNotOffered(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "test-src", "a", "default", "2.0")
	require.NoError(t, err)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "1 update(s) available.")
	assert.Contains(t, out, "1 skipped update")
	assert.Contains(t, out, "Mod A 2.0")
	assert.Contains(t, out, "lmm mod unskip-update a -s test-src")
}

func TestDoUpdate_EverythingSkippedIsNotUpToDate(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	for _, id := range []string{"a", "b"} {
		_, err := svc.SkipModUpdate(context.Background(), game, "test-src", id, "default", "2.0")
		require.NoError(t, err)
	}

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.NotContains(t, out, "All mods are up to date.", "two skipped updates are not 'up to date'")
	assert.Contains(t, out, "No updates to apply.")
	assert.Contains(t, out, "2 skipped updates")
}

func TestJSONGolden_UpdateBulkSkipped(t *testing.T) {
	withJSONOutput(t)
	svc, game := setupSkipUpdateTest(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "test-src", "a", "default", "2.0")
	require.NoError(t, err)

	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	assertJSONCLIGolden(t, "update_bulk_skipped", out)
}

func TestJSONGolden_ModSkipUpdate(t *testing.T) {
	t.Run("skip", func(t *testing.T) {
		svc, game := setupSkipUpdateTest(t)
		out := runJSONCommand(t, func() error { return doModSkipUpdate(context.Background(), svc, game, "a") })
		assertJSONCLIGolden(t, "mod_skip_update_result", out)
	})
	t.Run("unskip", func(t *testing.T) {
		svc, game := setupSkipUpdateTest(t)
		_, err := svc.SkipModUpdate(context.Background(), game, "test-src", "a", "default", "2.0")
		require.NoError(t, err)
		out := runJSONCommand(t, func() error { return doModUnskipUpdate(context.Background(), svc, game, "a") })
		assertJSONCLIGolden(t, "mod_unskip_update_result", out)
	})
}

func TestDoModShow_SaysTheUpdateIsSkipped(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "test-src", "a", "default", "2.0")
	require.NoError(t, err)

	out, err := captureStdoutErr(t, func() error { return doModShow(context.Background(), svc, game, "a") })
	require.NoError(t, err)
	assert.Contains(t, out, "Skipped update: 2.0 — 'lmm mod unskip-update a' to show it again")
}

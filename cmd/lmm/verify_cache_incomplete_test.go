package main

// #642: a cache entry that lost a member and its completion marker reads as
// CACHE INCOMPLETE, --fix re-downloads it, and the deployed file is never
// removed as stale.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

func TestDoVerify_CacheIncomplete_ReportsThenFixes(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	ctx := context.Background()
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: game.ID},
		[]domain.DownloadableFile{{ID: "1", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}})
	src.AddDownload("1", []byte("plugin content"))
	plan, err := svc.PlanInstall(ctx, game, "default", "test-src", "mod1", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	deployed := filepath.Join(game.ModPath, "mod1.esp")
	require.FileExists(t, deployed)

	entry := svc.GetGameCache(game).ModPath(cache.Key{GameID: game.ID, SourceID: "test-src", ModID: "mod1", Version: "1.0"})
	require.NoError(t, os.Remove(filepath.Join(entry, "mod1.esp")))
	require.NoError(t, os.Remove(filepath.Join(entry, cache.ReservedPrefix+"file-1")))

	verifyProfile = "default"
	oldJSON := jsonOutput
	t.Cleanup(func() { verifyProfile, jsonOutput, verifyFix = "", oldJSON, false })

	jsonOutput = false
	out := captureStdout(t, func() error { return doVerify(ctx, svc, game, nil) })
	assert.Contains(t, out, "X Mod One (1) - CACHE INCOMPLETE (the cache entry is missing mod1.esp, which this profile deployed)")
	assert.NotContains(t, out, "STALE DEPLOYMENT")
	assert.Contains(t, out, "1 issue(s)")

	jsonOutput = true
	out = captureStdout(t, func() error { return doVerify(ctx, svc, game, nil) })
	var doc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	var row *core.VerifyFinding
	for i := range doc.Result.Findings {
		if doc.Result.Findings[i].Status == "cache_incomplete" {
			row = &doc.Result.Findings[i]
		}
	}
	require.NotNil(t, row, "%+v", doc.Result.Findings)
	assert.Equal(t, "1", row.FileID)
	assert.True(t, row.Fixable)
	assert.Contains(t, row.Note, "mod1.esp")

	jsonOutput, verifyFix = false, true
	out = captureStdout(t, func() error { return doVerify(ctx, svc, game, nil) })
	assert.Contains(t, out, "CACHE INCOMPLETE")
	assert.Contains(t, out, "Re-downloaded OK")
	assert.NotContains(t, out, "Fixed: removed mod1.esp")
	assert.FileExists(t, filepath.Join(entry, "mod1.esp"), "the entry is whole again")
	b, err := os.ReadFile(deployed)
	require.NoError(t, err, "the deployed file is still there, and resolves")
	assert.Equal(t, "plugin content", string(b))
}

// A locally imported mod's row says why --fix cannot repair it.
func TestRenderVerifyFinding_CacheIncomplete(t *testing.T) {
	out := captureStdout(t, func() error {
		renderVerifyFinding(core.VerifyEvent{Finding: core.VerifyFinding{
			ModName: "Local Mod", Status: core.VerifyStatusCacheIncomplete,
			Note: "the cache entry is missing a.esp", FixableReason: "the mod was imported locally, so there is no source to re-download from",
		}})
		return nil
	})
	assert.Contains(t, out, "X Local Mod - CACHE INCOMPLETE (the cache entry is missing a.esp)")
	assert.Contains(t, out, "  the mod was imported locally, so there is no source to re-download from")
}

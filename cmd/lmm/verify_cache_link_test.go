package main

// #659: a link or special file left in a mod's cache entry reads as LINK IN
// CACHE, naming the mod and the path; --fix removes it.

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

// plantCacheLink puts a symlink at evil.esp in mod1's cache entry, and
// returns the entry.
func plantCacheLink(t *testing.T, svc *core.Service, game *domain.Game) string {
	t.Helper()
	entry := svc.GetGameCache(game).ModPath(cache.Key{GameID: game.ID, SourceID: "test-src", ModID: "mod1", Version: "1.0"})
	require.NoError(t, os.Symlink("/etc/hostname", filepath.Join(entry, "evil.esp")))
	return entry
}

func TestDoVerify_CacheLink_ReportsThenFixes(t *testing.T) {
	cmd, svc, game, _ := setupDoVerifyVersionTest(t, "1.0", []string{"2"}, []domain.DownloadableFile{
		{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"},
	})
	ctx := cmd.Context()
	entry := plantCacheLink(t, svc, game)
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput, verifyFix = oldJSON, false })

	jsonOutput = false
	out := captureStdout(t, func() error { return doVerify(ctx, svc, game, nil) })
	assert.Contains(t, out, "? Mod One - LINK IN CACHE (evil.esp in the cache entry is a symbolic link; lmm keeps none in a mod's cache, never deploys or follows one, and --fix removes it)")
	assert.Contains(t, out, "1 warning(s)")

	jsonOutput = true
	out = captureStdout(t, func() error { return doVerify(ctx, svc, game, nil) })
	var doc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	var row *core.VerifyFinding
	for i := range doc.Result.Findings {
		if doc.Result.Findings[i].Status == "cache_link" {
			row = &doc.Result.Findings[i]
		}
	}
	require.NotNil(t, row, "%+v", doc.Result.Findings)
	assert.Equal(t, "mod1", row.ModID)
	assert.Equal(t, "evil.esp", row.FileID)
	assert.True(t, row.Fixable)

	jsonOutput, verifyFix = false, true
	out = captureStdout(t, func() error { return doVerify(ctx, svc, game, nil) })
	assert.Contains(t, out, "Fixed: Mod One - removed evil.esp, a symbolic link, from the cache entry")
	_, err := os.Lstat(filepath.Join(entry, "evil.esp"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	b, err := os.ReadFile("/etc/hostname")
	if err == nil {
		assert.NotEmpty(t, b, "the target is untouched")
	}
}

// A removal that failed says why, under the row.
func TestRenderVerifyFinding_CacheLinkNotRemoved(t *testing.T) {
	out := captureStdout(t, func() error {
		renderVerifyFinding(core.VerifyEvent{Finding: core.VerifyFinding{
			ModName: "Mod One", Status: core.VerifyStatusCacheLink, FileID: "evil.esp",
			Note: "could not remove evil.esp: permission denied", FixableReason: "this --fix run already attempted a repair for it",
		}})
		return nil
	})
	assert.Contains(t, out, "? Mod One - LINK IN CACHE (could not remove evil.esp: permission denied)")
	assert.Contains(t, out, "  this --fix run already attempted a repair for it")
}

func TestJSONGolden_VerifyCacheLink(t *testing.T) {
	for _, fix := range []bool{false, true} {
		name := "verify_cache_link"
		if fix {
			name += "_fix"
		}
		t.Run(name, func(t *testing.T) {
			_, svc, game, _ := setupDoVerifyVersionTest(t, "1.0", []string{"2"}, []domain.DownloadableFile{
				{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"},
			})
			plantCacheLink(t, svc, game)
			withJSONOutput(t)
			old := verifyFix
			verifyFix = fix
			t.Cleanup(func() { verifyFix = old })

			out := captureStdout(t, func() error { return doVerify(context.Background(), svc, game, nil) })
			assertJSONCLIGolden(t, name, out, game.ModPath, "<GAME-DIR>")
		})
	}
}

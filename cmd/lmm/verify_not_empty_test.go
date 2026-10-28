package main

// #429: `lmm verify` printed "No installed mods to verify." for a game with
// installed mods it has no checksums for - Workshop items tracked from
// Steam, checksum-less imports - and an unsubscribed item's row had no text
// line at all. The run now says what it covered.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedVerifyExternal records a Steam Workshop item as `lmm import
// --workshop` does and returns its Steam directory.
func seedVerifyExternal(t *testing.T, svc interface {
	SaveInstalledMod(context.Context, *domain.InstalledMod) error
}, game *domain.Game, modID, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), modID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mod.dll"), []byte("steam"), 0o644))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: modID, SourceID: "steamworkshop", Name: name, Version: "7987119735124793734", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
		External: true, ExternalPath: dir,
	}))
	return dir
}

func TestDoVerify_AnAllExternalLoaderGameSaysWhatItChecked(t *testing.T) {
	cmd, svc, game := setupVerifySummaryGame(t, "")
	seedVerifyExternal(t, svc, game, "3000000001", "ModMenu")
	gone := seedVerifyExternal(t, svc, game, "3000000002", "Unsubscribed")
	require.NoError(t, os.RemoveAll(gone))

	out, result := verifyTally(t, cmd, svc, game)
	assert.NotContains(t, out, "No installed mods to verify.")
	assert.Contains(t, out, "Verifying 2 installed mod(s)...")
	assert.Contains(t, lineContaining(out, "ModMenu"), "tracked from Steam - present on disk", "a present item is named")
	missing := lineContaining(out, "Unsubscribed")
	assert.Contains(t, missing, "X Unsubscribed")
	assert.Contains(t, missing, "Steam no longer has this item on disk", "an unsubscribed item has a line, not only a count")
	assert.Contains(t, out, "loader", "the loader tier ran: %s", out)
	assert.Equal(t, 2, result.Mods)
	assert.Equal(t, 2, result.External)
	assert.Contains(t, out, "issue(s)")
}

func TestDoVerify_ChecksumlessImportsAreNotNoInstalledMods(t *testing.T) {
	cmd, svc, game := setupVerifySummaryGame(t, "")
	bepinexLog := filepath.Join(game.InstallPath, filepath.FromSlash(domain.BepInExLogPath))
	require.NoError(t, os.WriteFile(bepinexLog, []byte("ran"), 0o644))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "imp", SourceID: domain.SourceLocal, Name: "Imported", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))

	out, result := verifyTally(t, cmd, svc, game)
	assert.NotContains(t, out, "No installed mods to verify.")
	assert.Contains(t, out, "1 mod(s) have no recorded files for lmm to check")
	assert.Equal(t, 1, result.Unverified)
}

func TestDoVerify_AnEmptyProfileStillSaysSo(t *testing.T) {
	cmd, svc, game := setupVerifySummaryGame(t, "")
	bepinexLog := filepath.Join(game.InstallPath, filepath.FromSlash(domain.BepInExLogPath))
	require.NoError(t, os.WriteFile(bepinexLog, []byte("ran"), 0o644))

	out, result := verifyTally(t, cmd, svc, game)
	assert.Contains(t, out, "No installed mods to verify.")
	assert.Zero(t, result.Mods)
}

// TestDoVerify_FilteredWorkshopModOnAChecksummedProfileIsNotNoFilesFound
// (#429): a `lmm verify <workshop-id>` filter naming a tracked Steam
// item, on a profile that ALSO has a checksummed mod (so HasFiles is true
// and the branch with files runs), printed the "+ ... tracked from Steam -
// present on disk" row and then, wrongly, "No files found for mod <id>" -
// Checked stays 0 for an external row (externalPresencePass never
// increments it), but the filter DID match a mod. "No files found" is for a
// filter that matched nothing at all.
func TestDoVerify_FilteredWorkshopModOnAChecksummedProfileIsNotNoFilesFound(t *testing.T) {
	cmd, svc, game := setupVerifySummaryGame(t, "")
	seedVerifySummaryMod(t, svc, game)
	seedVerifyExternal(t, svc, game, "3000000001", "ModMenu")

	out := captureStdout(t, func() error { return doVerify(cmd.Context(), svc, game, []string{"3000000001"}) })
	assert.Contains(t, out, "tracked from Steam - present on disk")
	assert.NotContains(t, out, "No files found for mod", "the filter matched ModMenu; this message is for a filter that matched nothing")
}

// TestDoVerify_AFilterMatchingNothingStillSaysNoFilesFound is the other
// half of the same rule: a mod-id filter that matches NO installed mod at
// all (Mods stays 0) still gets the "No files found" message.
func TestDoVerify_AFilterMatchingNothingStillSaysNoFilesFound(t *testing.T) {
	cmd, svc, game := setupVerifySummaryGame(t, "")
	seedVerifySummaryMod(t, svc, game)

	out := captureStdout(t, func() error { return doVerify(cmd.Context(), svc, game, []string{"does-not-exist"}) })
	assert.Contains(t, out, "No files found for mod does-not-exist")
}

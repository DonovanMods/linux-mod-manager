package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyCommand_Exists(t *testing.T) {
	// Verify the command is registered
	cmd := rootCmd
	verifyCmd, _, err := cmd.Find([]string{"verify"})
	assert.NoError(t, err)
	assert.Equal(t, "verify", verifyCmd.Name())
}

func TestVerifyCommand_HasFixFlag(t *testing.T) {
	cmd := rootCmd
	verifyCmd, _, err := cmd.Find([]string{"verify"})
	assert.NoError(t, err)

	flag := verifyCmd.Flags().Lookup("fix")
	assert.NotNil(t, flag)
}

func TestVerifyCommand_AcceptsOptionalModID(t *testing.T) {
	cmd := rootCmd
	verifyCmd, _, err := cmd.Find([]string{"verify"})
	assert.NoError(t, err)

	// Command should accept 0 or 1 arguments
	assert.Contains(t, verifyCmd.Use, "[mod-id]")
}

// --- doVerify version-record check (issue #94, Task A6) ---
//
// setupDoVerifyVersionTest seeds a fakeInstallSource-backed service with a
// single installed mod row (SourceID "test-src", ModID "mod1") whose
// recorded Version/FileIDs the caller controls, plus a matching cache entry
// and stored checksum so the pre-existing file-count/checksum checks in
// doVerify report clean ("ok") and don't add noise to issues/warnings -
// isolating the new version-record pre-pass as the only thing under test.
// sourceFiles is what the fake source's GetModFiles returns for mod1 (the
// upstream truth the check compares FileIDs against).
func setupDoVerifyVersionTest(t *testing.T, recordedVersion string, fileIDs []string, sourceFiles []domain.DownloadableFile) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()

	svc, game, src := setupDoInstallTest(t)

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: recordedVersion, GameID: game.ID}, sourceFiles)

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:         domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: recordedVersion, GameID: game.ID},
		ProfileName: "default",
		Enabled:     true,
		FileIDs:     fileIDs,
	}))

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", recordedVersion, "mod1.esp", []byte("plugin content")))
	for _, id := range fileIDs {
		require.NoError(t, svc.SaveFileChecksum("test-src", "mod1", game.ID, "default", id, "deadbeef"))
	}

	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	return cmd, svc, game
}

// TestDoVerify_VersionMismatch_ReportedAsIssue guards issue #94's detection
// half: an installed row recorded as version "1.5" whose stored file ID
// "2" is, per the source, actually version "1.0" - the version of the bytes
// really on disk. doVerify must flag this as "version_mismatch" (an issue,
// not a warning) and show both the recorded and source-reported values, in
// both text and --json output. Task A7 adds the --fix repair; this task is
// detection only.
func TestDoVerify_VersionMismatch_ReportedAsIssue(t *testing.T) {
	cmd, svc, game := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"},
	})

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out, "1.5", "text must show the recorded version")
	assert.Contains(t, out, "1.0", "text must show the source-reported (effective) version")
	assert.Contains(t, out, "1 issue(s)")
	assert.NotContains(t, out, "0 issue(s)")

	jsonOutput = true
	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 1, result.Issues, "version mismatch must be bucketed as an issue")
	assert.Equal(t, 0, result.Warnings)

	found := false
	for _, f := range result.Files {
		if f.ModID == "mod1" && f.Status == "version_mismatch" {
			found = true
			assert.Empty(t, f.FileID, "FileID left empty per brief - fix branch fills it in later")
		}
	}
	assert.True(t, found, "expected a version_mismatch entry in JSON files: %+v", result.Files)
}

// TestDoVerify_VersionUnverifiable_ReportedAsWarning guards the other half:
// when the source no longer lists the stored file ID at all (superseded/
// removed upstream), doVerify can't compute an effective version to compare
// against, so it must report "version_unverifiable" as a warning (not an
// issue - there is nothing to definitively repair) rather than silently
// treating it as OK or crashing.
func TestDoVerify_VersionUnverifiable_ReportedAsWarning(t *testing.T) {
	cmd, svc, game := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "3", Name: "Some Other File", FileName: "mod1-other.esp", IsPrimary: true, Category: "MAIN", Version: "2.0"},
	})

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out, "1 warning(s)")

	jsonOutput = true
	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 0, result.Issues)
	assert.Equal(t, 1, result.Warnings, "unverifiable version must be bucketed as a warning, not an issue")

	found := false
	for _, f := range result.Files {
		if f.ModID == "mod1" && f.Status == "version_unverifiable" {
			found = true
		}
	}
	assert.True(t, found, "expected a version_unverifiable entry in JSON files: %+v", result.Files)
}

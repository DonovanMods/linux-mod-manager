package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// --- doVerify --fix repairs version_mismatch rows (issue #94, Task A7) ---
//
// setupDoVerifyFixTest builds on setupDoVerifyVersionTest with a "1.5"
// recorded / "1.0" effective mismatch (same fixture the detection tests
// above use) and additionally: creates the "default" profile file and seeds
// a matching (stale) profile ref, so pm.UpsertMod's rename path is
// exercised for real instead of hitting ErrProfileNotFound; and turns on
// --fix. When deployed is true, the mod row is marked Deployed with a
// symlink LinkMethod and the file is actually deployed into game.ModPath
// via the real installer (not hand-rolled) so the pre-state - a symlink
// pointing INTO the old-version cache dir - matches what a real install
// would have produced before the rename.
func setupDoVerifyFixTest(t *testing.T, deployed bool) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()

	cmd, svc, game := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"},
	})

	if deployed {
		mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
		require.NoError(t, err)
		mod.Deployed = true
		mod.LinkMethod = domain.LinkSymlink
		require.NoError(t, svc.SaveInstalledMod(mod))

		require.NoError(t, svc.GetInstaller(game).Install(context.Background(), game, &mod.Mod, "default"))
	}

	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Version: "1.5", FileIDs: []string{"2"},
	}))

	verifyFix = true
	t.Cleanup(func() { verifyFix = false })

	return cmd, svc, game
}

// TestDoVerify_Fix_VersionMismatch_NotDeployed_RepairsRecord guards
// scenario (a): a mismatch row that isn't deployed gets fully repaired in
// one pass - cache re-keyed to the effective version, DB row corrected,
// profile ref corrected, JSON status flips to "ok", and issues drops back
// to 0.
func TestDoVerify_Fix_VersionMismatch_NotDeployed_RepairsRecord(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 0, result.Issues, "a successful repair must decrement issues back out")

	found := false
	for _, f := range result.Files {
		if f.ModID == "mod1" {
			found = true
			assert.Equal(t, "ok", f.Status, "repaired row's JSON status must flip to ok")
		}
	}
	assert.True(t, found, "expected a mod1 entry in JSON files: %+v", result.Files)

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.0"), "cache must exist under the effective (new) version key")
	assert.False(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.5"), "cache must no longer exist under the recorded (old) version key")

	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", mod.Version, "DB row must be corrected to the effective version")

	pm := getProfileManager(svc)
	profile, err := pm.Get(game.ID, "default")
	require.NoError(t, err)
	profileFound := false
	for _, ref := range profile.Mods {
		if ref.SourceID == "test-src" && ref.ModID == "mod1" {
			profileFound = true
			assert.Equal(t, "1.0", ref.Version, "profile YAML ref must be corrected to the effective version")
		}
	}
	assert.True(t, profileFound, "expected a profile ref for mod1: %+v", profile.Mods)
}

// TestDoVerify_Fix_VersionMismatch_NotDeployed_PrintsRepairedLine covers the
// text-mode output side of scenario (a) separately from the JSON-mode
// state assertions above, since a single doVerify call can only be
// observed in one output mode at a time (the fix is a real mutation, not a
// read-only check the test can safely run twice).
func TestDoVerify_Fix_VersionMismatch_NotDeployed_PrintsRepairedLine(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	assert.Contains(t, out, "Repaired")
	assert.Contains(t, out, "1.5")
	assert.Contains(t, out, "1.0")
	assert.Contains(t, out, "All files verified OK.", "the repaired mismatch must no longer be counted as an outstanding issue")
}

// TestDoVerify_Fix_VersionMismatch_Deployed_RelinksSymlink guards scenario
// (b): when the mismatched mod is Deployed with a symlink LinkMethod, the
// game-dir symlink points INTO the cache dir keyed by the recorded (wrong)
// version. After --fix renames that cache dir to the effective version, the
// old symlink target is gone - --fix must re-run the installer so the
// symlink is refreshed and resolves again instead of dangling.
func TestDoVerify_Fix_VersionMismatch_Deployed_RelinksSymlink(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, true)

	deployedPath := filepath.Join(game.ModPath, "mod1.esp")

	// Sanity-check the pre-state: before --fix runs, the symlink exists and
	// resolves (it was deployed from the "1.5"-keyed cache dir).
	content, err := os.ReadFile(deployedPath)
	require.NoError(t, err, "pre-state: symlink must resolve before the cache rename")
	require.Equal(t, "plugin content", string(content))

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 0, result.Issues)

	// The symlink must resolve post-fix - it was refreshed to point at the
	// renamed ("1.0"-keyed) cache dir rather than left dangling.
	info, err := os.Lstat(deployedPath)
	require.NoError(t, err, "deployed symlink must still exist")
	assert.True(t, info.Mode()&os.ModeSymlink != 0, "deployment must still be a symlink")

	postContent, err := os.ReadFile(deployedPath)
	require.NoError(t, err, "post-fix: symlink must resolve, not dangle")
	assert.Equal(t, "plugin content", string(postContent))

	target, err := os.Readlink(deployedPath)
	require.NoError(t, err)
	assert.Contains(t, target, filepath.Join("test-src-mod1", "1.0"), "symlink must point into the renamed (effective-version) cache dir")
}

// TestDoVerify_Fix_VersionMismatch_RenameBlocked_StillFixesDB guards
// scenario (c): if a cache directory already exists under the effective
// version (e.g. a partial/previous repair, or a stray manual copy), --fix
// must not clobber it via os.Rename - it leaves the cache alone, emits a
// note, but still corrects the DB row (the DB/cache disagreement this
// check exists to catch must not become a NEW, different disagreement).
func TestDoVerify_Fix_VersionMismatch_RenameBlocked_StillFixesDB(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	gameCache := svc.GetGameCache(game)
	// Pre-create the new-version cache dir so the rename is blocked.
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", "1.0", "mod1.esp", []byte("pre-existing 1.0 content")))

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	assert.Contains(t, out, "Note", "a note must be emitted when the rename is blocked")

	// Cache left as-is: both the old and the pre-existing new entries remain.
	assert.True(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.5"), "old cache entry must be left in place, not renamed away")
	assert.True(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.0"), "pre-existing new cache entry must be left untouched")

	// DB is still fixed despite the blocked rename.
	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", mod.Version, "DB row must still be corrected even when the cache rename is blocked")
}

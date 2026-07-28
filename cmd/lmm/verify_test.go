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

// TestDoVerify_Fix_VersionMismatch_RenameBlocked_JSONExposesNote guards the
// --json half of the rename-blocked case: the text-mode "Note: ..." line
// has no JSON equivalent unless the row itself carries it, so a --json
// caller flipping a row from version_mismatch to ok has no way to tell a
// clean repair from one where the cache rename was skipped. The repaired
// row's "note" field must carry that detail.
func TestDoVerify_Fix_VersionMismatch_RenameBlocked_JSONExposesNote(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", "1.0", "mod1.esp", []byte("pre-existing 1.0 content")))

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))

	// FileID is empty specifically on the version-record pre-pass's own
	// entry (the one this repair flips from version_mismatch to ok) - the
	// later per-file loop emits its own separate "ok" entry for mod1 with
	// FileID "2" that never carried a note, so filtering on FileID avoids
	// asserting Note against the wrong entry.
	found := false
	for _, f := range result.Files {
		if f.ModID == "mod1" && f.FileID == "" {
			found = true
			assert.Equal(t, "ok", f.Status)
			assert.NotEmpty(t, f.Note, "the blocked-rename note must be visible in JSON, not just text output")
		}
	}
	assert.True(t, found, "expected a mod1 version-check entry in JSON files: %+v", result.Files)
}

// TestDoVerify_Fix_VersionMismatch_RenameBlocked_Deployed_LeavesWorkingSymlinkAlone
// guards the other half of the rename-blocked case: when the mod is
// Deployed via symlink and the rename is blocked, the CURRENT symlink
// still points at the intact recorded-version cache dir - it was never
// touched by a rename. Re-linking in this case would repoint a working
// deployment into the pre-existing effective-version dir, which is
// unvetted (it's realistically a stray or partial copy, the very reason
// the rename was blocked in the first place). --fix must leave it alone.
func TestDoVerify_Fix_VersionMismatch_RenameBlocked_Deployed_LeavesWorkingSymlinkAlone(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, true)

	deployedPath := filepath.Join(game.ModPath, "mod1.esp")
	preTarget, err := os.Readlink(deployedPath)
	require.NoError(t, err)
	require.Contains(t, preTarget, filepath.Join("test-src-mod1", "1.5"), "pre-state: deployed symlink points at the recorded-version cache dir")

	gameCache := svc.GetGameCache(game)
	// Pre-create the new-version cache dir with different content than the
	// old dir, so a wrongful re-link would be observable via content, not
	// just path.
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", "1.0", "mod1.esp", []byte("unvetted 1.0 content")))

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out, "Note")

	postTarget, err := os.Readlink(deployedPath)
	require.NoError(t, err)
	assert.Equal(t, preTarget, postTarget, "the deployment must still point at the original (intact) recorded-version cache dir, not be re-linked into the unvetted pre-existing one")

	content, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	assert.Equal(t, "plugin content", string(content), "the working deployment's content must be untouched")

	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", mod.Version, "DB is still corrected even though the deployment was left alone")
	assert.True(t, mod.Deployed, "Deployed must remain true - the existing deployment is still valid")
}

// TestDoVerify_Fix_VersionMismatch_Deployed_RelinkFails_ClearsDeployedFlag
// guards Important-1 from code review: once the cache re-key (step 1) and
// DB save (step 2) succeed, mod.Version == effective - a LATER
// `verify --fix` run can no longer detect a version_mismatch to retry,
// even if the re-link (step 4) then fails and leaves the game-dir symlink
// dangling. Leaving Deployed stuck at true in that case would have the DB
// silently claim a working deployment that doesn't exist, with no way for
// verify to ever flag it again. --fix must clear Deployed instead, so the
// DB stays honest even though the specific version_mismatch signal that
// triggered the repair is gone for good.
func TestDoVerify_Fix_VersionMismatch_Deployed_RelinkFails_ClearsDeployedFlag(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixTest(t, true)

	// Force the re-link's installer.Install to fail: strip write
	// permission from the game dir (which already holds the pre-fix
	// symlink), so linker.Deploy's os.Remove(dst)/os.Symlink can't
	// replace the stale entry.
	require.NoError(t, os.Chmod(game.ModPath, 0o555))
	t.Cleanup(func() { _ = os.Chmod(game.ModPath, 0o755) }) // restore before TempDir's own cleanup removes it

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out, "Repair failed", "the re-link failure must be surfaced, not silently swallowed")

	// Steps 1-3 (cache rename, DB save, profile upsert) already completed
	// before the re-link failed, so the version correction stands.
	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", mod.Version, "the version correction from steps 1-3 must stand even though step 4 failed")

	// The state must not self-erase from detection: Deployed is cleared
	// rather than left claiming a deployment that's actually dangling.
	assert.False(t, mod.Deployed, "Deployed must be cleared when the re-link fails")

	// A second run confirms the row is inert (not re-reported, not
	// re-counted) rather than stuck retrying forever or erroring - the
	// version_mismatch signal is genuinely gone; Deployed=false is the
	// only remaining honest trace that the deployment still needs
	// attention (e.g. via a subsequent `lmm deploy`, which redeploys every
	// enabled mod regardless of its recorded Deployed value).
	out2 := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.NotContains(t, out2, "VERSION MISMATCH", "the version is already corrected - a second run must not re-report it")

	mod2, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod2.Deployed, "Deployed must still read false on the second run - nothing re-marks it deployed on its own")
}

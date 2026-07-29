package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// TestDoVerify_Fix_VersionMismatch_RenameFails_LeavesRecordUnchanged guards
// the "os.Rename itself fails" branch of repairModVersion (as opposed to
// scenario (c) above, where the rename is deliberately skipped because the
// destination already exists) - a genuine filesystem error mid-rename. This
// is the ordering invariant the whole repair sequence depends on: no DB
// write may happen unless the cache rename actually succeeded, or the DB
// and cache would disagree in a NEW way. Forces the failure by chmod-ing
// the cache mod-key's parent directory (which holds the "1.5" dir and
// would receive the renamed "1.0" one) read-only - os.Rename needs write
// permission on the containing directory to unlink/relink an entry, even
// though the directory is still readable/listable (execute bit intact).
func TestDoVerify_Fix_VersionMismatch_RenameFails_LeavesRecordUnchanged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	gameCache := svc.GetGameCache(game)
	oldPath := gameCache.ModPath(game.ID, "test-src", "mod1", "1.5")
	parentDir := filepath.Dir(oldPath)

	require.NoError(t, os.Chmod(parentDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(parentDir, 0o755) }) // restore before TempDir's own cleanup removes it

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 1, result.Issues, "a failed rename must not repair the row - the issue must still be counted")

	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.5", mod.Version, "DB row must still hold the OLD version - no write happens when the rename itself fails")

	assert.True(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.5"), "old cache dir must be untouched under the old key")
	assert.False(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.0"), "no new cache dir must have been created under the effective key")

	found := false
	for _, f := range result.Files {
		if f.ModID == "mod1" && f.FileID == "" {
			found = true
			assert.Equal(t, "version_mismatch", f.Status, "JSON row must stay version_mismatch when the rename fails")
		}
	}
	assert.True(t, found, "expected a mod1 version-check entry in JSON files: %+v", result.Files)
}

// --- doVerify --fix repairs sibling-profile rows sharing the same cache dir
// (final-fix-wave Finding 1) ---
//
// The mod cache is shared across profiles (cache.ModPath has no profile
// segment - internal/storage/cache/cache.go:29) but installed_mods rows are
// per-profile. When repairModVersion renames the shared cache dir for one
// profile's row, a sibling profile holding the same mod at the same wrong
// recorded version is left pointing at the now-missing dir until it, too,
// is verified. setupDoVerifyFixSiblingTest builds on setupDoVerifyFixTest's
// "default"-profile mismatch (recorded "1.5", effective "1.0") and adds:
//   - "second": the SAME wrong recorded version ("1.5") - the sibling this
//     fix must also correct.
//   - "third": a DIFFERENT recorded version ("2.0") - a different state
//     that must be left untouched (its own verify run's problem).
func setupDoVerifyFixSiblingTest(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()

	cmd, svc, game := setupDoVerifyFixTest(t, false)

	pm := getProfileManager(svc)

	_, err := pm.Create(game.ID, "second")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "second", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Version: "1.5", FileIDs: []string{"2"},
	}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:         domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.5", GameID: game.ID},
		ProfileName: "second",
		Enabled:     true,
		FileIDs:     []string{"2"},
	}))

	_, err = pm.Create(game.ID, "third")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "third", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Version: "2.0", FileIDs: []string{"2"},
	}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:         domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: game.ID},
		ProfileName: "third",
		Enabled:     true,
		FileIDs:     []string{"2"},
	}))

	return cmd, svc, game
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_NotDeployed_RepairsRecord
// is the required end-to-end case: fixing the mismatch via the "default"
// profile must also correct "second" (same stale recorded version) in both
// the DB and its profile YAML ref, while leaving "third" (a different
// recorded version) completely alone.
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_NotDeployed_RepairsRecord(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 0, result.Issues)

	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.0", secondMod.Version, "sibling DB row must be corrected to the effective version")

	pm := getProfileManager(svc)
	secondProfile, err := pm.Get(game.ID, "second")
	require.NoError(t, err)
	found := false
	for _, ref := range secondProfile.Mods {
		if ref.SourceID == "test-src" && ref.ModID == "mod1" {
			found = true
			assert.Equal(t, "1.0", ref.Version, "sibling profile YAML ref must be corrected")
		}
	}
	assert.True(t, found, "expected a mod1 ref in the second profile: %+v", secondProfile.Mods)

	thirdMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "third")
	require.NoError(t, err)
	assert.Equal(t, "2.0", thirdMod.Version, "sibling with a different recorded version must not be touched")

	thirdProfile, err := pm.Get(game.ID, "third")
	require.NoError(t, err)
	for _, ref := range thirdProfile.Mods {
		if ref.SourceID == "test-src" && ref.ModID == "mod1" {
			assert.Equal(t, "2.0", ref.Version, "sibling profile with a different recorded version must not be touched")
		}
	}

	noteFound := false
	for _, f := range result.Files {
		if f.ModID == "mod1" && f.FileID == "" {
			noteFound = true
			assert.Contains(t, f.Note, "second", "note must mention the repaired sibling profile")
			assert.NotContains(t, f.Note, "third", "note must not mention the untouched, differently-versioned sibling")
		}
	}
	assert.True(t, noteFound, "expected a mod1 version-check entry in JSON files: %+v", result.Files)
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_PrintsRepairedLine covers
// the text-mode side: a dedicated "Repaired (profile ...)" line per
// repaired sibling, distinct from the primary row's own "Repaired: ..."
// line.
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_PrintsRepairedLine(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	assert.Contains(t, out, "Repaired")
	assert.Contains(t, out, "second", "text output must mention the repaired sibling profile by name")
}

// TestDoVerify_Fix_VersionMismatch_RenameBlocked_SiblingsUntouched guards
// the "keep blocked-rename semantics unchanged" requirement: when the cache
// rename itself is blocked (destination already exists), no physical
// rename happened, so sibling rows must be left exactly as they were - they
// aren't orphaned by anything, so there's nothing to fix.
func TestDoVerify_Fix_VersionMismatch_RenameBlocked_SiblingsUntouched(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", "1.0", "mod1.esp", []byte("pre-existing 1.0 content")))

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out, "Note")

	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.5", secondMod.Version, "sibling must be untouched when the cache rename itself was blocked")
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_Relinks guards
// the deployed-sibling variant: a sibling row marked Deployed via symlink
// has its links re-created through the installer exactly like the primary
// row's would be, once the shared cache dir has actually moved.
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_Relinks(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	secondMod.Deployed = true
	secondMod.LinkMethod = domain.LinkSymlink
	require.NoError(t, svc.SaveInstalledMod(secondMod))

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 0, result.Issues)

	deployedPath := filepath.Join(game.ModPath, "mod1.esp")
	info, err := os.Lstat(deployedPath)
	require.NoError(t, err, "sibling's deployed symlink must have been (re-)created")
	assert.True(t, info.Mode()&os.ModeSymlink != 0, "sibling deployment must be a symlink")

	content, err := os.ReadFile(deployedPath)
	require.NoError(t, err, "sibling symlink must resolve, not dangle")
	assert.Equal(t, "plugin content", string(content))

	afterMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.True(t, afterMod.Deployed, "Deployed remains true after a successful sibling re-link")
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_RelinkFails_ClearsDeployedFlag
// mirrors TestDoVerify_Fix_VersionMismatch_Deployed_RelinkFails_ClearsDeployedFlag
// for a sibling row: when the sibling's own re-link fails, its Deployed
// flag must be cleared - same honesty guarantee as the primary path - while
// the version correction (DB + profile) from the earlier steps still
// stands, for both the sibling AND the primary.
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_RelinkFails_ClearsDeployedFlag(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	secondMod.Deployed = true
	secondMod.LinkMethod = domain.LinkSymlink
	require.NoError(t, svc.SaveInstalledMod(secondMod))

	require.NoError(t, os.Chmod(game.ModPath, 0o555))
	t.Cleanup(func() { _ = os.Chmod(game.ModPath, 0o755) }) // restore before TempDir's own cleanup removes it

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	afterMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.0", afterMod.Version, "the sibling's version correction must stand even though its re-link failed")
	assert.False(t, afterMod.Deployed, "sibling Deployed must be cleared when its re-link fails")

	primaryMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", primaryMod.Version, "the primary row must still be repaired despite the sibling's re-link failure")
}

// --- re-review gaps: silent per-sibling failures + JSON note loss on
// primary relink failure (final-fix-wave, second round) ---

// setupDoVerifyFixSiblingUpsertFailureTest builds on
// setupDoVerifyFixSiblingTest and chmods the "second" profile's own YAML
// file (not the shared profiles directory, which would also block the
// PRIMARY profile's own already-completed upsert) read-only, so
// config.SaveProfile's os.WriteFile - which needs write permission on the
// file itself to truncate and rewrite it - fails specifically for
// "second"'s sibling repair.
func setupDoVerifyFixSiblingUpsertFailureTest(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()

	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	secondProfilePath := filepath.Join(configDir, "games", game.ID, "profiles", "second.yaml")
	require.NoError(t, os.Chmod(secondProfilePath, 0o444))
	t.Cleanup(func() { _ = os.Chmod(secondProfilePath, 0o644) }) // restore before TempDir's own cleanup removes it

	return cmd, svc, game
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_UpsertModFails_WarnsInTextMode
// guards against DEV.md's "never swallow errors without logging/context":
// repairSiblingProfiles must not silently `continue` past a failed
// pm.UpsertMod for one sibling - it must print a warning identifying which
// profile failed, while still processing any remaining siblings
// (best-effort loop, not aborted - "third", a different recorded version
// and never a repair candidate in the first place, is untouched either way).
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_UpsertModFails_WarnsInTextMode(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixSiblingUpsertFailureTest(t)

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out, "Warning", "an UpsertMod failure for a sibling must be surfaced, not swallowed")
	assert.Contains(t, out, "second", "the warning must identify which sibling profile failed")

	thirdMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "third")
	require.NoError(t, err)
	assert.Equal(t, "2.0", thirdMod.Version, "a sibling that was never a candidate must not be touched by another sibling's failure")
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_UpsertModFails_JSONNotesFailure
// covers the --json half of the same failure: the primary row's "note"
// field must fold in the failed sibling, not just the successful ones -
// separate fresh state from the text-mode test above since fixing the
// primary row (which succeeds regardless of the sibling's own failure) is
// a real mutation that can't be observed twice from one doVerify call.
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_UpsertModFails_JSONNotesFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixSiblingUpsertFailureTest(t)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	found := false
	for _, f := range result.Files {
		if f.ModID == "mod1" && f.FileID == "" {
			found = true
			assert.Contains(t, f.Note, "second", "JSON note must mention the failed sibling profile")
			assert.Contains(t, f.Note, "FAILED", "JSON note must clearly flag the failure, not read as a success")
		}
	}
	assert.True(t, found, "expected a mod1 version-check entry in JSON files: %+v", result.Files)
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_ListFails_WarnsOnce guards
// the pm.List failure path: it must surface once (a warning line plus a
// note), not be silently swallowed. Forces the failure by stripping read
// permission from the shared profiles directory (0o311: search/write, no
// read) so os.ReadDir (which pm.List/config.ListProfiles depends on) fails,
// while by-path opens of already-known files (LoadProfile/SaveProfile of
// the PRIMARY "default" profile, which only need search permission on
// ancestor directories) keep working - the primary repair itself must
// still succeed.
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_ListFails_WarnsOnce(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	profilesDir := filepath.Join(configDir, "games", game.ID, "profiles")
	require.NoError(t, os.Chmod(profilesDir, 0o311))
	t.Cleanup(func() { _ = os.Chmod(profilesDir, 0o755) }) // restore before TempDir's own cleanup removes it

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Equal(t, 1, strings.Count(out, "Warning"), "the pm.List failure must be surfaced exactly once, not once per (nonexistent) sibling")

	// The primary repair (by-path, unaffected by the missing read bit on
	// the shared directory) must still have gone through.
	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", mod.Version, "the primary row must still be repaired even though sibling discovery failed")
}

// TestDoVerify_Fix_VersionMismatch_PrimaryRelinkFails_SiblingRepaired_JSONNoteVisible
// guards the second re-review gap: repairModVersion can return a non-empty
// note (successful sibling repair) alongside a non-nil err (the PRIMARY
// row's own re-link failed) - doVerify's caller must still attach that
// note to the JSON row even though repairErr != nil, or a --json consumer
// has zero visibility into the sibling repair that actually happened. The
// row's status must stay "version_mismatch" (nothing about the primary
// row itself was fixed) and issues must NOT be decremented.
func TestDoVerify_Fix_VersionMismatch_PrimaryRelinkFails_SiblingRepaired_JSONNoteVisible(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	primaryMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	primaryMod.Deployed = true
	primaryMod.LinkMethod = domain.LinkSymlink
	require.NoError(t, svc.SaveInstalledMod(primaryMod))
	require.NoError(t, svc.GetInstaller(game).Install(context.Background(), game, &primaryMod.Mod, "default"))

	// Force the PRIMARY's own re-link to fail (same read-only-game-dir
	// trick as TestDoVerify_Fix_VersionMismatch_Deployed_RelinkFails_ClearsDeployedFlag).
	require.NoError(t, os.Chmod(game.ModPath, 0o555))
	t.Cleanup(func() { _ = os.Chmod(game.ModPath, 0o755) }) // restore before TempDir's own cleanup removes it

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 1, result.Issues, "the primary row's own repair failed, so the issue must still be counted")

	found := false
	for _, f := range result.Files {
		if f.ModID == "mod1" && f.FileID == "" {
			found = true
			assert.Equal(t, "version_mismatch", f.Status, "status must stay version_mismatch - the PRIMARY row itself was not fixed")
			assert.Contains(t, f.Note, "second", "the successful sibling repair must still be visible in the note despite the primary relink failure")
		}
	}
	assert.True(t, found, "expected a mod1 version-check entry in JSON files: %+v", result.Files)

	// The sibling itself was, in fact, repaired.
	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.0", secondMod.Version, "the sibling repair must have gone through independently of the primary's relink failure")
}

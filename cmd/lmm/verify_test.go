package main

import (
	"context"
	"encoding/json"
	"errors"
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
// upstream truth the check compares FileIDs against). Also returns the
// fakeInstallSource itself so callers that need to inspect what it actually
// received (e.g. receivedGameFileIDs, for the per-source GameID-mapping
// check) can do so - most callers ignore it.
func setupDoVerifyVersionTest(t *testing.T, recordedVersion string, fileIDs []string, sourceFiles []domain.DownloadableFile) (*cobra.Command, *core.Service, *domain.Game, *fakeInstallSource) {
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

	return cmd, svc, game, src
}

// TestDoVerify_VersionMismatch_ReportedAsIssue guards issue #94's detection
// half: an installed row recorded as version "1.5" whose stored file ID
// "2" is, per the source, actually version "1.0" - the version of the bytes
// really on disk. doVerify must flag this as "version_mismatch" (an issue,
// not a warning) and show both the recorded and source-reported values, in
// both text and --json output. Task A7 adds the --fix repair; this task is
// detection only.
func TestDoVerify_VersionMismatch_ReportedAsIssue(t *testing.T) {
	cmd, svc, game, _ := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
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
	cmd, svc, game, _ := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
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

// TestDoVerify_VersionCheck_SourceUnreachable_JSONNotesReason guards PR
// #128 Copilot round-6's suppressed finding: when svc.GetModFiles fails in
// the version-record pre-pass (the "source unreachable" case), --json
// emitted a "skipped" row with the error dropped entirely, while text mode
// at least printed "could not check version (source unreachable)" - a
// --json caller had no way to see WHY. Forces the failure via
// fakeInstallSource.getModFilesErr (a real error the fake source itself
// returns, not a permission trick, since this is a source-layer failure).
func TestDoVerify_VersionCheck_SourceUnreachable_JSONNotesReason(t *testing.T) {
	cmd, svc, game, src := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"},
	})
	src.getModFilesErr = errors.New("connection refused")

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(outJSON), &result))
	assert.Equal(t, 0, result.Issues, "source-unreachable is a warning, not an issue")
	assert.Equal(t, 1, result.Warnings, "the warnings count must be unaffected by adding the note")

	found := false
	for _, f := range result.Files {
		if f.ModID == "mod1" && f.FileID == "" {
			found = true
			assert.Equal(t, "skipped", f.Status, "status must stay skipped")
			assert.Contains(t, f.Note, "connection refused", "the source-unreachable reason must reach the JSON note, not just the text-mode line")
		}
	}
	assert.True(t, found, "expected a mod1 version-check entry in JSON files: %+v", result.Files)
}

// TestDoVerify_VersionCheck_MapsGameIDPerSourceMapping guards PR #128
// Copilot round-3's suppressed finding: the version-record pre-pass calls
// svc.GetModFiles(ctx, mod.SourceID, &mod.Mod) where mod.GameID is the LMM
// game ID (installed rows persist normalized IDs - see
// setupDoVerifyVersionTest, which stamps GameID: game.ID). But
// Service.GetModFiles (internal/core/service.go) forwards straight to the
// source with NO game-ID translation, unlike Service.GetMod, which maps
// through game.SourceIDs[sourceID] first. Sources like NexusMods address
// games by their own domain (e.g. "skyrimspecialedition"), so whenever a
// game's mapping differs from its LMM ID, the version check would silently
// call the source with the wrong ID.
//
// setupDoInstallTest's fixture happens to map game.SourceIDs["test-src"]
// to "g1" - the SAME as game.ID - which is exactly why none of the
// existing detection tests caught this: the bug is invisible when the
// mapped and unmapped IDs happen to coincide. This test deliberately
// overrides the mapping to a different value so the wrong ID becomes
// observable, using the same gameIDCapturingSource-style pattern as
// internal/core/updater_test.go's TestCheckUpdatesTranslatesGameIDPerSourceMapping
// (fakeInstallSource.receivedGameFileIDs here, since the two test doubles
// live in different packages).
func TestDoVerify_VersionCheck_MapsGameIDPerSourceMapping(t *testing.T) {
	cmd, svc, game, src := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"},
	})
	game.SourceIDs["test-src"] = "mapped-domain"

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	require.Len(t, src.receivedGameFileIDs, 1, "GetModFiles must have been called exactly once for mod1's version check")
	assert.Equal(t, "mapped-domain", src.receivedGameFileIDs[0], "the version-record check must translate GameID through game.SourceIDs, same rule as Service.GetMod")
}

// TestDoVerify_VersionCheck_EmptySourceMapping_KeepsLMMGameID covers the
// other half of the mapping rule: an empty (but present) mapping value
// means "this source applies to any game" (e.g. directory sources:
// `donovan-mods: ""`) and must NOT blank out the LMM game ID - it must be
// passed through unchanged, exactly like Service.GetMod's `ok && id != ""`
// guard.
func TestDoVerify_VersionCheck_EmptySourceMapping_KeepsLMMGameID(t *testing.T) {
	cmd, svc, game, src := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"},
	})
	game.SourceIDs["test-src"] = ""

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	require.Len(t, src.receivedGameFileIDs, 1)
	assert.Equal(t, game.ID, src.receivedGameFileIDs[0], "an empty per-source mapping must keep the LMM game ID, not blank it out")
}

// TestDoVerify_FileCountPrePass_ListFilesFails_SurfacedAsWarning guards
// audit Finding 5 (Minor): the file-count-mismatch pre-pass silently
// `continue`d past a gameCache.ListFiles error, indistinguishable from
// "cache exists and has content" - suppressing what should have been its
// own report instead of just skipping the FILE COUNT MISMATCH check.
// Forces a real filesystem error (not "file count is zero") by stripping
// all permissions from the cached mod-version directory, so
// filepath.WalkDir (which ListFiles uses) can't read it - while
// gameCache.Exists (a plain os.Stat, needing only traversal permission on
// ancestors) still reports the dir as present, reaching the ListFiles call
// at all. Uses a matching recorded/source version so there's no VERSION
// MISMATCH noise - isolating the file-count pre-pass as the only thing
// under test.
func TestDoVerify_FileCountPrePass_ListFilesFails_SurfacedAsWarning(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game, _ := setupDoVerifyVersionTest(t, "1.0", []string{"2"}, []domain.DownloadableFile{
		{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"},
	})

	gameCache := svc.GetGameCache(game)
	modDir := gameCache.ModPath(game.ID, "test-src", "mod1", "1.0")
	require.NoError(t, os.Chmod(modDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(modDir, 0o755) }) // restore before TempDir's own cleanup removes it

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out, "could not check cached file count", "a real ListFiles error must be surfaced, not silently swallowed")
	assert.Contains(t, out, "1 warning(s)", "the surfaced error must actually count as a warning, not just print")
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

	cmd, svc, game, _ := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"},
	})

	if deployed {
		// Targeted setters, not a full svc.SaveInstalledMod(mod) - the
		// latter's full-row upsert would wipe the checksum
		// setupDoVerifyVersionTest just seeded (the exact audit Finding 1
		// bug pattern, here as a test-setup artifact rather than
		// production code - fixed the same way).
		require.NoError(t, svc.SetModDeployed("test-src", "mod1", game.ID, "default", true))
		require.NoError(t, svc.SetModLinkMethod("test-src", "mod1", game.ID, "default", domain.LinkSymlink))

		mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
		require.NoError(t, err)
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

	// Audit Finding 8: a second run must be genuinely CLEAN, not merely
	// free of a re-reported VERSION MISMATCH. Before Finding 1's fix, the
	// version-record repair's SaveInstalledMod calls silently wiped the
	// mod's stored checksum, so THIS EXACT second run used to hit NO
	// CHECKSUM and then a failing redownload attempt (this fixture never
	// registers download content) - a real regression the original,
	// narrower "NotContains VERSION MISMATCH" assertion didn't catch. This
	// is the net that would have caught Finding 1.
	assert.Contains(t, out2, "All files verified OK.", "the second run must be clean - not hit NO CHECKSUM or any other new issue")

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
			// Audit Finding 7: the reason the repair itself failed must
			// reach --json, not just the text-mode "Repair failed: %v"
			// line - a --json caller had zero visibility into why
			// before this.
			assert.NotEmpty(t, f.Note, "the repair failure reason must be visible in the JSON note, not text-only")
		}
	}
	assert.True(t, found, "expected a mod1 version-check entry in JSON files: %+v", result.Files)
}

// --- full-file audit (epic98), findings F1-F8 ---

// TestDoVerify_Fix_VersionMismatch_PreservesFileChecksum guards audit
// Finding 1 (Critical): every repair-path save used to be a full
// svc.SaveInstalledMod, whose DB-layer upsert always replaces
// installed_mod_files (DELETE + a checksum-less re-INSERT) even when
// FileIDs is completely unchanged - silently wiping every stored checksum
// for the mod's files on every successful version-record repair. The next
// `verify` would then report NO CHECKSUM for the just-repaired mod, and
// the next --fix would mass-redownload via redownloadModFile instead of
// leaving the already-correct cached bytes alone.
func TestDoVerify_Fix_VersionMismatch_PreservesFileChecksum(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	// Sanity: the fixture seeds a real checksum before the repair runs.
	before, err := svc.GetFilesWithChecksums(game.ID, "default")
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(t, "deadbeef", before[0].Checksum)

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	require.Contains(t, out, "Repaired", "sanity: the repair must have actually run")

	after, err := svc.GetFilesWithChecksums(game.ID, "default")
	require.NoError(t, err)
	found := false
	for _, f := range after {
		if f.ModID == "mod1" && f.FileID == "2" {
			found = true
			assert.Equal(t, "deadbeef", f.Checksum, "a successful version-record repair must NOT wipe the stored checksum")
		}
	}
	assert.True(t, found, "expected mod1's file '2' to still be tracked after the repair: %+v", after)

	// Audit Finding 8: a second run must be genuinely CLEAN - this is the
	// repair-success half of the "net that would have caught Finding 1"
	// (the RelinkFails test above covers the failure half). Before
	// Finding 1's fix, the checksum wipe this test guards against would
	// have made this exact second run hit NO CHECKSUM and a failing
	// redownload attempt, not a clean pass.
	out2 := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out2, "All files verified OK.", "the second run must be clean, with no new issues or warnings introduced by the repair")
}

// TestDoVerify_Fix_VersionMismatch_RetryAfterPartialFailure_StillRepairsSiblings
// guards audit Finding 2 (Important): the sibling pass was gated purely on
// `renamed` (a rename performed THIS run), so a retry after an earlier
// run's partial failure - one where the cache rename already succeeded but
// a later step (DB save, profile upsert) then failed - would see oldPath
// already gone and skip siblings entirely, even though the shared cache
// dir has, in fact, already moved out from under them. Simulates exactly
// that retry state: the cache already lives under the effective version
// (as if a prior run's rename succeeded), while BOTH the primary and
// sibling DB rows still record the old version (as if that prior run then
// failed before completing).
func TestDoVerify_Fix_VersionMismatch_RetryAfterPartialFailure_StillRepairsSiblings(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	gameCache := svc.GetGameCache(game)
	oldPath := gameCache.ModPath(game.ID, "test-src", "mod1", "1.5")
	newPath := gameCache.ModPath(game.ID, "test-src", "mod1", "1.0")
	require.NoError(t, os.Rename(oldPath, newPath), "simulating a prior run's already-completed cache rename")

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.0", secondMod.Version, "the sibling must still be repaired on retry, even though no rename happened THIS run - the cache already lives at the effective version")
}

// TestDoVerify_Fix_VersionMismatch_UpsertModFailsBeforeDBWrite_ConvergesOnRetry
// guards audit Finding 3 (Important): the ORIGINAL step order was cache
// rename -> DB save -> profile upsert. If the DB save succeeded but the
// profile upsert then failed, the DB already showed the effective version -
// so the NEXT verify run would see no mismatch at all (recorded == source)
// and would never re-detect or retry the now-permanently-stale profile
// YAML. Swapping the order (profile upsert BEFORE the DB write) makes the
// DB write the last, defining "done" signal: if upsert fails, the DB is
// untouched, so a retry still sees version_mismatch and can converge.
// Forces the upsert to fail by chmod-ing "default.yaml" read-only, then
// runs --fix TWICE under the same failure condition and asserts the
// SECOND run still reports version_mismatch (not silently "fixed").
func TestDoVerify_Fix_VersionMismatch_UpsertModFailsBeforeDBWrite_ConvergesOnRetry(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	defaultProfilePath := filepath.Join(configDir, "games", game.ID, "profiles", "default.yaml")
	require.NoError(t, os.Chmod(defaultProfilePath, 0o444))
	t.Cleanup(func() { _ = os.Chmod(defaultProfilePath, 0o644) }) // restore before TempDir's own cleanup removes it

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out1 := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out1, "Repair failed", "run 1 must fail visibly, not silently")

	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.5", mod.Version, "the DB must NOT have been written when the profile upsert (which now runs first) fails")

	out2 := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out2, "VERSION MISMATCH", "run 2 must still detect the mismatch - the DB was never corrupted into a false 'already fixed' state")
}

// TestDoVerify_Fix_VersionMismatch_OldPathStatErrorBlocksRepair guards
// audit Finding 4 (Minor): the original code treated ANY os.Stat(oldPath)
// error identically to "the old cache dir doesn't exist" (a normal,
// nothing-to-rename state), including a genuine stat failure (permission
// denied, I/O error) that isn't really telling us the dir is absent at
// all. Forces a real stat error - not fs.ErrNotExist - by stripping ALL
// permissions (0o000, no execute either) from the cache mod-key's parent
// directory, so os.Stat can't even traverse into it to check.
func TestDoVerify_Fix_VersionMismatch_OldPathStatErrorBlocksRepair(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	gameCache := svc.GetGameCache(game)
	oldPath := gameCache.ModPath(game.ID, "test-src", "mod1", "1.5")
	parentDir := filepath.Dir(oldPath)

	require.NoError(t, os.Chmod(parentDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(parentDir, 0o755) }) // restore before TempDir's own cleanup removes it

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.5", mod.Version, "a genuine stat failure on the old cache path must block the repair entirely - no DB write, same as an os.Rename failure")
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
	// Seed a checksum for "second" too, mirroring "default"'s fixture
	// (setupDoVerifyVersionTest) - realistic (a real sibling profile has
	// its own recorded checksum, not a NULL one), and load-bearing for
	// tests asserting the sibling repair path doesn't wipe it (audit
	// Finding 1's class, Copilot round 8).
	require.NoError(t, svc.SaveFileChecksum("test-src", "mod1", game.ID, "second", "2", "deadbeef"))

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
	require.NoError(t, svc.SaveFileChecksum("test-src", "mod1", game.ID, "third", "2", "deadbeef"))

	return cmd, svc, game
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_NotInstalled_SkippedSilently
// pins Copilot round-2's (suppressed, no inline thread) low-confidence
// finding: repairSiblingProfiles must distinguish "this profile never had
// the mod at all" (svc.GetInstalledMod returning domain.ErrModNotFound)
// from a genuine lookup failure - the former is not a candidate and must
// stay a silent skip (no warning, not in the failed list), unlike any
// OTHER GetInstalledMod error, which the production code now surfaces the
// same way as a SaveInstalledMod/UpsertMod/re-link failure. Adds a
// "fourth" profile that exists (so pm.List finds it) but was never given
// an InstalledMod row for mod1 at all - distinct from "third" (which DOES
// have a row, just at a different recorded version, exercising the
// `sibling.Version != recorded` branch instead).
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_NotInstalled_SkippedSilently(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "fourth")
	require.NoError(t, err)
	// Deliberately no pm.AddMod / svc.SaveInstalledMod for mod1 in "fourth" -
	// it's a profile that exists but never had this mod installed.

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	assert.NotContains(t, out, "fourth", "a profile that never had the mod must be a silent skip - no warning, no mention at all")
	assert.NotContains(t, out, "Warning", "the not-installed case must not be reported as a failure")

	_, err = svc.GetInstalledMod("test-src", "mod1", game.ID, "fourth")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "sanity: 'fourth' genuinely has no row for mod1 - this is the ErrModNotFound path, not a different bug")
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

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_DifferentFileIDs_NotAutoRepaired
// guards audit Finding 6 (Minor): repairSiblingProfiles matched a sibling
// purely on Version equality, ignoring FileIDs entirely. Two profiles can
// legitimately record the SAME wrong version for DIFFERENT files (e.g. one
// profile picked a different optional file at install time) - blindly
// stamping the sibling with the PRIMARY's effective version would be
// wrong for that sibling's own files, causing churn (an unnecessary
// rename plus a full redownload the next time that profile is verified).
// A sibling whose FileIDs differ from the primary's must be left alone
// (not auto-repaired) with a warning pointing at the right fix instead.
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_DifferentFileIDs_NotAutoRepaired(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "differs")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "differs", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Version: "1.5", FileIDs: []string{"3"},
	}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:         domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.5", GameID: game.ID},
		ProfileName: "differs",
		Enabled:     true,
		FileIDs:     []string{"3"}, // same recorded version as "second", but a DIFFERENT file selection
	}))

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})
	assert.Contains(t, out, "differs", "a warning must identify the profile whose file selection differs")
	assert.Contains(t, out, "file selection", "the warning must explain why this sibling wasn't auto-repaired")

	differsMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "differs")
	require.NoError(t, err)
	assert.Equal(t, "1.5", differsMod.Version, "a sibling with different FileIDs must NOT be auto-repaired, even though its recorded version matches")

	// "second" (same version, SAME FileIDs as primary) must still be
	// auto-repaired normally - this finding only withholds repair from
	// siblings whose file selection actually differs.
	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.0", secondMod.Version, "a sibling with matching FileIDs must still be auto-repaired")
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_Relinks guards
// the deployed-sibling variant: a sibling row marked Deployed via symlink
// has its links re-created through the installer exactly like the primary
// row's would be, once the shared cache dir has actually moved.
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_Relinks(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	// Targeted setters, not a mutate-then-svc.SaveInstalledMod - the
	// latter's full-row upsert would wipe the checksum
	// setupDoVerifyVersionTest seeded (audit Finding 1's exact pattern,
	// here as a test-setup artifact - Copilot round 8, PR #128).
	require.NoError(t, svc.SetModDeployed("test-src", "mod1", game.ID, "second", true))
	require.NoError(t, svc.SetModLinkMethod("test-src", "mod1", game.ID, "second", domain.LinkSymlink))

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

	// This doVerify run only checks the "default" profile - checking
	// result.Files' status wouldn't observe whether "second"'s own
	// checksum survived the targeted setters above, since verify never
	// looks at a non-active profile's checksums. Check directly instead.
	secondFiles, err := svc.GetFilesWithChecksums(game.ID, "second")
	require.NoError(t, err)
	for _, f := range secondFiles {
		if f.ModID == "mod1" && f.FileID == "2" {
			assert.Equal(t, "deadbeef", f.Checksum, "the targeted setters (and the sibling repair itself) must not wipe the seeded checksum")
		}
	}

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

	// Targeted setters, not a mutate-then-svc.SaveInstalledMod - see
	// TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_Relinks.
	require.NoError(t, svc.SetModDeployed("test-src", "mod1", game.ID, "second", true))
	require.NoError(t, svc.SetModLinkMethod("test-src", "mod1", game.ID, "second", domain.LinkSymlink))

	require.NoError(t, os.Chmod(game.ModPath, 0o555))
	t.Cleanup(func() { _ = os.Chmod(game.ModPath, 0o755) }) // restore before TempDir's own cleanup removes it

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	// This run only checks the "default" profile, so "NO CHECKSUM" in its
	// own output wouldn't reflect whether "second"'s checksum survived the
	// targeted setters above - check directly instead.
	secondFiles, err := svc.GetFilesWithChecksums(game.ID, "second")
	require.NoError(t, err)
	for _, f := range secondFiles {
		if f.ModID == "mod1" && f.FileID == "2" {
			assert.Equal(t, "deadbeef", f.Checksum, "the targeted setters must not wipe the seeded checksum")
		}
	}

	afterMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.0", afterMod.Version, "the sibling's version correction must stand even though its re-link failed")
	assert.False(t, afterMod.Deployed, "sibling Deployed must be cleared when its re-link fails")

	primaryMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", primaryMod.Version, "the primary row must still be repaired despite the sibling's re-link failure")

	// PR #128 Copilot review, Claim 2: a sibling re-link failure must be
	// surfaced like any other per-sibling failure (SaveInstalledMod/
	// UpsertMod already are, per the previous round) - not silently
	// absorbed into a false "Repaired" success line while the deployment
	// is actually left broken.
	assert.Contains(t, out, "Warning", "a sibling re-link failure must print a warning, not be silently absorbed")
	assert.Contains(t, out, "second", "the warning must identify which sibling profile's re-link failed")
	assert.NotContains(t, out, "Repaired (profile second)", "a sibling whose re-link failed must not be reported as a clean success")
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_RelinkFails_JSONNotesFailure
// covers the --json half of PR #128 Copilot review Claim 2: the primary
// row's "note" field must flag the sibling's re-link failure the same way
// it already flags a SaveInstalledMod/UpsertMod failure - fresh state
// from the text-mode test above since fixing the primary row is a real
// mutation that can't be observed twice from one doVerify call.
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_RelinkFails_JSONNotesFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixSiblingTest(t)

	// Targeted setters, not a mutate-then-svc.SaveInstalledMod - see
	// TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_Relinks.
	require.NoError(t, svc.SetModDeployed("test-src", "mod1", game.ID, "second", true))
	require.NoError(t, svc.SetModLinkMethod("test-src", "mod1", game.ID, "second", domain.LinkSymlink))

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

	found := false
	for _, f := range result.Files {
		if f.ModID == "mod1" && f.FileID == "" {
			found = true
			assert.Contains(t, f.Note, "second", "JSON note must mention the sibling whose re-link failed")
			assert.Contains(t, f.Note, "FAILED", "JSON note must clearly flag it as a failure, not a success")
		}
	}
	assert.True(t, found, "expected a mod1 version-check entry in JSON files: %+v", result.Files)

	// This run only checks the "default" profile, so result.Files wouldn't
	// reflect whether "second"'s checksum survived the targeted setters
	// above - check directly instead.
	secondFiles, err := svc.GetFilesWithChecksums(game.ID, "second")
	require.NoError(t, err)
	for _, f := range secondFiles {
		if f.ModID == "mod1" && f.FileID == "2" {
			assert.Equal(t, "deadbeef", f.Checksum, "the targeted setters must not wipe the seeded checksum")
		}
	}
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

	// PR #128 Copilot review round 4: a surfaced sibling failure must also
	// move the needle on the actual warnings COUNT, not just print a line -
	// the primary row's own repair succeeded (issues drops to 0), but the
	// summary must not claim a clean "All files verified OK." while a
	// sibling repair genuinely failed.
	assert.Contains(t, out, "1 warning(s)", "a failed sibling repair must be counted as a warning in the summary")
	assert.NotContains(t, out, "All files verified OK.", "the summary must not claim a clean run while a sibling repair failed")

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

	// PR #128 Copilot review round 4: the JSON "warnings" field must
	// reflect the failed sibling repair - not just the note text - so a
	// --json caller can detect the problem without string-matching "FAILED"
	// inside a human-readable note.
	assert.Equal(t, 1, result.Warnings, "the failed sibling repair must be counted in the JSON warnings field")
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

	// PR #128 Copilot review round 4: a pm.List failure counts as exactly
	// one warning in the actual counter, not just in the printed text.
	assert.Contains(t, out, "1 warning(s)", "the pm.List failure must count as exactly one warning")

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

	// Targeted setters, not a mutate-then-svc.SaveInstalledMod - see
	// TestDoVerify_Fix_VersionMismatch_SiblingProfile_Deployed_Relinks (this
	// straggler wasn't in Copilot round 8's three cited sites, but matches
	// the same pattern - caught by grepping the whole file per the
	// coordinator's instruction).
	require.NoError(t, svc.SetModDeployed("test-src", "mod1", game.ID, "default", true))
	require.NoError(t, svc.SetModLinkMethod("test-src", "mod1", game.ID, "default", domain.LinkSymlink))

	primaryMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
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
		assert.NotEqual(t, "no_checksum", f.Status, "the targeted setters must not have wiped the seeded checksum: %+v", f)
	}
	assert.True(t, found, "expected a mod1 version-check entry in JSON files: %+v", result.Files)

	// The sibling itself was, in fact, repaired.
	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.0", secondMod.Version, "the sibling repair must have gone through independently of the primary's relink failure")
}

// --- PR #128 Copilot review round 1: verifying (not blindly fixing) two
// claims against the actual code ---

// TestDoVerify_VersionUnverifiable_WithModFilter_ChecksumRowsAlwaysExist is
// evidence for refuting Copilot's Claim 1 (comment 3670171464): that
// `checked` is only incremented on the pre-pass's OK path, so a filtered
// run producing only VERSION MISMATCH/UNVERIFIABLE/source-unreachable
// output with "no checksum rows" could still wrongly print "No files
// found for mod ...".
//
// That premise doesn't hold: mod.FileIDs (what gates entry into the
// pre-pass's version-check branches at all - see the `len(mod.FileIDs) ==
// 0` skip in doVerify) and the `files` slice the main per-file loop
// iterates (from svc.GetFilesWithChecksums) are BOTH sourced from the
// exact same `installed_mod_files` table (see
// internal/storage/db/mods.go: replaceModFileIDsTx populates it,
// getModFileIDsBatch/GetModFileIDs and GetFilesWithChecksums both read it
// with no additional filtering beyond game/profile). So whenever
// mod.FileIDs is non-empty for a modFilter'd mod - the only way the
// pre-pass can reach VERSION MISMATCH/UNVERIFIABLE/unreachable at all -
// `files` filtered to that same mod ID is provably non-empty too, and the
// main loop's unconditional `checked++` (before any per-file status
// logic) fires for it. The "no checksum rows" state Claim 1 hypothesizes
// cannot coexist with a non-empty FileIDs set under the current schema.
//
// This test constructs exactly the scenario Claim 1 describes (a
// modFilter'd mod, VERSION UNVERIFIABLE - no matching file ID upstream -
// and nothing else installed) and asserts the misleading message does
// NOT appear.
func TestDoVerify_VersionUnverifiable_WithModFilter_ChecksumRowsAlwaysExist(t *testing.T) {
	cmd, svc, game, _ := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "3", Name: "Some Other File", FileName: "mod1-other.esp", IsPrimary: true, Category: "MAIN", Version: "2.0"},
	})

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, []string{"mod1"})
	})

	assert.Contains(t, out, "VERSION UNVERIFIABLE", "sanity: the scenario Claim 1 hypothesizes must actually be reached")
	assert.NotContains(t, out, "No files found for mod", "checksum rows for mod1 exist (same installed_mod_files table backs both FileIDs and the checksum listing) - the main loop's checked++ must have fired")
}

// setupDoVerifyRedownloadTest builds a minimal fixture for the MISSING/NO
// CHECKSUM --fix repair paths: a real fakeInstallSource-backed service and
// an installed mod1/file "2", WITHOUT registering any download content for
// "2" via src.AddDownload - so any redownloadModFile attempt genuinely
// fails (the fake source's HTTP handler 404s), letting these tests force a
// real repair failure without permission tricks.
func setupDoVerifyRedownloadTest(t *testing.T) (*cobra.Command, *core.Service, *domain.Game, *fakeInstallSource) {
	t.Helper()

	svc, game, src := setupDoInstallTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: game.ID},
		[]domain.DownloadableFile{{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}})

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:         domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName: "default",
		Enabled:     true,
		FileIDs:     []string{"2"},
	}))

	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })
	verifyFix = true
	t.Cleanup(func() { verifyFix = false })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	return cmd, svc, game, src
}

// TestDoVerify_Fix_Missing_JSONNotesRedownloadFailure guards audit Finding
// 7 (Minor): a MISSING file's --fix redownload failure was reported ONLY
// in text mode ("Re-download failed: %v") - a --json caller saw the row
// stay Status "missing" with no indication of why the repair attempt
// itself failed. No cache entry is ever Store()d for "1.0", so the file
// is reported MISSING, and the fixture's missing download content makes
// the redownload attempt fail for real.
func TestDoVerify_Fix_Missing_JSONNotesRedownloadFailure(t *testing.T) {
	cmd, svc, game, _ := setupDoVerifyRedownloadTest(t)

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
		if f.ModID == "mod1" && f.FileID == "2" {
			found = true
			assert.Equal(t, "missing", f.Status)
			assert.NotEmpty(t, f.Note, "the redownload failure reason must reach the JSON note, not just the text-mode line")
		}
	}
	assert.True(t, found, "expected a mod1 file entry in JSON files: %+v", result.Files)
}

// TestDoVerify_Fix_Redownload_MapsGameIDPerSourceMapping guards the
// follow-up d1c0e0f explicitly flagged as out of scope: redownloadModFile
// (the MISSING/NO CHECKSUM --fix repair) had the identical unmapped
// svc.GetModFiles(ctx, mod.SourceID, &mod.Mod) call the version-record
// pre-pass was fixed for - mod.GameID is the LMM game ID (installed rows
// persist normalized IDs), but Service.GetModFiles forwards straight to
// the source with no game-ID translation, so on any game whose per-source
// mapping differs from its LMM ID (e.g. "skyrim-se" vs NexusMods'
// "skyrimspecialedition"), the redownload lookup queried the wrong game.
// Same capture pattern as TestDoVerify_VersionCheck_MapsGameIDPerSourceMapping:
// override the fixture's mapping (which otherwise coincides with game.ID,
// hiding the bug) and assert via fakeInstallSource.receivedGameFileIDs.
// The fixture's missing cache entry drives the MISSING branch; the NO
// CHECKSUM branch routes through the same redownloadModFile call, so one
// scenario pins both.
func TestDoVerify_Fix_Redownload_MapsGameIDPerSourceMapping(t *testing.T) {
	cmd, svc, game, src := setupDoVerifyRedownloadTest(t)
	game.SourceIDs["test-src"] = "mapped-domain"

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	// Call 1 is the version-record pre-pass (already mapped, d1c0e0f);
	// call 2 is redownloadModFile's own lookup for the MISSING repair.
	require.Len(t, src.receivedGameFileIDs, 2, "expected GetModFiles twice: version pre-pass + redownload lookup")
	assert.Equal(t, "mapped-domain", src.receivedGameFileIDs[1], "redownloadModFile must translate GameID through game.SourceIDs, same rule as Service.GetMod and the version-record pre-pass")
}

// TestDoVerify_Fix_NoChecksum_JSONNotesRedownloadFailure is the NO
// CHECKSUM half of audit Finding 7: same missing-download fixture, but
// with the cache entry actually present (so the file reads as NO
// CHECKSUM, not MISSING) and no checksum ever saved - the
// redownload-to-populate-checksum attempt fails the same way.
func TestDoVerify_Fix_NoChecksum_JSONNotesRedownloadFailure(t *testing.T) {
	cmd, svc, game, _ := setupDoVerifyRedownloadTest(t)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", "1.0", "mod1.esp", []byte("plugin content")))

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
		if f.ModID == "mod1" && f.FileID == "2" {
			found = true
			assert.Equal(t, "no_checksum", f.Status)
			assert.NotEmpty(t, f.Note, "the redownload failure reason must reach the JSON note, not just the text-mode line")
		}
	}
	assert.True(t, found, "expected a mod1 file entry in JSON files: %+v", result.Files)
}

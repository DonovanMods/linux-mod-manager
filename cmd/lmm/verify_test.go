package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: recordedVersion, GameID: game.ID},
		ProfileName: "default",
		Enabled:     true,
		FileIDs:     fileIDs,
	}))

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", recordedVersion, "mod1.esp", []byte("plugin content")))
	for _, id := range fileIDs {
		require.NoError(t, svc.SaveFileChecksum(context.Background(), "test-src", "mod1", game.ID, "default", id, "deadbeef"))
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
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.Contains(t, out, "1.5", "text must show the recorded version")
	assert.Contains(t, out, "1.0", "text must show the source-reported (effective) version")
	assert.Contains(t, out, "1 issue(s)")
	assert.NotContains(t, out, "0 issue(s)")

	jsonOutput = true
	outJSON := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	var resultDoc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &resultDoc))
	result := resultDoc.Result
	assert.Equal(t, 1, result.Issues, "version mismatch must be bucketed as an issue")
	assert.Equal(t, 0, result.Warnings)

	found := false
	for _, f := range result.Findings {
		if f.ModID == "mod1" && f.Status == "version_mismatch" {
			found = true
			assert.Empty(t, f.FileID, "FileID left empty per brief - fix branch fills it in later")
		}
	}
	assert.True(t, found, "expected a version_mismatch entry in JSON files: %+v", result.Findings)
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
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.Contains(t, out, "1 warning(s)")

	jsonOutput = true
	outJSON := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	var resultDoc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &resultDoc))
	result := resultDoc.Result
	assert.Equal(t, 0, result.Issues)
	assert.Equal(t, 1, result.Warnings, "unverifiable version must be bucketed as a warning, not an issue")

	found := false
	for _, f := range result.Findings {
		if f.ModID == "mod1" && f.Status == "version_unverifiable" {
			found = true
		}
	}
	assert.True(t, found, "expected a version_unverifiable entry in JSON files: %+v", result.Findings)
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
		return doVerify(cmd.Context(), svc, game, nil)
	})
	var resultDoc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &resultDoc))
	result := resultDoc.Result
	assert.Equal(t, 0, result.Issues, "source-unreachable is a warning, not an issue")
	assert.Equal(t, 1, result.Warnings, "the warnings count must be unaffected by adding the note")

	found := false
	for _, f := range result.Findings {
		if f.ModID == "mod1" && f.FileID == "" {
			found = true
			assert.Equal(t, "skipped", f.Status, "status must stay skipped")
			assert.Contains(t, f.Note, "connection refused", "the source-unreachable reason must reach the JSON note, not just the text-mode line")
		}
	}
	assert.True(t, found, "expected a mod1 version-check entry in JSON files: %+v", result.Findings)
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
		return doVerify(cmd.Context(), svc, game, nil)
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
		return doVerify(cmd.Context(), svc, game, nil)
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
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.Contains(t, out, "could not check cached file count", "a real ListFiles error must be surfaced, not silently swallowed")
	// #168/#212: the same permission-denied cache dir also breaks
	// convergeDeployedFiles' own attempt to list mod1's deployable files
	// while building its "provided" set - a second, independent warning via
	// a different code path for the same underlying permission problem, not
	// a double-count of the one above.
	assert.Contains(t, out, "2 warning(s)", "the surfaced error must actually count as a warning, not just print")
}

// --- doVerify --fix repairs version_mismatch rows (issue #94, Task A7) ---
//
// The repair itself - cache re-key, DB and profile correction, re-links,
// the sibling pass and its failure accounting - is pinned in
// internal/core (verify_test.go, verify_version_repair_test.go). What
// stays here is how doVerify renders it: the repair-detail lines and the
// summary tally.
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
		// ApplyDeploy (via deployInstalledMod) sets Deployed/LinkMethod
		// through the same targeted per-field writes SetModDeployed/
		// SetModLinkMethod use, not a full svc.SaveInstalledMod(...) upsert
		// that would wipe the checksum setupDoVerifyVersionTest just seeded
		// (the exact audit Finding 1 bug pattern, here as a test-setup
		// artifact rather than production code - fixed the same way), while
		// also deploying the real file a bare targeted-setter pair would not.
		mod, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, "default")
		require.NoError(t, err)
		deployInstalledMod(t, svc, game, &mod.Mod, "default")
	}

	pm := getProfileManager(svc)
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Version: "1.5", FileIDs: []string{"2"},
	}))

	verifyFix = true
	t.Cleanup(func() { verifyFix = false })

	return cmd, svc, game
}

// TestDoVerify_Fix_VersionMismatch_NotDeployed_PrintsRepairedLine: a
// repaired mismatch prints its "Repaired" line, and the summary no longer
// counts it as an outstanding issue.
func TestDoVerify_Fix_VersionMismatch_NotDeployed_PrintsRepairedLine(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})

	assert.Contains(t, out, "Repaired")
	assert.Contains(t, out, "1.5")
	assert.Contains(t, out, "1.0")
	assert.Contains(t, out, "All files verified OK.", "the repaired mismatch must no longer be counted as an outstanding issue")
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
// "default"-profile mismatch (recorded "1.5", effective "1.0") and adds
// "second", a sibling at the SAME wrong recorded version ("1.5") that the
// fix also corrects.
func setupDoVerifyFixSiblingTest(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()

	cmd, svc, game := setupDoVerifyFixTest(t, false)

	pm := getProfileManager(svc)

	_, err := pm.Create(context.Background(), game.ID, "second")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "second", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Version: "1.5", FileIDs: []string{"2"},
	}))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.5", GameID: game.ID},
		ProfileName: "second",
		Enabled:     true,
		FileIDs:     []string{"2"},
	}))
	// Seed a checksum for "second" too, mirroring "default"'s fixture
	// (setupDoVerifyVersionTest): a real sibling profile has its own
	// recorded checksum, not a NULL one.
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "test-src", "mod1", game.ID, "second", "2", "deadbeef"))

	return cmd, svc, game
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
		return doVerify(cmd.Context(), svc, game, nil)
	})

	assert.Contains(t, out, "Repaired")
	assert.Contains(t, out, "second", "text output must mention the repaired sibling profile by name")
}

// --- sibling-pass failures are surfaced, and counted (final-fix-wave) ---

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

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_UpsertModFails_WarnsInTextMode:
// a sibling whose profile document cannot be written prints a warning
// naming it, and the summary counts it rather than claiming a clean run.
// Core's TestVerify_Fix_VersionMismatch_SiblingUpsertFails_CountsAWarning
// pins the counts and the untouched profiles.
func TestDoVerify_Fix_VersionMismatch_SiblingProfile_UpsertModFails_WarnsInTextMode(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	cmd, svc, game := setupDoVerifyFixSiblingUpsertFailureTest(t)

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
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
}

// TestDoVerify_Fix_VersionMismatch_SiblingProfile_ListFails_WarnsOnce: when
// the profiles directory cannot be listed (0o311: no read bit), the
// sibling-discovery failure prints one warning line, and the convergence
// pass's refusal prints as its own "? convergence:" row. Core's
// TestVerify_Fix_VersionMismatch_SiblingListFails_WarnsOnce pins that the
// primary repair still goes through.
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
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.Equal(t, 1, strings.Count(out, "Warning"), "the pm.List failure must be surfaced exactly once, not once per (nonexistent) sibling")

	// PR #128 Copilot review round 4: a pm.List failure counts as exactly
	// one warning in the actual counter, not just in the printed text. The
	// second is the convergence pass, which removes nothing while lmm
	// cannot list the profiles to tell which is active (#462).
	assert.Contains(t, out, "2 warning(s)", "the pm.List failure must count as exactly one warning")
	assert.Contains(t, out, "? convergence: resolving the active profile for g1")
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
		return doVerify(cmd.Context(), svc, game, []string{"mod1"})
	})

	assert.Contains(t, out, "VERSION UNVERIFIABLE", "sanity: the scenario Claim 1 hypothesizes must actually be reached")
	assert.NotContains(t, out, "No files found for mod", "checksum rows for mod1 exist (same installed_mod_files table backs both FileIDs and the checksum listing) - the main loop's checked++ must have fired")
}

// TestDoVerify_VersionUnverifiable_HintMentionsUpdateRemedy pins the #131
// rider: VERSION UNVERIFIABLE and #95's stored-files-gone deploy error fire
// on the same underlying condition (recorded file IDs no longer offered
// upstream), so verify's hint must offer the same remedies - reinstall OR
// 'lmm update' - not just "reinstall to refresh".
func TestDoVerify_VersionUnverifiable_HintMentionsUpdateRemedy(t *testing.T) {
	cmd, svc, game, _ := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "3", Name: "Some Other File", FileName: "mod1-other.esp", IsPrimary: true, Category: "MAIN", Version: "2.0"},
	})

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, []string{"mod1"})
	})

	assert.Contains(t, out, "VERSION UNVERIFIABLE")
	assert.Contains(t, out, "reinstall the mod or run 'lmm update' to adopt the current version",
		"verify's hint must match #95's deploy-error remedies for the same condition")
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

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
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
		return doVerify(cmd.Context(), svc, game, nil)
	})
	var resultDoc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &resultDoc))
	result := resultDoc.Result

	found := false
	for _, f := range result.Findings {
		if f.ModID == "mod1" && f.FileID == "2" {
			found = true
			assert.Equal(t, "missing", f.Status)
			assert.NotEmpty(t, f.Note, "the redownload failure reason must reach the JSON note, not just the text-mode line")
		}
	}
	assert.True(t, found, "expected a mod1 file entry in JSON files: %+v", result.Findings)
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
		return doVerify(cmd.Context(), svc, game, nil)
	})

	// Call 1 is the version-record pre-pass (already mapped, d1c0e0f);
	// call 2 is redownloadModFile's own lookup for the MISSING repair.
	require.Len(t, src.receivedGameFileIDs, 2, "expected GetModFiles twice: version pre-pass + redownload lookup")
	assert.Equal(t, "mapped-domain", src.receivedGameFileIDs[1], "redownloadModFile must translate GameID through game.SourceIDs, same rule as Service.GetMod and the version-record pre-pass")
}

// TestDoVerify_Fix_Redownload_MapsGameIDForDownloadURL is the download-side
// half of the sibling test above, and guards #228: redownloadModFile mapped
// its GetModFiles lookup but then handed the RAW installed mod (&mod.Mod,
// GameID = the LMM game ID) to Service.DownloadMod, which forwards it
// straight to src.GetDownloadURL with no translation of its own. On any game
// whose per-source mapping differs from its LMM ID, the repair therefore
// resolved the download URL against the wrong upstream game. The fixture
// stages no content for file "2", so the HTTP fetch still 404s - but
// GetDownloadURL is reached (and recorded) before that, which is all this
// pins.
func TestDoVerify_Fix_Redownload_MapsGameIDForDownloadURL(t *testing.T) {
	cmd, svc, game, src := setupDoVerifyRedownloadTest(t)
	game.SourceIDs["test-src"] = "mapped-domain"

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})

	require.Len(t, src.receivedGameDownloadIDs, 1, "expected GetDownloadURL exactly once, for the MISSING file's repair")
	assert.Equal(t, "mapped-domain", src.receivedGameDownloadIDs[0], "the --fix re-download must resolve its URL against the source-mapped game, same rule as its own GetModFiles lookup")
}

// TestDoVerify_Fix_Redownload_EmptyMappingKeepsLMMGameIDForDownloadURL is the
// #228 fix's companion guard, mirroring the GetModFiles-side test above: an
// empty per-source mapping value means "this source applies to any game"
// (directory sources declare `donovan-mods: ""`), so it must NOT blank out
// the LMM game ID on the way to GetDownloadURL either.
func TestDoVerify_Fix_Redownload_EmptyMappingKeepsLMMGameIDForDownloadURL(t *testing.T) {
	cmd, svc, game, src := setupDoVerifyRedownloadTest(t)
	game.SourceIDs["test-src"] = ""

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	_ = captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})

	require.Len(t, src.receivedGameDownloadIDs, 1)
	assert.Equal(t, game.ID, src.receivedGameDownloadIDs[0], "an empty per-source mapping must keep the LMM game ID, not blank it out")
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
		return doVerify(cmd.Context(), svc, game, nil)
	})
	var resultDoc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &resultDoc))
	result := resultDoc.Result

	found := false
	for _, f := range result.Findings {
		if f.ModID == "mod1" && f.FileID == "2" {
			found = true
			assert.Equal(t, "no_checksum", f.Status)
			assert.NotEmpty(t, f.Note, "the redownload failure reason must reach the JSON note, not just the text-mode line")
		}
	}
	assert.True(t, found, "expected a mod1 file entry in JSON files: %+v", result.Findings)
}

// --- doVerify --fix checksum persistence for directory sources (#164) ---
//
// setupDoVerifyDirectorySourceTest reproduces the live #164 state: a real
// custom directory source (file ID "main", file:// download URL → local
// ingest) with one installed mod whose installed_mod_files row has a NULL
// checksum. memberFiles maps member-relative paths to contents; an empty map
// yields a mod directory with no regular files (no content to fingerprint).
// When ingest is true the mod is pre-ingested into the cache the same way
// install did - on the broken build that stores content but never a checksum.
func setupDoVerifyDirectorySourceTest(t *testing.T, modDirName string, memberFiles map[string]string, ingest bool) (*cobra.Command, *core.Service, *domain.Game, *domain.Mod, string) {
	t.Helper()

	root := t.TempDir()
	modDir := filepath.Join(root, modDirName)
	require.NoError(t, os.MkdirAll(modDir, 0755))
	for name, content := range memberFiles {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(modDir, name)), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(modDir, name), []byte(content), 0644))
	}

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src, err := custom.New(custom.SourceDefinition{
		ID:        "my-mods",
		Name:      "My Mods",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: root},
	})
	require.NoError(t, err)
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "7dtd", Name: "7 Days to Die", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"my-mods": ""}, // README-documented empty mapping for directory sources
		DeployMode: domain.DeployExtract,
	}

	ctx := context.Background()
	mod, err := svc.GetMod(ctx, "my-mods", game.ID, modDirName)
	require.NoError(t, err)

	if ingest {
		// SkipVerify reproduces the exact "broken build" state: the real
		// install path (PlanInstall/ApplyInstall) downloads and caches the
		// mod's content but - matching downloadSelectedFiles' own
		// "if !skipVerify && checksum != ..." gate - never calls
		// SaveFileChecksum, leaving the NULL checksum this fixture needs.
		plan, err := svc.PlanInstall(ctx, game, "default", "my-mods", modDirName, false)
		require.NoError(t, err)
		_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{SkipVerify: true}, nil)
		require.NoError(t, err)
	} else {
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod: *mod, ProfileName: "default", Enabled: true, FileIDs: []string{"main"},
		}))
	}

	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })
	verifyFix = true
	t.Cleanup(func() { verifyFix = false })
	oldNoColor := noColor
	noColor = true
	t.Cleanup(func() { noColor = oldNoColor })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	return cmd, svc, game, mod, modDir
}

// storedChecksum returns the checksum recorded for (sourceID, modID, fileID)
// in the given game/profile, failing the test if the row is absent.
func storedChecksum(t *testing.T, svc *core.Service, gameID, profile, sourceID, modID, fileID string) string {
	t.Helper()
	files, err := svc.GetFilesWithChecksums(context.Background(), gameID, profile)
	require.NoError(t, err)
	for _, f := range files {
		if f.SourceID == sourceID && f.ModID == modID && f.FileID == fileID {
			return f.Checksum
		}
	}
	t.Fatalf("no installed file row for %s/%s file %s in %+v", sourceID, modID, fileID, files)
	return ""
}

// TestDoVerify_Fix_DirectorySource_PersistsChecksum_SecondRunClean is THE
// #164 regression test: `verify --fix` on a directory-source mod (NULL
// checksum) must actually persist a checksum - re-read from the DB, not
// trusted from stdout - and a second verify run must come back clean instead
// of warning NO CHECKSUM forever.
func TestDoVerify_Fix_DirectorySource_PersistsChecksum_SecondRunClean(t *testing.T) {
	cmd, svc, game, _, _ := setupDoVerifyDirectorySourceTest(t, "BiggerBackpack",
		map[string]string{"ModInfo.xml": `<?xml version="1.0"?><xml><Name value="BiggerBackpack"/><Version value="1.2.0"/></xml>`}, true)

	require.Empty(t, storedChecksum(t, svc, game.ID, "default", "my-mods", "BiggerBackpack", "main"),
		"fixture must start with a NULL checksum")

	out := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.Contains(t, out, "checksum populated")

	assert.NotEmpty(t, storedChecksum(t, svc, game.ID, "default", "my-mods", "BiggerBackpack", "main"),
		"--fix claimed 'checksum populated', so a checksum must actually be in the DB")

	secondRun := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.NotContains(t, secondRun, "NO CHECKSUM", "the fix must converge - no permanent NO CHECKSUM loop")
	assert.Contains(t, secondRun, "All files verified OK.")
}

// TestDoVerify_Fix_DirectorySource_ReingestDropsRemovedMember is the #166
// regression at the verify --fix level: the fix's re-ingest lands on an
// EXISTING cache entry for the same (source, mod, version), and a member
// deleted from the source directory in the meantime must NOT survive it -
// the committed cache must shrink with the source, the refreshed digest must
// be persisted, and a second run must be clean.
func TestDoVerify_Fix_DirectorySource_ReingestDropsRemovedMember(t *testing.T) {
	cmd, svc, game, _, modDir := setupDoVerifyDirectorySourceTest(t, "BiggerBackpack",
		map[string]string{
			"ModInfo.xml": `<?xml version="1.0"?><xml><Name value="BiggerBackpack"/><Version value="1.2.0"/></xml>`,
			"stale.txt":   "to be removed",
		}, true)

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, "my-mods", "BiggerBackpack", "1.2.0")
	require.NoError(t, err)
	require.Contains(t, files, "stale.txt", "fixture: the member must be in the cache before the source shrinks")

	require.NoError(t, os.Remove(filepath.Join(modDir, "stale.txt")))

	out := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.Contains(t, out, "checksum populated")

	files, err = gameCache.ListFiles(game.ID, "my-mods", "BiggerBackpack", "1.2.0")
	require.NoError(t, err)
	assert.NotContains(t, files, "stale.txt", "#166: the re-ingest must drop the member deleted from the source")

	secondRun := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.Contains(t, secondRun, "All files verified OK.")
}

// TestDoVerify_Fix_NoChecksum_NotPersisted_HonestWarning covers the honesty
// half of #164 when there is genuinely no checksum to store (a directory mod
// with no regular files): --fix must NOT claim "checksum populated" - the
// NO CHECKSUM warning stays, with a note saying why, and is still counted.
func TestDoVerify_Fix_NoChecksum_NotPersisted_HonestWarning(t *testing.T) {
	cmd, svc, game, _, _ := setupDoVerifyDirectorySourceTest(t, "EmptyMod-1.0", nil, true)

	out := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})

	assert.NotContains(t, out, "checksum populated", "no write happened - claiming success is the #164 lie")
	assert.Contains(t, out, "NO CHECKSUM", "the warning must survive an unproductive --fix attempt")
	assert.Contains(t, out, "no checksum was available", "the user must learn why --fix could not populate it")
	// 2 warnings: the honest NO CHECKSUM one under test, plus the
	// pre-existing FILE COUNT MISMATCH a fileless mod's empty cache entry
	// legitimately draws from the file-count pre-pass.
	assert.Contains(t, out, "2 warning(s)", "an unpersisted checksum must still be counted as a warning")
}

// TestDoVerify_Fix_NoChecksum_NotPersisted_JSONStaysNoChecksum is the --json
// counterpart: status must stay "no_checksum" (not flip to "ok") with a note,
// and the warnings counter must reflect it.
func TestDoVerify_Fix_NoChecksum_NotPersisted_JSONStaysNoChecksum(t *testing.T) {
	cmd, svc, game, _, _ := setupDoVerifyDirectorySourceTest(t, "EmptyMod-1.0", nil, true)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	var resultDoc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &resultDoc))
	result := resultDoc.Result

	found := false
	for _, f := range result.Findings {
		if f.ModID == "EmptyMod-1.0" && f.FileID == "main" {
			found = true
			assert.Equal(t, "no_checksum", f.Status, "no write happened, so the row must not claim ok")
			assert.NotEmpty(t, f.Note, "the reason no checksum was stored must reach --json")
		}
	}
	assert.True(t, found, "expected an EmptyMod-1.0 file entry in JSON files: %+v", result.Findings)
	// 2 warnings: the honest no_checksum one under test, plus the
	// pre-existing file_count_mismatch a fileless mod's empty cache entry
	// legitimately draws from the file-count pre-pass.
	assert.Equal(t, 2, result.Warnings, "an unpersisted checksum must still be counted as a warning")
}

// TestDoVerify_Fix_Missing_NotPersisted_JSONStaysNoChecksum covers the
// MISSING branch's honesty (#164, "one step removed"): the re-ingest restores
// the cache (so the MISSING issue is genuinely resolved) but stores no
// checksum, so the row must surface as no_checksum with a note - not "ok".
func TestDoVerify_Fix_Missing_NotPersisted_JSONStaysNoChecksum(t *testing.T) {
	cmd, svc, game, _, _ := setupDoVerifyDirectorySourceTest(t, "EmptyMod-1.0", nil, false)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	var resultDoc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &resultDoc))
	result := resultDoc.Result

	found := false
	for _, f := range result.Findings {
		if f.ModID == "EmptyMod-1.0" && f.FileID == "main" {
			found = true
			assert.Equal(t, "no_checksum", f.Status, "cache was restored but no checksum stored - the row must say so, not claim ok")
			assert.NotEmpty(t, f.Note, "the reason no checksum was stored must reach --json")
		}
	}
	assert.True(t, found, "expected an EmptyMod-1.0 file entry in JSON files: %+v", result.Findings)
	assert.Equal(t, 0, result.Issues, "the MISSING issue itself was genuinely repaired")
	assert.Equal(t, 1, result.Warnings, "the unpersisted checksum must still be counted as a warning")
}

// TestDoVerify_Fix_Missing_DirectorySource_RedownloadPersistsChecksum: the
// MISSING branch's success path for a real directory mod - the re-ingest both
// restores the cache AND persists a checksum, so the run repairs completely.
func TestDoVerify_Fix_Missing_DirectorySource_RedownloadPersistsChecksum(t *testing.T) {
	cmd, svc, game, _, _ := setupDoVerifyDirectorySourceTest(t, "BiggerBackpack",
		map[string]string{"ModInfo.xml": `<?xml version="1.0"?><xml><Name value="BiggerBackpack"/><Version value="1.2.0"/></xml>`}, false)

	out := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.Contains(t, out, "MISSING")
	assert.Contains(t, out, "Re-downloaded OK")

	assert.NotEmpty(t, storedChecksum(t, svc, game.ID, "default", "my-mods", "BiggerBackpack", "main"),
		"a MISSING repair for a directory mod must persist the checksum too")

	secondRun := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.Contains(t, secondRun, "All files verified OK.")
}

// --- doVerify lock-awareness (#97, Task 8) ---
//
// A locked mod's VERSION MISMATCH must still be reported and counted as an
// issue - verify stays honest about state - but --fix must refuse to
// rewrite the locked record, since rewriting it would silently move the
// lock's meaning out from under the user. Separately, a locked mod whose DB
// version hasn't yet converged to the lock's target (pending a `profile
// apply`, not corruption) gets its own informational note instead of being
// silently swept into the quiet-OK path.

// TestDoVerify_Fix_VersionMismatchLocked_RefusesRepair: with the profile
// ref locked at the recorded "1.5", --fix prints the refusal (naming the
// mod, the lock's version and its profile, with -s/-p flagged remedies)
// and still counts the issue. Core's
// TestVerify_Fix_VersionMismatch_LockedPrimary_RefusesRepair pins that
// the DB, the cache and the profile ref are left alone.
func TestDoVerify_Fix_VersionMismatchLocked_RefusesRepair(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, false)

	pm := getProfileManager(svc)
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "test-src", "mod1", "1.5"))

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})

	assert.Contains(t, out, "1 issue(s)", "a locked mod's version mismatch must still be reported and counted as an issue")
	assert.Contains(t, out, "--fix skipped: Mod One is locked at v1.5 in profile default", "the refusal text must name the mod, the lock's target version, and the profile holding the lock")
	assert.Contains(t, out, "lmm mod lock -s test-src -p default mod1 <version>", "the lock remedy must carry -s/-p so a copy-paste can never resolve against a different source/profile")
	assert.Contains(t, out, "lmm mod unlock -s test-src -p default mod1", "the refusal must also offer the unlock alternative, flagged the same way")
	assert.NotContains(t, out, "Repaired", "a locked record must never be silently repaired")
}

// setupDoVerifyLockDriftTest builds a fixture where the recorded DB version
// ("1.5") matches what the installed file ID actually reports upstream (no
// VERSION MISMATCH), but the profile ref is locked at a DIFFERENT version
// ("2.0") - the lock hasn't converged yet (a `profile apply` is pending),
// which is expected drift, not corruption.
func setupDoVerifyLockDriftTest(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()

	cmd, svc, game, _ := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.5"},
	})

	pm := getProfileManager(svc)
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Version: "1.5", FileIDs: []string{"2"},
	}))
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "test-src", "mod1", "2.0"))

	return cmd, svc, game
}

// TestDoVerify_LockedDrift_PrintsInformationalNote guards the pending-
// convergence case: DB and the source-verified "effective" version agree
// (no mismatch), but the locked ref's target ("2.0") differs from what's
// actually installed ("1.5"). This must print an informational note - not
// be silently absorbed into the quiet-OK path - and must NOT be counted as
// an issue.
func TestDoVerify_LockedDrift_PrintsInformationalNote(t *testing.T) {
	cmd, svc, game := setupDoVerifyLockDriftTest(t)

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})

	assert.Contains(t, out, "Mod One — lock pending convergence (installed v1.5, locked v2.0)", "the informational line must name the mod and both versions")
	assert.Contains(t, out, "lmm profile apply", "the informational line must point at the remedy command")
	assert.NotContains(t, out, "1 issue(s)", "pending convergence must not be counted as an issue")
}

// TestDoVerify_LockedDrift_JSONNotCountedAsIssue guards the --json half:
// issues stays 0 for pending-convergence drift.
func TestDoVerify_LockedDrift_JSONNotCountedAsIssue(t *testing.T) {
	cmd, svc, game := setupDoVerifyLockDriftTest(t)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})

	var resultDoc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &resultDoc))
	result := resultDoc.Result
	assert.Equal(t, 0, result.Issues, "pending convergence must not be counted as an issue")

	found := false
	for _, f := range result.Findings {
		if f.ModID == "mod1" && f.FileID == "" {
			found = true
			assert.Contains(t, f.Note, "lock pending convergence", "the JSON note must additively carry the pending-convergence detail")
		}
	}
	assert.True(t, found, "expected a mod1 entry in JSON files: %+v", result.Findings)
}

// TestDoVerify_LockedConverged_NoNote closes the named no-note edge case: a
// locked mod whose DB version ALREADY equals the lock's target (fully
// converged - no pending `profile apply` needed) must stay on the plain
// quiet-OK path, exactly like an unlocked matching mod - no informational
// note printed, and (matching the pre-existing quiet-OK convention) no
// extra JSON row emitted for it either.
func TestDoVerify_LockedConverged_NoNote(t *testing.T) {
	cmd, svc, game, _ := setupDoVerifyVersionTest(t, "1.5", []string{"2"}, []domain.DownloadableFile{
		{ID: "2", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.5"},
	})

	pm := getProfileManager(svc)
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Version: "1.5", FileIDs: []string{"2"},
	}))
	// Lock target equals the already-installed version: converged, nothing pending.
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "test-src", "mod1", "1.5"))

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	assert.NotContains(t, out, "lock pending convergence", "a converged lock must not print the pending-convergence note")
	assert.NotContains(t, out, "1 issue(s)")

	jsonOutput = true
	outJSON := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})
	var resultDoc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &resultDoc))
	result := resultDoc.Result
	assert.Equal(t, 0, result.Issues)
	for _, f := range result.Findings {
		if f.ModID == "mod1" && f.FileID == "" {
			t.Fatalf("expected no version-check row for a converged locked mod, got: %+v", f)
		}
	}
}

// --- verify --fix leaves a user's replacement of a deployed link alone (#483) ---

// TestDoVerify_Fix_VersionMismatch_Deployed_PreservesReplacedLinkJSON checks
// the JSON surface when a user replaces a recorded symlink with a regular
// file while the cache version also needs repair. The file remains theirs,
// and JSON must explain why verify --fix leaves it in place (#483).
func TestDoVerify_Fix_VersionMismatch_Deployed_PreservesReplacedLinkJSON(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, true)

	deployedPath := filepath.Join(game.ModPath, "mod1.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("squatter"), 0o644))

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	outJSON := captureStdout(t, func() error {
		return doVerify(cmd.Context(), svc, game, nil)
	})

	var resultDoc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &resultDoc))
	result := resultDoc.Result
	assert.Positive(t, result.Warnings)

	foundVersion, foundReplacement := false, false
	for _, f := range result.Findings {
		if f.ModID == "mod1" && f.FileID == "" {
			foundVersion = true
		}
		if f.ModID == "mod1" && f.Status == core.VerifyStatusDeployedModified {
			foundReplacement = true
			assert.Contains(t, f.Note, "you replaced lmm's link")
		}
	}
	assert.True(t, foundVersion, "expected a mod1 version-check entry in JSON files: %+v", result.Findings)
	assert.True(t, foundReplacement, "expected the replacement warning in JSON files: %+v", result.Findings)

	info, err := os.Lstat(deployedPath)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the user's replacement must remain a regular file")
	content, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	assert.Equal(t, "squatter", string(content))
}

// A genuine undeploy error can still accompany a successful relink. Keep
// the JSON diagnostic contract independent of user-replacement fixtures.
func TestDoVerify_Fix_VersionMismatch_Deployed_UndeployErrorJSONNotesIt(t *testing.T) {
	cmd, svc, game := setupDoVerifyFixTest(t, true)
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "lmm.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	_, err = conn.Exec(`CREATE TRIGGER block_deployed_file_delete
		BEFORE DELETE ON deployed_files BEGIN
			SELECT RAISE(ABORT, 'synthetic undeploy failure');
		END`)
	require.NoError(t, err)

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	outJSON := captureStdout(t, func() error { return doVerify(cmd.Context(), svc, game, nil) })
	var doc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &doc))
	var found bool
	for _, finding := range doc.Result.Findings {
		if finding.ModID == "mod1" && finding.FileID == "" {
			found = true
			assert.Equal(t, "ok", finding.Status)
			assert.Contains(t, finding.Note, "undeploy")
			assert.Contains(t, finding.Note, "synthetic undeploy failure")
		}
	}
	assert.True(t, found, "%+v", doc.Result.Findings)
	info, err := os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	require.NoError(t, err)
	assert.True(t, info.Mode()&os.ModeSymlink != 0, "the relink still succeeds")
}

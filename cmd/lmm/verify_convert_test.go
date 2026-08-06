package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pakOutcomeCompilerSource wraps compilerInstallSource (install_compile_test.go)
// so a test can script per-ref pak-conversion failures (#221 Task 11),
// mirroring internal/core/service_icarus_compile_test.go's
// pakConversionOutcomeSource at the CLI-seam level: any source whose ModRef
// is in failRefs is skipped from the merged output and reported via the
// returned failed slice, with a "... - deploying raw" warning, matching
// internal/source/icarus/merge.go's real pak-dispatch failure path.
type pakOutcomeCompilerSource struct {
	*compilerInstallSource
	failRefs map[string]string
}

func (s *pakOutcomeCompilerSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, []source.MergeFailure, error) {
	s.compileCalls++
	var out []byte
	var warnings []string
	var failed []source.MergeFailure
	for _, src := range sources {
		if reason, bad := s.failRefs[src.ModRef]; bad {
			failed = append(failed, source.MergeFailure{ModRef: src.ModRef, Reason: reason})
			warnings = append(warnings, fmt.Sprintf("mod %s: pak conversion failed: %s - deploying raw", src.ModRef, reason))
			continue
		}
		data, err := os.ReadFile(src.SourcePath)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, data...)
	}
	return warnings, failed, os.WriteFile(outputPath, out, 0o644)
}

var _ source.MergeCompiler = (*pakOutcomeCompilerSource)(nil)

// seedEnabledPakModCLI installs an ENABLED pak-kind mod carrying both a
// retained pak (cache.RetainedSourceName) and a deployable pak copy recorded
// as the manifest's sole member - the #221 Task 9 ingest shape for a pak
// eligible for conversion, before any merge/reconcile has run. Mirrors
// internal/core/service_icarus_compile_test.go's seedEnabledPakMod at the
// CLI-seam level. Assumes a "default" profile already exists (every test
// here builds on setupDoUpdateRecompileTest, which creates one).
func seedEnabledPakModCLI(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version, fileID string, pakContent []byte) {
	t.Helper()
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(fileID), pakContent))
	member := modID + ".pak"
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, member, pakContent))
	versionDir := gameCache.ModPath(game.ID, sourceID, modID, version)
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, fileID, []string{member}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: modID, Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: version, FileIDs: []string{fileID}}))
}

// seedLegacyPakModCLI installs an ENABLED pak-kind mod in the PRE-#221 shape:
// the deployable pak is stored and manifested as usual, but NO retained
// source was ever kept - the exact state PakNeedsReingest exists to detect.
func seedLegacyPakModCLI(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version, fileID string, pakContent []byte) {
	t.Helper()
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, fileID, pakContent))
	versionDir := gameCache.ModPath(game.ID, sourceID, modID, version)
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, fileID, []string{fileID}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: modID, Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: version, FileIDs: []string{fileID}}))
}

// TestVerifyReportsConversionFailed proves `lmm verify` surfaces a #221
// pak-conversion failure recorded on the merged pak's own stored fingerprint
// (Task 8's MergedPakOutcomes): text mode prints "CONVERSION FAILED" and
// "deploying raw", --json reports status "conversion_failed" with the fail
// reason as note, and it's counted as a warning (not an issue - the mod
// stays raw-deployed, a working fallback, not corruption).
func TestVerifyReportsConversionFailed(t *testing.T) {
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	game.ConvertPaks = true

	const modID, version, fileID = "raw-pak-mod", "1.0", "modfile.pak"
	seedEnabledPakModCLI(t, svc, game, "fake-compiler", modID, version, fileID, []byte("pak-bytes"))

	outcome := &pakOutcomeCompilerSource{
		compilerInstallSource: compiler,
		failRefs:              map[string]string{"fake-compiler:" + modID: "table X not present in current base"},
	}
	svc.RegisterSource(outcome)

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	verifyProfile = "default"
	jsonOutput = true
	t.Cleanup(func() { verifyProfile = ""; jsonOutput = false })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	out := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })

	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	var found *verifyFileJSON
	for i := range result.Files {
		if result.Files[i].Status == "conversion_failed" {
			found = &result.Files[i]
		}
	}
	require.NotNil(t, found, "expected a conversion_failed row: %+v", result.Files)
	assert.Equal(t, modID, found.ModID)
	assert.Equal(t, "table X not present in current base", found.Note)
	assert.GreaterOrEqual(t, result.Warnings, 1)

	jsonOutput = false
	textOut := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })
	assert.Contains(t, textOut, "CONVERSION FAILED")
	assert.Contains(t, textOut, "deploying raw")
}

// TestVerifyReportsConversionFailed_UninstalledMod guards the Copilot round
// 1 fix (PR #222): a merged-pak fingerprint entry can outlive the mod it
// names - the mod may since have been uninstalled while the merged pak
// (and its recorded conversion failure) is still in place - in which case
// verify's modNames lookup misses and must fall back to entry.ModID instead
// of reporting a blank name (blank human line, JSON mod_name:"").
func TestVerifyReportsConversionFailed_UninstalledMod(t *testing.T) {
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	game.ConvertPaks = true

	const modID, version, fileID = "gone-pak-mod", "1.0", "modfile.pak"
	seedEnabledPakModCLI(t, svc, game, "fake-compiler", modID, version, fileID, []byte("pak-bytes"))

	outcome := &pakOutcomeCompilerSource{
		compilerInstallSource: compiler,
		failRefs:              map[string]string{"fake-compiler:" + modID: "table X not present in current base"},
	}
	svc.RegisterSource(outcome)

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	// Uninstall the mod - only the installed-mod row goes away; the merged
	// pak's stored fingerprint (and the conversion-failure entry on it) is
	// profile-scoped and untouched, exactly the state a fingerprint entry
	// for a since-removed mod is in.
	require.NoError(t, svc.DeleteInstalledMod("fake-compiler", modID, game.ID, "default"))

	verifyProfile = "default"
	jsonOutput = true
	t.Cleanup(func() { verifyProfile = ""; jsonOutput = false })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	out := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })

	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	var found *verifyFileJSON
	for i := range result.Files {
		if result.Files[i].Status == "conversion_failed" {
			found = &result.Files[i]
		}
	}
	require.NotNil(t, found, "expected a conversion_failed row: %+v", result.Files)
	assert.Equal(t, modID, found.ModID)
	assert.Equal(t, modID, found.ModName, "ModName must fall back to the raw ModID when the mod is no longer installed")

	jsonOutput = false
	textOut := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })
	assert.Contains(t, textOut, modID+" - CONVERSION FAILED", "text output must show the fallback name, not a blank")
}

// TestVerifyNeedsReingest_ReportsThenFixes proves the #221 lazy-migration
// contract: a convert-eligible pak whose cache entry predates pak retention
// (deployable pak present, no retained source) is reported as
// "needs_reingest" by plain verify, and `verify --fix` re-ingests it through
// the existing redownload path - afterward the cache entry has the retained
// source and the manifest records the pak member (Task 9's ingest shape).
func TestVerifyNeedsReingest_ReportsThenFixes(t *testing.T) {
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	game.ConvertPaks = true

	const modID, version, fileID = "legacy-pak-mod", "1.0", "LegacyMod.pak"
	seedLegacyPakModCLI(t, svc, game, "fake-compiler", modID, version, fileID, []byte("legacy-pak-bytes"))

	compiler.AddMod(&domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Legacy Pak Mod", Version: version, GameID: game.ID},
		[]domain.DownloadableFile{{ID: fileID, FileName: "LegacyMod.pak", IsPrimary: true}})
	compiler.AddDownload(fileID, []byte("fresh-pak-bytes"))

	verifyProfile = "default"
	jsonOutput = true
	t.Cleanup(func() { verifyProfile = ""; jsonOutput = false })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	out := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })
	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	var found *verifyFileJSON
	for i := range result.Files {
		if result.Files[i].ModID == modID {
			found = &result.Files[i]
		}
	}
	require.NotNil(t, found, "expected a row for the legacy pak mod: %+v", result.Files)
	assert.Equal(t, "needs_reingest", found.Status)
	assert.Contains(t, found.Note, "verify --fix")
	firstRunWarnings := result.Warnings

	verifyFix = true
	t.Cleanup(func() { verifyFix = false })
	fixOut := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })

	// The --fix run's OWN JSON output must reflect the successful re-ingest
	// immediately, in the SAME run - every other --fix success path in this
	// file (MISSING, NO CHECKSUM, stale_deployment -> fixed_stale_deployment)
	// rewrites status/note and backs the row out of the warnings count
	// instead of leaving it reading as still-outstanding; needs_reingest
	// must follow the same convention.
	var fixResult verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(fixOut), &fixResult))
	var fixedRow *verifyFileJSON
	for i := range fixResult.Files {
		if fixResult.Files[i].ModID == modID {
			fixedRow = &fixResult.Files[i]
		}
	}
	require.NotNil(t, fixedRow, "expected a row for the legacy pak mod in the --fix run: %+v", fixResult.Files)
	assert.Equal(t, "fixed_needs_reingest", fixedRow.Status, "a successful same-run re-ingest must rewrite the row to a fixed-state status")
	assert.NotContains(t, fixedRow.Note, "run 'lmm verify --fix'", "the note must reflect success, not still tell the user to run the fix that just ran")
	assert.Equal(t, firstRunWarnings-1, fixResult.Warnings, "a successfully re-ingested row must be backed out of the warnings count, like every other --fix success path")

	gameCache := svc.GetGameCache(game)
	retainedPath := gameCache.GetFilePath(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID))
	_, statErr := os.Stat(retainedPath)
	assert.NoError(t, statErr, "the retained source must exist after --fix re-ingests the legacy pak")

	manifests, merr := gameCache.FileManifests(game.ID, "fake-compiler", modID, version)
	require.NoError(t, merr)
	assert.True(t, manifests[fileID].Recorded, "the manifest must now be a Task-9-shaped recorded marker, not the pre-#221 bare one")

	// End to end: the same verify.go:683 SyncMergedPak call --fix always
	// makes also converges the freshly-retained pak into the merged pak
	// (nothing scripted a conversion failure here), so the entry's manifest
	// legitimately flips to zero members (merged-claimed) rather than
	// staying at the raw-deploy member Task 9's ingest alone would leave -
	// the direct, convergence-independent proof of the fix is that the
	// entry itself no longer needs re-ingesting.
	fixedMod, err := svc.GetInstalledMod("fake-compiler", modID, game.ID, "default")
	require.NoError(t, err)
	need, nerr := svc.PakNeedsReingest(game, fixedMod, fileID)
	require.NoError(t, nerr)
	assert.False(t, need, "the entry must no longer need reingest after --fix re-ingests it")

	jsonOutput = true
	verifyFix = false
	secondOut := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })
	var secondResult verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(secondOut), &secondResult))
	for _, f := range secondResult.Files {
		if f.ModID == modID {
			assert.NotEqual(t, "needs_reingest", f.Status, "a re-ingested entry must not be flagged needs_reingest again")
		}
	}
}

// TestVerifyNeedsReingest_ModOptedOut_NotFlagged proves the lazy migration
// respects the per-mod opt-out (#221): a mod with ConvertPaks=false must
// never be flagged needs_reingest, even though its cache entry has the same
// pre-#221 shape (deployable pak, no retained source) as a convert-eligible
// one.
func TestVerifyNeedsReingest_ModOptedOut_NotFlagged(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	game.ConvertPaks = true

	const modID, version, fileID = "opted-out-pak-mod", "1.0", "OptedOut.pak"
	seedLegacyPakModCLI(t, svc, game, "fake-compiler", modID, version, fileID, []byte("legacy-pak-bytes"))
	require.NoError(t, svc.SetModConvertPaks("fake-compiler", modID, game.ID, "default", false))

	verifyProfile = "default"
	jsonOutput = true
	t.Cleanup(func() { verifyProfile = ""; jsonOutput = false })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })

	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	for _, f := range result.Files {
		if f.ModID == modID {
			assert.NotEqual(t, "needs_reingest", f.Status,
				"a mod opted out of pak conversion (ConvertPaks=false) must not be flagged for reingest")
		}
	}
}

// TestVerifyNeedsReingest_CheckErrorSurfacedUnderVerbose is the verify.go
// minor fix (final whole-branch review of #221): the needs_reingest block
// used to silently swallow a genuine PakNeedsReingest error (`nerr == nil &&
// need`, where a non-nil nerr just fell through to the ordinary MISSING/NO
// CHECKSUM checks below with no trace at all). Forces a real error - not
// "nothing ingested yet" (fs.ErrNotExist), which PakNeedsReingest treats as
// a normal, silent "false" - by stripping all permissions from the cache
// mod-key's parent directory (same technique as
// TestDoVerify_Fix_VersionMismatch_OldPathStatErrorBlocksRepair), so
// PakNeedsReingest's own os.Stat can't even traverse into the version dir.
func TestVerifyNeedsReingest_CheckErrorSurfacedUnderVerbose(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	game.ConvertPaks = true

	const modID, version, fileID = "perm-blocked-pak-mod", "1.0", "PermBlocked.pak"
	seedLegacyPakModCLI(t, svc, game, "fake-compiler", modID, version, fileID, []byte("legacy-pak-bytes"))

	gameCache := svc.GetGameCache(game)
	versionDir := gameCache.ModPath(game.ID, "fake-compiler", modID, version)
	parentDir := filepath.Dir(versionDir)
	require.NoError(t, os.Chmod(parentDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(parentDir, 0o755) }) // restore before TempDir's own cleanup removes it

	verifyProfile = "default"
	jsonOutput = false
	verbose = true
	t.Cleanup(func() { verifyProfile = ""; verbose = false })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	quietOut := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })
	verbose = false
	// Non-verbose: the check failure must not be reported as a fabricated
	// pass/fail status at all (no needs_reingest row, since the check itself
	// never resolved either way).
	assert.NotContains(t, quietOut, "NEEDS REINGEST")

	verbose = true
	verboseOut := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })
	assert.Contains(t, verboseOut, "could not check pak-reingest status", "a genuine check failure must be surfaced under --verbose, not silently dropped")
	assert.Contains(t, verboseOut, modID)
}

// TestVerifyFileCountCarveOutMembersAware proves the #221 fix to
// hasRetainedSource's retain-only carve-out: before this fix, ANY cache
// entry carrying a retained source was suppressed from the file-count check,
// even a genuinely broken one. A pak entry whose manifest records a member
// (the raw-deploy default every convert-eligible pak carries) but whose
// actual deployable content never made it to disk - a retained source
// present alongside zero real files - must still be reported as
// file_count_mismatch: the carve-out now only suppresses entries whose
// manifests record ZERO members (the pure retain-only exmodz case).
func TestVerifyFileCountCarveOutMembersAware(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)

	const sourceID, modID, version, fileID = "fake-compiler", "broken-pak-mod", "1.0", "BrokenMod.pak"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(fileID), []byte("retained-bytes")))
	versionDir := gameCache.ModPath(game.ID, sourceID, modID, version)
	// The manifest claims one member ("broken-pak-mod.pak"), but that member
	// is never actually written to the cache directory - simulating a pak
	// entry whose deployable content went missing while the retained source
	// survived. gameCache.ListFiles will therefore report 0 actual files.
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, fileID, []string{modID + ".pak"}))

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: "Broken Pak Mod", Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: version, FileIDs: []string{fileID}}))

	verifyProfile = "default"
	jsonOutput = true
	t.Cleanup(func() { verifyProfile = ""; jsonOutput = false })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })

	var result verifyJSONOutput
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	found := false
	for _, f := range result.Files {
		if f.ModID == modID && f.Status == "file_count_mismatch" {
			found = true
		}
	}
	assert.True(t, found, "a retained pak entry whose manifest records members must NOT be suppressed by the retain-only carve-out: %+v", result.Files)
}

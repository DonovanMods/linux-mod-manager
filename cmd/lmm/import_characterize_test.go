package main

// Task 17 (#291): capture tests for `lmm import` pinned on pre-lift code,
// before Tasks 18-19 lift runImportScan/doImport into internal/core. These
// tests are recorded ONCE and then frozen - a future diff here is a defect,
// never a re-record (docs/plans/2026-08-28-v2-phase2-impl.md Global
// Constraints). Every test asserts END STATE (DB rows, profile YAML,
// cache tree) alongside console output, per Unit J's review finding that
// stdout-only characterization missed a real state regression.
//
// Setup mirrors import_test.go's setupDoImportTest/newFakeMatchSource so
// these tests share its fakes and globals-management convention exactly.

import (
	"context"
	"errors"
	"io"
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

// captureStdoutAndStderr redirects both os.Stdout and os.Stderr for the
// duration of fn, returning each stream's captured text separately - doImport/
// runImportScan write to the two streams independently throughout (most
// diagnostics go to stderr, but several "verbose" warnings go to stdout via
// plain fmt.Printf), so keeping them apart lets a test pin exactly which
// stream a given line lands on, unlike install_test.go's captureStdoutErr/
// uninstall_test.go's captureStderrErr, which each discard the other stream.
func captureStdoutAndStderr(t *testing.T, fn func() error) (stdout, stderr string, fnErr error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	errR, errW, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	fnErr = fn()
	require.NoError(t, outW.Close())
	require.NoError(t, errW.Close())
	outBytes, err := io.ReadAll(outR)
	require.NoError(t, err)
	require.NoError(t, outR.Close())
	errBytes, err := io.ReadAll(errR)
	require.NoError(t, err)
	require.NoError(t, errR.Close())
	return string(outBytes), string(errBytes), fnErr
}

// --- scan mode ---

// TestRunImportScan_ExtractModeWarning_ExactOutput pins the extract-mode
// caveat note (game.DeployMode != DeployCopy) that setupDoImportTest's bare
// game (DeployMode zero value = DeployExtract) always triggers, followed by
// a fully deterministic empty scan (empty mod_path, no installed mods).
func TestRunImportScan_ExtractModeWarning_ExactOutput(t *testing.T) {
	svc, game := setupDoImportTest(t)
	importSkipMatch = true // isolate from core's matchScannedMod entirely

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out, _, err := captureStdoutAndStderr(t, func() error {
		return runImportScan(cmd, game, svc, "default")
	})

	require.NoError(t, err)
	expected := "Note: Scan import for extract-mode games tracks mods in-place without caching.\n" +
		"      Uninstall will only remove the database entry, not the files.\n" +
		"\n" +
		"Scanning " + game.ModPath + " for untracked mods...\n" +
		"Found 0 files, 0 untracked\n" +
		"\n" +
		"All mods are already tracked!\n"
	assert.Equal(t, expected, out)

	mods, mErr := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, mErr)
	assert.Empty(t, mods, "an empty scan must leave nothing installed")
}

// TestRunImportScan_BackfillLoop_UpdatesMissingMetadataAndSavesToDB pins the
// backfill loop's console output and its DB write: an already-installed,
// source-linked mod missing Author/SourceURL gets them filled in from a
// fresh GetMod call and saved back via SaveInstalledMod.
func TestRunImportScan_BackfillLoop_UpdatesMissingMetadataAndSavesToDB(t *testing.T) {
	svc, game := setupDoImportTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	src := newFakeMatchSource("acme-source")
	src.mods["77"] = &domain.Mod{ID: "77", SourceID: "acme-source", Name: "Needs Backfill", Author: "Jane Modder", SourceURL: "http://example.com/mod77", Version: "1.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	existing := &domain.InstalledMod{
		Mod:          domain.Mod{ID: "77", SourceID: "acme-source", Name: "Needs Backfill", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
	}
	require.NoError(t, svc.SaveInstalledMod(context.Background(), existing))

	importSkipMatch = false

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out, _, err := captureStdoutAndStderr(t, func() error {
		return runImportScan(cmd, game, svc, "default")
	})

	require.NoError(t, err)
	expected := "Note: Scan import for extract-mode games tracks mods in-place without caching.\n" +
		"      Uninstall will only remove the database entry, not the files.\n" +
		"\n" +
		"Scanning " + game.ModPath + " for untracked mods...\n" +
		"Found 0 files, 0 untracked\n" +
		"\n" +
		"Backfilling metadata for 1 mod(s)...\n" +
		"Updated metadata for 1 existing mod(s)\n" +
		"\n" +
		"All mods are already tracked!\n"
	assert.Equal(t, expected, out)

	updated, gErr := svc.GetInstalledMod(context.Background(), "acme-source", "77", "g1", "default")
	require.NoError(t, gErr)
	assert.Equal(t, "Jane Modder", updated.Author, "backfill must write the fetched Author to the DB row")
	assert.Equal(t, "http://example.com/mod77", updated.SourceURL, "backfill must write the fetched SourceURL to the DB row")
}

// TestRunImportScan_BackfillLoop_SkipMatchHonored_NoBackfillAttempted is the
// --skip-match twin: the same installed mod missing metadata is left
// completely untouched - the backfill block never runs at all, not even the
// "Backfilling..." announcement.
func TestRunImportScan_BackfillLoop_SkipMatchHonored_NoBackfillAttempted(t *testing.T) {
	svc, game := setupDoImportTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	src := newFakeMatchSource("acme-source")
	src.mods["77"] = &domain.Mod{ID: "77", SourceID: "acme-source", Name: "Needs Backfill", Author: "Jane Modder", SourceURL: "http://example.com/mod77", Version: "1.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	existing := &domain.InstalledMod{
		Mod:          domain.Mod{ID: "77", SourceID: "acme-source", Name: "Needs Backfill", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
	}
	require.NoError(t, svc.SaveInstalledMod(context.Background(), existing))

	importSkipMatch = true // already the setup default, set explicitly for clarity

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out, _, err := captureStdoutAndStderr(t, func() error {
		return runImportScan(cmd, game, svc, "default")
	})

	require.NoError(t, err)
	expected := "Note: Scan import for extract-mode games tracks mods in-place without caching.\n" +
		"      Uninstall will only remove the database entry, not the files.\n" +
		"\n" +
		"Scanning " + game.ModPath + " for untracked mods...\n" +
		"Found 0 files, 0 untracked\n" +
		"\n" +
		"All mods are already tracked!\n"
	assert.Equal(t, expected, out, "--skip-match must skip the backfill block entirely, not just the source-matching block")

	unchanged, gErr := svc.GetInstalledMod(context.Background(), "acme-source", "77", "g1", "default")
	require.NoError(t, gErr)
	assert.Empty(t, unchanged.Author, "--skip-match must leave the DB row's missing Author untouched")
	assert.Empty(t, unchanged.SourceURL, "--skip-match must leave the DB row's missing SourceURL untouched")
}

// TestRunImportScan_DuplicateSkip_NoDBWriteNoCacheWrite pins FindDuplicateMod's
// skip path: an untracked file whose detected name normalizes to match an
// already-installed mod's name is neither saved nor cached.
func TestRunImportScan_DuplicateSkip_NoDBWriteNoCacheWrite(t *testing.T) {
	svc, game := setupDoImportTest(t)
	game.DeployMode = domain.DeployCopy

	existing := &domain.InstalledMod{
		Mod:          domain.Mod{ID: "existing-id", SourceID: domain.SourceLocal, Name: "CoolMod", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
	}
	require.NoError(t, svc.SaveInstalledMod(context.Background(), existing))

	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "CoolMod-2.0.zip"), []byte("dup-payload"), 0644))

	importSkipMatch = true
	importForce = true // isolate from the confirm prompt

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out, _, err := captureStdoutAndStderr(t, func() error {
		return runImportScan(cmd, game, svc, "default")
	})

	require.NoError(t, err)
	expected := "Scanning " + game.ModPath + " for untracked mods...\n" +
		"Found 1 files, 1 untracked\n" +
		"\n" +
		"Ready to import 1 mod(s):\n" +
		"  - CoolMod (local, v2.0)\n" +
		"  ⊘ CoolMod-2.0.zip: skipped (duplicate of \"CoolMod\")\n" +
		"\n" +
		"Imported: 0, Skipped: 1, Failed: 0\n"
	assert.Equal(t, expected, out)

	mods, mErr := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1, "the duplicate must not be saved - only the pre-existing row remains")
	assert.Equal(t, "existing-id", mods[0].ID)

	cacheMatches, gErr := filepath.Glob(filepath.Join(svc.GlobalCacheDir(), "g1", "local-*"))
	require.NoError(t, gErr)
	assert.Empty(t, cacheMatches, "a skipped duplicate must not write a cache entry")
}

// TestRunImportScan_ConfirmDecline_ReturnsPlainCancelledError_NotErrCancelled
// pins the confirm-prompt decline path, including a real gap this
// characterization surfaces: unlike purge/install's declined prompts, scan
// import's decline returns a bare fmt.Errorf("import cancelled"), NOT the
// shared cmd/lmm.ErrCancelled sentinel root.go's exit-code-2 handling checks
// via errors.Is - this test pins that fact so a future lift decision to
// unify onto ErrCancelled is a deliberate, visible change, not an accident.
func TestRunImportScan_ConfirmDecline_ReturnsPlainCancelledError_NotErrCancelled(t *testing.T) {
	svc, game := setupDoImportTest(t)
	game.DeployMode = domain.DeployCopy
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "LooseMod-1.0.zip"), []byte("loose-payload"), 0644))

	importSkipMatch = true
	importForce = false
	importDryRun = false

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out string
	var err error
	withStdin(t, "n\n", func() {
		out, _, err = captureStdoutAndStderr(t, func() error {
			return runImportScan(cmd, game, svc, "default")
		})
	})

	require.Error(t, err)
	assert.Equal(t, "import cancelled", err.Error())
	assert.False(t, errors.Is(err, ErrCancelled), "scan import's decline is NOT the shared ErrCancelled sentinel today")

	expected := "Scanning " + game.ModPath + " for untracked mods...\n" +
		"Found 1 files, 1 untracked\n" +
		"\n" +
		"Ready to import 1 mod(s):\n" +
		"  - LooseMod (local, v1.0)\n" +
		"\n" +
		"Import these mods? [y/N]: "
	assert.Equal(t, expected, out)

	mods, mErr := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, mErr)
	assert.Empty(t, mods, "a declined confirm must leave nothing installed")
}

// TestRunImportScan_JSONOutputReturnsConfirmationRequired pins the
// non-interactive rule (v2 Phase 3 Ruling 2) at runImportScan's "Import
// these mods?" prompt: under --json with no --force, it must fail with
// core.ErrConfirmationRequired before ever reading stdin, and nothing gets
// installed.
func TestRunImportScan_JSONOutputReturnsConfirmationRequired(t *testing.T) {
	svc, game := setupDoImportTest(t)
	game.DeployMode = domain.DeployCopy
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "LooseMod-1.0.zip"), []byte("loose-payload"), 0644))

	importSkipMatch = true
	importForce = false
	importDryRun = false
	withJSONOutput(t)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := assertStdinNeverRead(t, func() error {
		return runImportScan(cmd, game, svc, "default")
	})

	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	mods, mErr := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, mErr)
	assert.Empty(t, mods, "must leave nothing installed")
}

// TestRunImportScan_Force_SkipsConfirmPrompt_ImportsAndWritesCache pins
// --force's behavior for the twin case: the confirm prompt line never
// prints, the mod is actually imported, and (copy-mode) its cache entry is
// written with the source file's own bytes.
func TestRunImportScan_Force_SkipsConfirmPrompt_ImportsAndWritesCache(t *testing.T) {
	svc, game := setupDoImportTest(t)
	game.DeployMode = domain.DeployCopy
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "LooseMod-1.0.zip"), []byte("loose-payload"), 0644))

	importSkipMatch = true
	importForce = true
	importDryRun = false

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out, _, err := captureStdoutAndStderr(t, func() error {
		return runImportScan(cmd, game, svc, "default")
	})

	require.NoError(t, err)
	expected := "Scanning " + game.ModPath + " for untracked mods...\n" +
		"Found 1 files, 1 untracked\n" +
		"\n" +
		"Ready to import 1 mod(s):\n" +
		"  - LooseMod (local, v1.0)\n" +
		"  ✓ LooseMod\n" +
		"\n" +
		"Imported: 1, Skipped: 0, Failed: 0\n"
	assert.Equal(t, expected, out, "--force must skip the confirm prompt entirely")

	mods, mErr := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1)
	installed := mods[0]
	assert.Equal(t, domain.SourceLocal, installed.SourceID)
	assert.True(t, installed.ManualDownload, "scanned imports always require manual download")
	assert.Empty(t, installed.FileIDs, "no source match means no resolved file ID")

	cachePath := svc.GlobalCacheDir()
	cachedFile := filepath.Join(cachePath, "g1", "local-"+installed.ID, "1.0", "LooseMod-1.0.zip")
	data, rErr := os.ReadFile(cachedFile)
	require.NoError(t, rErr, "copy-mode import must write the source file into the cache")
	assert.Equal(t, "loose-payload", string(data))
}

// --- archive mode ---

// TestDoImport_EnrichmentVersionChange_RenamesCacheEntryOnDisk asserts the
// filesystem end state of the enrichment-triggered cache rename (import.go's
// needsCacheRename block): the pre-enrichment ("unknown"-version) cache
// directory is gone and the post-enrichment (real-version) directory holds
// the extracted content, not just that the marker/FileIDs end up right
// (already pinned by TestDoImport_ArchiveWithID_ResolvesFileIDAndStampsMarker).
func TestDoImport_EnrichmentVersionChange_RenamesCacheEntryOnDisk(t *testing.T) {
	svc, game := setupDoImportTest(t)
	src := newFakeMatchSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createTestArchive(t, archivePath, map[string]string{"mymod.esp": "data"})

	importModID = "999"

	_, err := captureStdoutErr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})
	require.NoError(t, err)

	gameCache := svc.GetGameCache(game)
	oldPath := gameCache.ModPath("g1", "acme-source", "999", "unknown")
	newPath := gameCache.ModPath("g1", "acme-source", "999", "2.0")

	_, statErr := os.Stat(oldPath)
	assert.True(t, os.IsNotExist(statErr), "the pre-enrichment cache directory must be gone after the rename")

	data, rErr := os.ReadFile(filepath.Join(newPath, "mymod.esp"))
	require.NoError(t, rErr, "the post-enrichment cache directory must hold the extracted content")
	assert.Equal(t, "data", string(data))
}

// setupImportConflictTest builds two fake-source-linked mods, "First Mod"
// (ID A1) and its archive, both resolved through the same sole configured
// source so every printed ID is deterministic (no uuid.New() in the output) -
// archive A is already imported+deployed by the time this returns, owning
// "shared.txt" in the profile's file-conflict table.
func setupImportConflictTest(t *testing.T) (svc *core.Service, game *domain.Game, archiveBPath string) {
	t.Helper()
	svc, game = setupDoImportTest(t)
	src := newFakeMatchSource("acme-source")
	src.mods["A1"] = &domain.Mod{ID: "A1", SourceID: "acme-source", Name: "First Mod", Version: "1.0", GameID: "g1"}
	src.mods["B1"] = &domain.Mod{ID: "B1", SourceID: "acme-source", Name: "Second Mod", Version: "1.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archiveA := filepath.Join(t.TempDir(), "first.zip")
	createTestArchive(t, archiveA, map[string]string{"shared.txt": "from-A"})
	importModID = "A1"
	importForce = true // mod A's own import is unrelated to the conflict this fixture sets up
	_, err := captureStdoutErr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archiveA})
	})
	require.NoError(t, err)

	archiveBPath = filepath.Join(t.TempDir(), "second.zip")
	createTestArchive(t, archiveBPath, map[string]string{"shared.txt": "from-B"})
	importModID = "B1"

	return svc, game, archiveBPath
}

// expectedConflictBlock is the exact "⚠ File conflicts detected" block both
// the decline and accept tests share, up to and including the prompt.
//
// RULING 18 (#314) retired this file's `importReadoutRerunLines` constant.
// Under Ruling 7 an accepted conflict re-ran ImportArchive, so the import
// readout printed a SECOND time between the prompt and the deploy line -
// exactly these bytes:
//
//	"\nFetching metadata from acme-source...\n" +
//	"\nMod: Second Mod\n" +
//	"  Source: acme-source\n" +
//	"  ID: B1\n" +
//	"  Version: 1.0\n" +
//	"  Files: 1\n"
//
// doImport now plans once, prompts from plan.Conflicts and applies once, so
// the readout prints once per user-level import and the ID it printed is the
// ID persisted. The accept test below is re-pinned without those lines.
func expectedConflictBlockAndPromptFor(archiveBPath string) string {
	return "Importing: " + archiveBPath + "\n" +
		"\nFetching metadata from acme-source...\n" +
		"\nMod: Second Mod\n" +
		"  Source: acme-source\n" +
		"  ID: B1\n" +
		"  Version: 1.0\n" +
		"  Files: 1\n" +
		"\n⚠ File conflicts detected:\n" +
		"  From First Mod (A1):\n" +
		"    - shared.txt\n" +
		"\n1 file(s) will be overwritten. Continue? [y/N]: "
}

// TestDoImport_ConflictPromptDecline_CancelsWithoutOverwriting pins the
// decline path: the conflict block prints, "n" returns the plain cancelled
// error, and - critically - mod A's deployed file is left untouched.
func TestDoImport_ConflictPromptDecline_CancelsWithoutOverwriting(t *testing.T) {
	svc, game, archiveBPath := setupImportConflictTest(t)
	importForce = false

	var out, errOut string
	var err error
	withStdin(t, "n\n", func() {
		out, errOut, err = captureStdoutAndStderr(t, func() error {
			return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archiveBPath})
		})
	})

	require.Error(t, err)
	assert.Equal(t, "import cancelled", err.Error())
	assert.False(t, errors.Is(err, ErrCancelled))
	assert.Equal(t, expectedConflictBlockAndPromptFor(archiveBPath), out)
	assert.Empty(t, errOut)

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1, "a declined conflict must not install the second mod")
	assert.Equal(t, "A1", mods[0].ID)

	data, rErr := os.ReadFile(filepath.Join(game.ModPath, "shared.txt"))
	require.NoError(t, rErr)
	assert.Equal(t, "from-A", string(data), "a declined conflict must not overwrite the already-deployed file")
}

// TestDoImport_ConflictPromptAccept_OverwritesAndInstalls is the accept
// twin: "y" proceeds through hooks/deploy/save, mod B ends up installed, and
// the shared file's content is now B's.
//
// Ruling 18 delta (#314): accepting no longer re-runs anything. doImport
// plans once, prompts from plan.Conflicts, and calls ApplyImportArchive with
// AcceptConflicts set - so the readout that printed twice under Ruling 7
// prints once, and "Deploying to game directory..." follows the prompt
// directly.
func TestDoImport_ConflictPromptAccept_OverwritesAndInstalls(t *testing.T) {
	svc, game, archiveBPath := setupImportConflictTest(t)
	importForce = false

	var out, errOut string
	var err error
	withStdin(t, "y\n", func() {
		out, errOut, err = captureStdoutAndStderr(t, func() error {
			return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archiveBPath})
		})
	})

	require.NoError(t, err)
	expected := expectedConflictBlockAndPromptFor(archiveBPath) +
		"\nDeploying to game directory...\n" +
		"\n✓ Imported: Second Mod\n" +
		"  Files deployed: 1\n" +
		"  Added to profile: default\n"
	assert.Equal(t, expected, out)
	assert.Empty(t, errOut)

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 2, "an accepted conflict must install the second mod alongside the first")

	data, rErr := os.ReadFile(filepath.Join(game.ModPath, "shared.txt"))
	require.NoError(t, rErr)
	assert.Equal(t, "from-B", string(data), "an accepted conflict must overwrite the file with the new mod's content")
}

// TestDoImport_ForceSkipsConflictPromptEvenWithRealConflict pins --force's
// archive-mode behavior: the conflict CHECK itself is skipped (not just the
// prompt) - no "⚠" text ever prints - and the overwrite happens silently.
func TestDoImport_ForceSkipsConflictPromptEvenWithRealConflict(t *testing.T) {
	svc, game, archiveBPath := setupImportConflictTest(t)
	importForce = true

	out, errOut, err := captureStdoutAndStderr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archiveBPath})
	})

	require.NoError(t, err)
	expected := "Importing: " + archiveBPath + "\n" +
		"\nFetching metadata from acme-source...\n" +
		"\nMod: Second Mod\n" +
		"  Source: acme-source\n" +
		"  ID: B1\n" +
		"  Version: 1.0\n" +
		"  Files: 1\n" +
		"\nDeploying to game directory...\n" +
		"\n✓ Imported: Second Mod\n" +
		"  Files deployed: 1\n" +
		"  Added to profile: default\n"
	assert.Equal(t, expected, out)
	assert.Empty(t, errOut)
	assert.NotContains(t, out, "⚠", "--force must skip the conflict check entirely, not just the prompt")

	data, rErr := os.ReadFile(filepath.Join(game.ModPath, "shared.txt"))
	require.NoError(t, rErr)
	assert.Equal(t, "from-B", string(data))
}

// setupImportHookTest builds a single fake-source-linked mod ("Hook Mod",
// ID H1) with a deterministic archive, for exercising the install.* hook
// sequence doImport's archive tail runs. importForce defaults true; the
// before-force test overrides it.
func setupImportHookTest(t *testing.T) (svc *core.Service, game *domain.Game, archivePath string) {
	t.Helper()
	svc, game = setupDoImportTest(t)
	src := newFakeMatchSource("acme-source")
	src.mods["H1"] = &domain.Mod{ID: "H1", SourceID: "acme-source", Name: "Hook Mod", Version: "1.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath = filepath.Join(t.TempDir(), "hookmod.zip")
	createTestArchive(t, archivePath, map[string]string{"file.txt": "hi"})
	importModID = "H1"

	return svc, game, archivePath
}

func writeFailingHookScript(t *testing.T) string {
	t.Helper()
	scriptsDir := t.TempDir()
	script := filepath.Join(scriptsDir, "hook.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/bash\nexit 1\n"), 0755))
	return script
}

// TestDoImport_HookWarnings_AfterEachAfterAll_AccumulateAndPrintToStderr
// pins the non-fatal after_each/after_all hook path: both failures are
// accumulated (not force-gated - after_each/after_all never abort) and
// printed to stderr together, in call order, while the import itself still
// succeeds and is saved.
func TestDoImport_HookWarnings_AfterEachAfterAll_AccumulateAndPrintToStderr(t *testing.T) {
	svc, game, archivePath := setupImportHookTest(t)
	failScript := writeFailingHookScript(t)
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{AfterEach: failScript, AfterAll: failScript}}
	importForce = true

	out, errOut, err := captureStdoutAndStderr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})

	require.NoError(t, err, "after_each/after_all hook failures must never abort the import")
	expected := "Importing: " + archivePath + "\n" +
		"\nFetching metadata from acme-source...\n" +
		"\nMod: Hook Mod\n" +
		"  Source: acme-source\n" +
		"  ID: H1\n" +
		"  Version: 1.0\n" +
		"  Files: 1\n" +
		"\nDeploying to game directory...\n" +
		"\n✓ Imported: Hook Mod\n" +
		"  Files deployed: 1\n" +
		"  Added to profile: default\n"
	assert.Equal(t, expected, out)

	expectedStderr := "Warning: install.after_each hook failed: hook failed with exit code 1: " + failScript + "\n" +
		"Warning: install.after_all hook failed: hook failed with exit code 1: " + failScript + "\n"
	assert.Equal(t, expectedStderr, errOut)

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1, "a non-fatal hook warning must not block the install from being saved")
}

// TestDoImport_BeforeAllHookFails_Forced_WarnsToStderrAndContinues pins the
// forced before_all path: the hook failure becomes a stderr warning (not a
// fatal error) and the import proceeds to completion.
func TestDoImport_BeforeAllHookFails_Forced_WarnsToStderrAndContinues(t *testing.T) {
	svc, game, archivePath := setupImportHookTest(t)
	failScript := writeFailingHookScript(t)
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeAll: failScript}}
	importForce = true

	out, errOut, err := captureStdoutAndStderr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})

	require.NoError(t, err)
	expected := "Importing: " + archivePath + "\n" +
		"\nFetching metadata from acme-source...\n" +
		"\nMod: Hook Mod\n" +
		"  Source: acme-source\n" +
		"  ID: H1\n" +
		"  Version: 1.0\n" +
		"  Files: 1\n" +
		"\nDeploying to game directory...\n" +
		"\n✓ Imported: Hook Mod\n" +
		"  Files deployed: 1\n" +
		"  Added to profile: default\n"
	assert.Equal(t, expected, out)
	assert.Equal(t, "Warning: install.before_all hook failed (forced): hook failed with exit code 1: "+failScript+"\n", errOut)

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1)
}

// TestDoImport_BeforeAllHookFails_AbortsWithoutForce_NothingInstalled is the
// non-forced twin: the whole import aborts before deploy/save, but the
// cache write from Import() (which runs BEFORE hooks) is left behind - a
// real, worth-pinning asymmetry between the cache and the DB/profile state
// on this failure path.
func TestDoImport_BeforeAllHookFails_AbortsWithoutForce_NothingInstalled(t *testing.T) {
	svc, game, archivePath := setupImportHookTest(t)
	failScript := writeFailingHookScript(t)
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeAll: failScript}}
	importForce = false

	out, errOut, err := captureStdoutAndStderr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})

	require.Error(t, err)
	assert.Equal(t, "install.before_all hook failed: hook failed with exit code 1: "+failScript, err.Error())
	expected := "Importing: " + archivePath + "\n" +
		"\nFetching metadata from acme-source...\n" +
		"\nMod: Hook Mod\n" +
		"  Source: acme-source\n" +
		"  ID: H1\n" +
		"  Version: 1.0\n" +
		"  Files: 1\n"
	assert.Equal(t, expected, out)
	assert.Empty(t, errOut, "a fatal (non-forced) before_all failure returns its own error - nothing is printed to stderr for it")

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	assert.Empty(t, mods, "a fatal before_all hook must leave nothing installed")

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists("g1", "acme-source", "H1", "1.0"),
		"Import()'s cache write happens before hooks run, so it survives a later fatal hook abort")
}

// TestDoImport_VerboseCacheRenameFailure_CascadesToDeploymentFailure pins a
// genuine cascade this characterization surfaced: when the enrichment
// version-change rename fails (here, because something already occupies the
// destination path as a non-directory), the only symptom under a NON-verbose
// run would be a completely unexplained "deployment failed: mod not in
// cache" - -v is what surfaces the real cause via two separate warnings.
// The os.Rename error text itself is OS-dependent, so only its fixed
// "Warning: could not rename cache entry: " prefix is pinned exactly; the
// deterministic "mod not in cache" text (both the conflict-check warning and
// the final error) is pinned exactly.
//
// #314 review I1 (recorded plain-text delta, Ruling 18): since the readout
// now renders from the PLAN before ApplyImportArchive runs at all, this
// warning - raised inside Apply's rename step - moved from BEFORE the
// readout to AFTER it: old order Fetching metadata.../Warning: could not
// rename.../Mod: .../Warning: could not check conflicts..., new order
// Fetching metadata.../Mod: .../Warning: could not rename.../Warning: could
// not check conflicts.... The order-blind assert.Contains this test used to
// carry could not have caught that reordering, so it is pinned here with
// index comparisons instead.
func TestDoImport_VerboseCacheRenameFailure_CascadesToDeploymentFailure(t *testing.T) {
	svc, game := setupDoImportTest(t)
	verbose = true
	src := newFakeMatchSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createTestArchive(t, archivePath, map[string]string{"mymod.esp": "data"})
	importModID = "999"
	importForce = false

	gameCache := svc.GetGameCache(game)
	oldPath := gameCache.ModPath("g1", "acme-source", "999", "unknown")
	newPath := gameCache.ModPath("g1", "acme-source", "999", "2.0")
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0755))
	require.NoError(t, os.WriteFile(newPath, []byte("blocker"), 0644)) // a FILE, not a dir: os.Rename(dir, this) fails

	out, _, err := captureStdoutAndStderr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})

	require.Error(t, err)
	assert.Equal(t, "deployment failed: mod not in cache: acme-source/999@2.0", err.Error())

	modIdx := strings.Index(out, "Mod: Acme Mod")
	renameIdx := strings.Index(out, "Warning: could not rename cache entry: ")
	conflictIdx := strings.Index(out, "Warning: could not check conflicts: mod not in cache: acme-source/999@2.0\n")
	deployIdx := strings.Index(out, "\nDeploying to game directory...\n")
	require.NotEqual(t, -1, modIdx, "the readout must print")
	require.NotEqual(t, -1, renameIdx, "the -v rename-failure warning must print")
	require.NotEqual(t, -1, conflictIdx,
		"the -v conflict-check-failure warning must also print, since the rename failure leaves the enriched version uncached")
	require.NotEqual(t, -1, deployIdx, "the deploy announcement must print before Install's own failure")

	assert.True(t, modIdx < renameIdx,
		"#314 review I1 (Ruling 18): the readout, rendered from the plan before Apply runs, must print before Apply's own rename-failure warning")
	assert.True(t, renameIdx < conflictIdx, "the rename-failure warning must print before the conflict-check warning")
	assert.True(t, conflictIdx < deployIdx, "the conflict-check warning must print before the deploy announcement")

	data, rErr := os.ReadFile(filepath.Join(oldPath, "mymod.esp"))
	require.NoError(t, rErr, "a failed rename must leave the original cache directory - and its content - untouched")
	assert.Equal(t, "data", string(data))

	blocker, rErr := os.ReadFile(newPath)
	require.NoError(t, rErr, "a failed rename must leave the blocking path untouched too")
	assert.Equal(t, "blocker", string(blocker))

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	assert.Empty(t, mods, "Install failing must leave nothing installed")
}

// TestDoImport_DeployCompile_ConvertiblePak_PrintsFilesDeployedLine pins the
// archive-mode DeployCompile branch import_compile_test.go's existing
// exmodz test doesn't cover: a convertible raw ".pak" (game.ConvertPaks
// true) deploys a real file of its own at Install() time, so the summary
// must say "Files deployed: 1", never the zero-file "Installed (merged pak
// updated)" line that's specific to a validate+retain-only exmodz import -
// even though doImport's own SyncMergedPak call, which always runs right
// after, immediately converts that raw deploy into the merged pak (#221's
// reconcilePakManifests), so the file actually left on disk under ModPath is
// the merged pak, not the raw "Raw_Weapon.pak" the summary line counted.
func TestDoImport_DeployCompile_ConvertiblePak_PrintsFilesDeployedLine(t *testing.T) {
	svc, game, _ := setupDoImportCompileTest(t)
	game.ConvertPaks = true

	archivePath := filepath.Join(t.TempDir(), "Raw_Weapon.pak")
	require.NoError(t, os.WriteFile(archivePath, []byte("raw-pak-bytes"), 0644))

	out, err := captureStdoutErr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})
	require.NoError(t, err)
	assert.Contains(t, out, "  Files deployed: 1\n")
	assert.NotContains(t, out, "Installed (merged pak updated)",
		"a convertible pak deploys a real file - it must not use the zero-file exmodz wording")

	prof, pErr := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, pErr)
	require.Len(t, prof.Mods, 1)
	assert.Contains(t, prof.Mods[0].FileIDs, "Raw_Weapon.pak",
		"the retained source's archive-filename identity must be folded into FileIDs (#197 C1) for a convertible pak too")

	_, statErr := os.Stat(filepath.Join(game.ModPath, "Raw_Weapon.pak"))
	assert.True(t, os.IsNotExist(statErr),
		"the raw per-mod deploy must be gone - SyncMergedPak's reconciliation converts it and undeploys the raw link")

	merged, rErr := os.ReadFile(filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak"))
	require.NoError(t, rErr, "the imported pak's content must end up in the merged pak SyncMergedPak deploys instead")
	assert.Equal(t, "raw-pak-bytes", string(merged))
}

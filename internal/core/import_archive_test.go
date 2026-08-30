package core_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for ImportArchive - the core twin of the CLI's `lmm import
// <archive>` mode (v2 Phase 2 Unit K Task 19, #291). cmd/lmm's
// TestDoImport_* characterization tests keep pinning the printed lines;
// these pin the end state (cache tree, DB row, profile ref), the returned
// result, the conflict callback's contract and the event stream every
// printed line is rendered from.
//
// Fixtures reuse adopt_test.go's adoptTestSource (same package) for the
// --id metadata path, and service_import_compile_test.go's
// newImportCompileTestGame for the DeployCompile branch.

// newImportArchiveTestService builds a service plus an EXTRACT-mode game
// (DeployMode's zero value, matching cmd's setupDoImportTest fixture) with
// no profile.yaml yet - ImportArchive creates the profile itself, exactly
// as the pre-lift doImport tail did.
func newImportArchiveTestService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

// importArchiveLines renders a recorded event stream as "<phase>|<detail>"
// strings, in order, so a test can pin the exact sequence a byte-identical
// frontend renders from.
func importArchiveLines(events []core.Event) []string {
	var out []string
	for _, e := range events {
		switch ev := e.(type) {
		case core.StepEvent:
			out = append(out, fmt.Sprintf("%s|%s", ev.Phase, ev.Detail))
		case core.HookEvent:
			out = append(out, fmt.Sprintf("%s|%s", ev.Phase, ev.Detail))
		}
	}
	return out
}

// --- the readout every archive import emits ---

// TestImportArchive_LocalArchive_EmitsTheReadoutAndInstalls pins the plain,
// sourceless path: the mod-detection readout and the deploy notice are the
// whole event stream, and the mod ends up installed, deployed and in the
// profile.
func TestImportArchive_LocalArchive_EmitsTheReadoutAndInstalls(t *testing.T) {
	svc, game := newImportArchiveTestService(t)

	archivePath := filepath.Join(t.TempDir(), "MyMod-1.2.zip")
	createImportTestZip(t, archivePath, map[string]string{"mymod.esp": "data"})

	sink, events := core.RecordEvents()
	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, sink)
	require.NoError(t, err)

	require.NotNil(t, result.Mod)
	assert.Equal(t, domain.SourceLocal, result.LinkedSource)
	assert.Equal(t, 1, result.Deployed)
	assert.False(t, result.Renamed)
	assert.Empty(t, result.FileID)
	assert.Empty(t, result.HookWarnings)
	assert.False(t, result.MergedPakSynced,
		"a non-DeployCompile game has no merged pak to sync (task-19 review Minor 1); syncMergedPak's no-op must not be reported as a sync")

	assert.Equal(t, []string{
		"import_archive_detected|Mod: MyMod-1.2",
		"import_archive_detail|Source: local",
		"import_archive_detail|ID: " + result.Mod.ID,
		"import_archive_detail|Version: 1.2",
		"import_archive_detail|Files: 1",
		"import_archive_deploying|Deploying to game directory...",
	}, importArchiveLines(*events))

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.True(t, mods[0].Deployed)
	assert.Equal(t, domain.UpdateNotify, mods[0].UpdatePolicy)

	prof, err := svc.NewProfileManager().Get("g1", "default")
	require.NoError(t, err)
	require.Len(t, prof.Mods, 1)

	_, statErr := os.Lstat(filepath.Join(game.ModPath, "mymod.esp"))
	assert.NoError(t, statErr, "the archive's content must be deployed into mod_path")
}

// --- --id enrichment (#139) ---

// TestImportArchive_WithID_ResolvesFileIDAndStampsMarker is the archive-mode
// happy path: --id enriches the mod, resolves the archive to the source's
// file by exact FileName, adopts its version, stamps the completion marker
// and records the FileIDs on the DB row and the profile ref.
func TestImportArchive_WithID_ResolvesFileIDAndStampsMarker(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1", Author: "Ace", SourceURL: "https://example.test/999"}
	src.files = []domain.DownloadableFile{
		{ID: "55", FileName: "mymod.zip", Version: "2.0", IsPrimary: true},
		{ID: "56", FileName: "other.zip", Version: "1.0"},
	}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createImportTestZip(t, archivePath, map[string]string{"mymod.esp": "data"})

	sink, events := core.RecordEvents()
	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "999", Force: true}, sink)
	require.NoError(t, err)

	assert.Equal(t, "55", result.FileID)
	assert.Equal(t, []string{"55"}, result.FileIDs)
	assert.True(t, result.Renamed, "adopting the resolved file's version renames the cache entry")

	assert.Equal(t, []string{
		"import_archive_fetching|Fetching metadata from acme-source...",
		"import_archive_detected|Mod: Acme Mod",
		"import_archive_detail|Source: acme-source",
		"import_archive_detail|ID: 999",
		"import_archive_detail|Version: 2.0",
		"import_archive_detail|Author: Ace",
		"import_archive_detail|URL: https://example.test/999",
		"import_archive_detail|Files: 1",
		"import_archive_deploying|Deploying to game directory...",
	}, importArchiveLines(*events))

	installed, err := svc.GetInstalledMod(context.Background(), "acme-source", "999", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"55"}, installed.FileIDs)

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.HasFileIDs("g1", "acme-source", "999", "2.0", []string{"55"}),
		"the import-written cache entry must carry the resolved file's completion marker")

	prof, err := svc.NewProfileManager().Get("g1", "default")
	require.NoError(t, err)
	require.Len(t, prof.Mods, 1)
	assert.Equal(t, []string{"55"}, prof.Mods[0].FileIDs)
}

// TestImportArchive_EnrichmentVersionChange_RenamesCacheEntryOnDisk pins the
// cache rename: the pre-enrichment directory is gone and the post-enrichment
// one holds the extracted content.
func TestImportArchive_EnrichmentVersionChange_RenamesCacheEntryOnDisk(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createImportTestZip(t, archivePath, map[string]string{"mymod.esp": "data"})

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "999", Force: true}, nil)
	require.NoError(t, err)
	assert.True(t, result.Renamed)

	gameCache := svc.GetGameCache(game)
	_, statErr := os.Stat(gameCache.ModPath("g1", "acme-source", "999", "unknown"))
	assert.True(t, os.IsNotExist(statErr), "the pre-enrichment cache directory must be gone after the rename")

	data, rErr := os.ReadFile(filepath.Join(gameCache.ModPath("g1", "acme-source", "999", "2.0"), "mymod.esp"))
	require.NoError(t, rErr)
	assert.Equal(t, "data", string(data))
}

// TestImportArchive_CacheRenameFailure_NotesAndCascadesToDeploymentFailure
// pins the cascade cmd's -v characterization surfaced: a blocked rename
// leaves the enriched version uncached, so the conflict check and then
// Install both fail on "mod not in cache". Both diagnostics are notes (the
// CLI gates them on --verbose); only the deploy failure is fatal.
func TestImportArchive_CacheRenameFailure_NotesAndCascadesToDeploymentFailure(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createImportTestZip(t, archivePath, map[string]string{"mymod.esp": "data"})

	gameCache := svc.GetGameCache(game)
	newPath := gameCache.ModPath("g1", "acme-source", "999", "2.0")
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0755))
	require.NoError(t, os.WriteFile(newPath, []byte("blocker"), 0644)) // a FILE: os.Rename(dir, this) fails

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "999", AcceptConflicts: true}, nil)
	require.Error(t, err)
	assert.Equal(t, "deployment failed: mod not in cache: acme-source/999@2.0", err.Error())
	assert.False(t, result.Renamed)
	require.GreaterOrEqual(t, len(result.Notes), 2)
	assert.Contains(t, result.Notes[0], "Warning: could not rename cache entry: ")
	assert.Equal(t, "Warning: could not check conflicts: mod not in cache: acme-source/999@2.0", result.Notes[1])

	data, rErr := os.ReadFile(filepath.Join(gameCache.ModPath("g1", "acme-source", "999", "unknown"), "mymod.esp"))
	require.NoError(t, rErr, "a failed rename must leave the original cache directory untouched")
	assert.Equal(t, "data", string(data))

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	assert.Empty(t, mods, "Install failing must leave nothing installed")
}

// TestImportArchive_FileListingFails_DegradesToMarkerless pins guardrail 1:
// a failed GetModFiles is a warning, never a failure - the import lands
// marker-less with empty FileIDs.
func TestImportArchive_FileListingFails_DegradesToMarkerless(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	src.filesErr = errors.New("source offline")
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createImportTestZip(t, archivePath, map[string]string{"mymod.esp": "data"})

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "999", Force: true}, nil)
	require.NoError(t, err, "a failed source file listing must not fail the import")
	assert.Empty(t, result.FileID)
	assert.Contains(t, result.Warnings, "could not resolve source file for archive: listing source files: source offline")

	installed, err := svc.GetInstalledMod(context.Background(), "acme-source", "999", "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, installed.FileIDs)
}

// TestImportArchive_UnmappedSource_WarnsAndSkipsMetadataFetch pins the
// source-not-configured branch: a warning, no fetch, and the import still
// lands (as the archive's own identity).
func TestImportArchive_UnmappedSource_WarnsAndSkipsMetadataFetch(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	svc.RegisterSource(src)
	// game.SourceIDs deliberately left empty.

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createImportTestZip(t, archivePath, map[string]string{"mymod.esp": "data"})

	sink, events := core.RecordEvents()
	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "999", Force: true}, sink)
	require.NoError(t, err)
	assert.Equal(t, []string{"source acme-source is not configured for this game; skipping metadata fetch"}, result.Warnings)
	assert.NotContains(t, importArchiveLines(*events), "import_archive_fetching|Fetching metadata from acme-source...")
}

// TestImportArchive_MetadataFetchFails_WarnsAndKeepsLocalIdentity pins the
// GetMod failure branch: warned, non-fatal, no enrichment.
func TestImportArchive_MetadataFetchFails_WarnsAndKeepsLocalIdentity(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.modErr = errors.New("source offline")
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createImportTestZip(t, archivePath, map[string]string{"mymod.esp": "data"})

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "999", Force: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"could not fetch metadata: source offline"}, result.Warnings)
	assert.Equal(t, "mymod", result.Mod.Name, "a failed fetch keeps the archive's own detected name")
}

// --- conflicts (Ruling 1: *ConflictError + AcceptConflicts, no callback) ---

// setupImportArchiveConflict imports mod A (owning "shared.txt") and returns
// a second archive whose deploy would overwrite it.
func setupImportArchiveConflict(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["A1"] = &domain.Mod{ID: "A1", SourceID: "acme-source", Name: "First Mod", Version: "1.0", GameID: "g1"}
	src.mods["B1"] = &domain.Mod{ID: "B1", SourceID: "acme-source", Name: "Second Mod", Version: "1.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archiveA := filepath.Join(t.TempDir(), "first.zip")
	createImportTestZip(t, archiveA, map[string]string{"shared.txt": "from-A"})
	_, err := svc.ImportArchive(context.Background(), game, "default", archiveA,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "A1", Force: true}, nil)
	require.NoError(t, err)

	archiveB := filepath.Join(t.TempDir(), "second.zip")
	createImportTestZip(t, archiveB, map[string]string{"shared.txt": "from-B"})
	return svc, game, archiveB
}

// TestImportArchive_UnacceptedConflict_ReturnsConflictError pins Ruling 1's
// refusal path: core computes the conflicts itself and returns
// *core.ConflictError carrying them, leaving mod A's deployed file - and the
// whole managed state - untouched. The archive IS in the cache by then (a
// cache fill is not a mutation), which is what lets the frontend's re-run
// with AcceptConflicts find its conflict list already computable.
func TestImportArchive_UnacceptedConflict_ReturnsConflictError(t *testing.T) {
	svc, game, archiveB := setupImportArchiveConflict(t)

	_, err := svc.ImportArchive(context.Background(), game, "default", archiveB,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "B1"}, nil)
	require.Error(t, err)

	var conflictErr *core.ConflictError
	require.ErrorAs(t, err, &conflictErr, "the conflict must surface as a typed error, never a callback")
	assert.ErrorIs(t, err, domain.ErrFileConflict)
	require.Len(t, conflictErr.Conflicts, 1)
	assert.Equal(t, "shared.txt", conflictErr.Conflicts[0].RelativePath)
	assert.Equal(t, "A1", conflictErr.Conflicts[0].CurrentModID)

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1, "an unaccepted conflict must not install the second mod")

	data, rErr := os.ReadFile(filepath.Join(game.ModPath, "shared.txt"))
	require.NoError(t, rErr)
	assert.Equal(t, "from-A", string(data))
}

// TestImportArchive_AcceptConflicts_OverwritesAndInstalls is the accept twin:
// the frontend prompted, the user said yes, and the very same call re-runs
// with AcceptConflicts set.
func TestImportArchive_AcceptConflicts_OverwritesAndInstalls(t *testing.T) {
	svc, game, archiveB := setupImportArchiveConflict(t)

	_, err := svc.ImportArchive(context.Background(), game, "default", archiveB,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "B1"}, nil)
	require.ErrorAs(t, err, new(*core.ConflictError), "sanity: the first run must refuse")

	_, err = svc.ImportArchive(context.Background(), game, "default", archiveB,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "B1", AcceptConflicts: true}, nil)
	require.NoError(t, err)

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 2)

	data, rErr := os.ReadFile(filepath.Join(game.ModPath, "shared.txt"))
	require.NoError(t, rErr)
	assert.Equal(t, "from-B", string(data))
}

// TestImportArchive_Conflicts_DeclineThenAccept_RunsHooksExactlyOnce is the
// import twin of the ApplyInstall hook-count pin: the archive is cached and
// the conflicts computed BEFORE install.before_all/before_each, so a refused
// import runs no hook and the accept re-run runs each exactly once. One
// user-level import, one run of every hook.
func TestImportArchive_Conflicts_DeclineThenAccept_RunsHooksExactlyOnce(t *testing.T) {
	svc, game, archiveB := setupImportArchiveConflict(t)

	scriptsDir := t.TempDir()
	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeAll := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "install.before_all:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	beforeEach := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "install.before_each:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeAll: beforeAll, BeforeEach: beforeEach}}

	_, err := svc.ImportArchive(context.Background(), game, "default", archiveB,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "B1"}, nil)
	require.ErrorAs(t, err, new(*core.ConflictError))

	_, statErr := os.Stat(callLog)
	assert.True(t, os.IsNotExist(statErr),
		"a refused conflict must run NO hook at all - the conflict gate precedes install.before_all")

	_, err = svc.ImportArchive(context.Background(), game, "default", archiveB,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "B1", AcceptConflicts: true}, nil)
	require.NoError(t, err)

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "install.before_all:\ninstall.before_each:B1\n", string(logContent),
		"the accept re-run runs each hook exactly once (before_all with no mod identity, before_each with the imported mod's)")
}

// TestImportArchive_Force_SkipsTheConflictCheckEntirely pins that Force skips
// the CHECK, not just the refusal: a real conflict exists and the import
// still lands without a *ConflictError.
func TestImportArchive_Force_SkipsTheConflictCheckEntirely(t *testing.T) {
	svc, game, archiveB := setupImportArchiveConflict(t)

	_, err := svc.ImportArchive(context.Background(), game, "default", archiveB,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "B1", Force: true}, nil)
	require.NoError(t, err, "--force must skip the conflict check entirely, not just the refusal")

	data, rErr := os.ReadFile(filepath.Join(game.ModPath, "shared.txt"))
	require.NoError(t, rErr)
	assert.Equal(t, "from-B", string(data))
}

// --- hooks ---

func importArchiveHookGame(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	svc, game := newImportArchiveTestService(t)
	archivePath := filepath.Join(t.TempDir(), "hookmod.zip")
	createImportTestZip(t, archivePath, map[string]string{"file.txt": "hi"})
	return svc, game, archivePath
}

// TestImportArchive_AfterHooksFail_AccumulateInHookWarnings pins the
// non-fatal tail hooks: both failures are collected, in call order, and the
// import still succeeds. printHookWarnings' job is now the caller's.
func TestImportArchive_AfterHooksFail_AccumulateInHookWarnings(t *testing.T) {
	svc, game, archivePath := importArchiveHookGame(t)
	fail := createTestScript(t, t.TempDir(), "hook.sh", "#!/bin/bash\nexit 1\n")
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{AfterEach: fail, AfterAll: fail}}

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err, "after_each/after_all hook failures must never abort the import")
	assert.Equal(t, []string{
		"install.after_each hook failed: hook failed with exit code 1: " + fail,
		"install.after_all hook failed: hook failed with exit code 1: " + fail,
	}, result.HookWarnings)

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1)
}

// TestImportArchive_HookScope_BeforeAllAndAfterAllRunWithEmptyModScope pins
// a subtlety the pre-lift CLI's tail had only in the ORDER of its
// hookCtx.ModID assignments, with no test or comment marking it (task-19
// review Minor 2): install.before_all/after_all run with an EMPTY mod
// scope, while before_each/after_each see the imported mod's own ID - a
// later cleanup that hoisted the mod-scope assignment above before_all
// would silently widen what before_all/after_all's scripts can see, with
// no assertion here to fail.
func TestImportArchive_HookScope_BeforeAllAndAfterAllRunWithEmptyModScope(t *testing.T) {
	svc, game, archivePath := importArchiveHookGame(t)
	scriptsDir := t.TempDir()
	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "before_all:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "before_each:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
echo "after_each:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	afterAllScript := createTestScript(t, scriptsDir, "after_all.sh", `#!/bin/bash
echo "after_all:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{
		BeforeAll: beforeAllScript, BeforeEach: beforeEachScript,
		AfterEach: afterEachScript, AfterAll: afterAllScript,
	}}

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Mod)

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t,
		"before_all:\nbefore_each:"+result.Mod.ID+"\nafter_each:"+result.Mod.ID+"\nafter_all:\n",
		string(logContent))
}

// TestImportArchive_BeforeAllFails_Forced_WarnsAndContinues pins the forced
// before_all path: a warning event plus a Warnings entry, and the import
// runs to completion.
func TestImportArchive_BeforeAllFails_Forced_WarnsAndContinues(t *testing.T) {
	svc, game, archivePath := importArchiveHookGame(t)
	fail := createTestScript(t, t.TempDir(), "hook.sh", "#!/bin/bash\nexit 1\n")
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeAll: fail}}

	sink, events := core.RecordEvents()
	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, sink)
	require.NoError(t, err)

	msg := "install.before_all hook failed (forced): hook failed with exit code 1: " + fail
	assert.Equal(t, []string{msg}, result.Warnings)
	assert.Contains(t, importArchiveLines(*events), "install_before_all_forced|"+msg)

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1)
}

// TestImportArchive_BeforeAllFails_AbortsWithoutForce pins the fatal twin,
// including the asymmetry the characterization surfaced: Import's cache
// write happens before hooks run, so it survives the abort.
func TestImportArchive_BeforeAllFails_AbortsWithoutForce(t *testing.T) {
	svc, game, archivePath := importArchiveHookGame(t)
	fail := createTestScript(t, t.TempDir(), "hook.sh", "#!/bin/bash\nexit 1\n")
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeAll: fail}}

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{}, nil)
	require.Error(t, err)
	assert.Equal(t, "install.before_all hook failed: hook failed with exit code 1: "+fail, err.Error())
	assert.Empty(t, result.Warnings)

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	assert.Empty(t, mods, "a fatal before_all hook must leave nothing installed")

	require.NotNil(t, result.Mod)
	assert.True(t, svc.GetGameCache(game).Exists("g1", result.Mod.SourceID, result.Mod.ID, result.Mod.Version),
		"Import's cache write happens before hooks run, so it survives a later fatal hook abort")
}

// TestImportArchive_BeforeEachFails_AbortsWithoutForce pins the second
// fatal hook gate, which shares before_all's Force semantics.
func TestImportArchive_BeforeEachFails_AbortsWithoutForce(t *testing.T) {
	svc, game, archivePath := importArchiveHookGame(t)
	fail := createTestScript(t, t.TempDir(), "hook.sh", "#!/bin/bash\nexit 1\n")
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeEach: fail}}

	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{}, nil)
	require.Error(t, err)
	assert.Equal(t, "install.before_each hook failed: hook failed with exit code 1: "+fail, err.Error())

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	assert.Empty(t, mods)
}

// TestImportArchive_SkipHooks_RunsNothing pins --no-hooks: a before_all that
// would otherwise abort the import never runs at all.
func TestImportArchive_SkipHooks_RunsNothing(t *testing.T) {
	svc, game, archivePath := importArchiveHookGame(t)
	fail := createTestScript(t, t.TempDir(), "hook.sh", "#!/bin/bash\nexit 1\n")
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeAll: fail, AfterAll: fail}}

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SkipHooks: true}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.HookWarnings)
}

// --- DeployCompile (#197 C1) ---

// TestImportArchive_DeployCompile_FoldsRetainedFileIDIntoFileIDs pins the
// #197 C1 fix: an ".exmodz" import's retained source is keyed by the
// archive's own filename, which must reach the DB row and the profile ref
// or the mod is invisible to every future merge. It also proves the tail's
// merged-pak sync ran.
func TestImportArchive_DeployCompile_FoldsRetainedFileIDIntoFileIDs(t *testing.T) {
	svc, _, game := newImportCompileTestGame(t)

	archivePath := filepath.Join(t.TempDir(), "Bear_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-exmodz-bytes"), 0644))

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Deployed, "an exmodz import deploys zero files of its own")
	assert.Equal(t, []string{"Bear_Mount.exmodz"}, result.FileIDs)
	assert.True(t, result.MergedPakSynced)

	prof, pErr := svc.NewProfileManager().Get(game.ID, "default")
	require.NoError(t, pErr)
	require.Len(t, prof.Mods, 1)
	assert.Contains(t, prof.Mods[0].FileIDs, "Bear_Mount.exmodz")

	installed, iErr := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, iErr)
	require.Len(t, installed, 1)
	assert.Contains(t, installed[0].FileIDs, "Bear_Mount.exmodz")
}

// --- the refused conflict leaves no cache entry behind (unit P review, Important 3) ---

// importCacheEntries lists every cache VERSION directory under the game's
// cache root as "<source>-<modID>/<version>", sorted - the granularity a
// "one import, one entry" claim is made at. An unlinked import mints a fresh
// uuid per Import call, so counting entries is the only stable way to see
// the orphan a refused-then-accepted import used to leave behind.
func importCacheEntries(t *testing.T, svc *core.Service, game *domain.Game) []string {
	t.Helper()
	root := filepath.Join(svc.GetGameCachePath(game), game.ID)
	modKeys, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)

	var entries []string
	for _, mk := range modKeys {
		if !mk.IsDir() {
			continue
		}
		versions, vErr := os.ReadDir(filepath.Join(root, mk.Name()))
		require.NoError(t, vErr)
		for _, v := range versions {
			if v.IsDir() {
				entries = append(entries, mk.Name()+"/"+v.Name())
			}
		}
	}
	sort.Strings(entries)
	return entries
}

// setupUnlinkedImportConflict is setupImportArchiveConflict's unlinked twin:
// mod A is imported with --id (so it owns "shared.txt" deterministically),
// but the archive returned is imported with NO source or mod ID, so Import
// mints a fresh uuid for it on every call. That is the shape Important 3
// measures - a refused-then-accepted import used to leave the first call's
// whole cache entry orphaned under an identity nothing references.
func setupUnlinkedImportConflict(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	svc, game, _ := setupImportArchiveConflict(t)
	unlinked := filepath.Join(t.TempDir(), "Zeta-1.0.zip")
	createImportTestZip(t, unlinked, map[string]string{"shared.txt": "from-Z"})
	return svc, game, unlinked
}

// TestImportArchive_UnacceptedConflict_RemovesTheCacheEntryItCreated pins
// Important 3's refusal half: a *ConflictError leaves the cache and the DB
// exactly as it found them, so nothing this call created survives an answer
// the caller may never come back to give.
func TestImportArchive_UnacceptedConflict_RemovesTheCacheEntryItCreated(t *testing.T) {
	svc, game, unlinked := setupUnlinkedImportConflict(t)

	before := importCacheEntries(t, svc, game)
	modsBefore, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)

	_, err = svc.ImportArchive(context.Background(), game, "default", unlinked,
		core.ImportArchiveOptions{}, nil)
	require.ErrorAs(t, err, new(*core.ConflictError))

	assert.Equal(t, before, importCacheEntries(t, svc, game),
		"a refused conflict must leave no cache entry behind")

	modsAfter, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Len(t, modsAfter, len(modsBefore), "a refused conflict must write no DB row")
}

// TestImportArchive_ConflictRefusedThenAccepted_EnrichmentRenameSucceeds
// pins the one behaviour delta Important 3's fix carries (task-8 review
// Minor 4's other half). An --id import caches under the pre-enrich identity
// and renames the entry onto the resolved version; before the fix the
// refused pass had already populated the destination, so the accept re-run's
// rename failed with ENOTEMPTY - leaving `Renamed: false` on a json-tagged
// result field and, under -v, a
// "Warning: could not rename cache entry: ... file exists" note. With the
// refusal cleaning up after itself the rename has a clear destination again,
// so both wrong values are gone.
func TestImportArchive_ConflictRefusedThenAccepted_EnrichmentRenameSucceeds(t *testing.T) {
	svc, game, archiveB := setupImportArchiveConflict(t)

	_, err := svc.ImportArchive(context.Background(), game, "default", archiveB,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "B1"}, nil)
	require.ErrorAs(t, err, new(*core.ConflictError), "sanity: the first run must refuse")

	result, err := svc.ImportArchive(context.Background(), game, "default", archiveB,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "B1", AcceptConflicts: true}, nil)
	require.NoError(t, err)

	assert.True(t, result.Renamed, "the accept re-run's enrichment rename must succeed")
	assert.Empty(t, result.Notes, "and emit no rename-failure note for the -v readout to print")
}

// TestImportArchive_ConflictRefusedThenAccepted_LeavesExactlyOneCacheEntry
// is the accept half: after the frontend's re-run there is exactly ONE new
// cache entry for the import, and the identity persisted to the DB and the
// profile is that entry's - never the refused pass's discarded uuid.
func TestImportArchive_ConflictRefusedThenAccepted_LeavesExactlyOneCacheEntry(t *testing.T) {
	svc, game, unlinked := setupUnlinkedImportConflict(t)

	before := importCacheEntries(t, svc, game)

	_, err := svc.ImportArchive(context.Background(), game, "default", unlinked,
		core.ImportArchiveOptions{}, nil)
	require.ErrorAs(t, err, new(*core.ConflictError), "sanity: the first run must refuse")

	result, err := svc.ImportArchive(context.Background(), game, "default", unlinked,
		core.ImportArchiveOptions{AcceptConflicts: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Mod)

	after := importCacheEntries(t, svc, game)
	require.Len(t, after, len(before)+1, "one import must leave exactly one cache entry: %v", after)

	added := after[len(after)-1]
	for _, e := range after {
		if !slices.Contains(before, e) {
			added = e
		}
	}
	assert.Equal(t, result.Mod.SourceID+"-"+result.Mod.ID+"/"+result.Mod.Version, added,
		"the surviving entry must be the identity that was persisted")

	mods, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	var ids []string
	for _, m := range mods {
		ids = append(ids, m.ID)
	}
	assert.Contains(t, ids, result.Mod.ID)
	assert.Len(t, mods, 2, "the accepted import adds exactly one row alongside mod A")
}

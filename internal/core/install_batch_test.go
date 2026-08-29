package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- v2 Phase 2 Unit H Task 7: PlanInstallMany + ApplyInstall's batch branch ---
//
// One test per scenario cmd/lmm/testdata/install_batch_golden/ froze on the
// pre-lift tree (Task 6), asserted here at the CORE layer: the InstallResult,
// the DB/profile end state the golden's "## state" section records, and the
// RecordEvents sequence the CLI's rendering closure consumes. The cmd goldens
// stay the byte-level contract; these are the semantics behind them, so a
// future change breaks a readable core assertion before it breaks a golden.

// batchInstallSource serves an explicit per-mod file list (mockSource's own
// GetModFiles returns a single, version-less file, which can't express the
// per-mod file IDs/versions these scenarios need) and can fail GetModFiles
// for exactly one mod - the core-side equivalent of the golden test's
// perModFetchFailureSource.
type batchInstallSource struct {
	*mockSourceWithDownloads
	files       map[string][]domain.DownloadableFile // mod.ID -> served files
	getFilesErr map[string]error                     // mod.ID -> GetModFiles failure
}

func newBatchInstallSource(id string) *batchInstallSource {
	return &batchInstallSource{
		mockSourceWithDownloads: newMockSourceWithDownloads(id),
		files:                   make(map[string][]domain.DownloadableFile),
		getFilesErr:             make(map[string]error),
	}
}

func (s *batchInstallSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if err := s.getFilesErr[mod.ID]; err != nil {
		return nil, err
	}
	return s.files[mod.ID], nil
}

// addZipMod registers mod plus one downloadable file whose payload is a
// one-entry zip archive (relativePath -> content), keyed by the file's own
// ID (matching mockSourceWithDownloads' GetDownloadURL).
func (s *batchInstallSource) addZipMod(t *testing.T, mod *domain.Mod, file domain.DownloadableFile, relativePath, content string) {
	t.Helper()
	zipPath := createTestZip(t, t.TempDir(), map[string]string{relativePath: content})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	s.AddDownload(file.ID, zipContent)
	s.files[mod.ID] = []domain.DownloadableFile{file}
	s.AddMod(mod.GameID, mod)
}

// addRawMod registers mod plus one downloadable file served verbatim (no
// zip) - what a DeployCompile ".exmodz" ingest validates and retains.
func (s *batchInstallSource) addRawMod(mod *domain.Mod, file domain.DownloadableFile, content []byte) {
	s.AddDownload(file.ID, content)
	s.files[mod.ID] = []domain.DownloadableFile{file}
	s.AddMod(mod.GameID, mod)
}

// batchCompileSource is batchInstallSource plus source.MergeCompiler, so a
// DeployCompile game's ".exmodz" mods take the validate+retain ingest path
// and reach the end-of-batch merged-pak sync. mergeErr, when set, fails the
// merge itself.
type batchCompileSource struct {
	fakeMergeFormat
	*batchInstallSource
	mergeErr error
}

func (s *batchCompileSource) ValidateSource(sourceFilePath string) error {
	_, err := os.Stat(sourceFilePath)
	return err
}

func (s *batchCompileSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, []source.MergeFailure, error) {
	if s.mergeErr != nil {
		return nil, nil, s.mergeErr
	}
	var out []byte
	for _, src := range sources {
		data, err := os.ReadFile(src.SourcePath)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, data...)
	}
	return nil, nil, os.WriteFile(outputPath, out, 0o644)
}

var _ source.MergeCompiler = (*batchCompileSource)(nil)

// batchStateRow/batchStateRef mirror the golden's "## state" section field
// for field, so a core assertion and the cmd golden describe the same end
// state in the same terms.
type batchStateRow struct {
	SourceID, ID, Version string
	Enabled, Deployed     bool
	FileIDs               []string
}

type batchStateRef struct {
	SourceID, ModID, Version string
	FileIDs                  []string
	Locked                   bool
}

func batchInstalledRows(t *testing.T, svc *core.Service, gameID, profileName string) []batchStateRow {
	t.Helper()
	installed, err := svc.GetInstalledMods(context.Background(), gameID, profileName)
	require.NoError(t, err)
	rows := make([]batchStateRow, 0, len(installed))
	for _, m := range installed {
		rows = append(rows, batchStateRow{
			SourceID: m.SourceID, ID: m.ID, Version: m.Version,
			Enabled: m.Enabled, Deployed: m.Deployed, FileIDs: m.FileIDs,
		})
	}
	return rows
}

func batchProfileRefs(t *testing.T, svc *core.Service, gameID, profileName string) []batchStateRef {
	t.Helper()
	profile, err := svc.NewProfileManager().Get(gameID, profileName)
	require.NoError(t, err)
	refs := make([]batchStateRef, 0, len(profile.Mods))
	for _, r := range profile.Mods {
		refs = append(refs, batchStateRef{
			SourceID: r.SourceID, ModID: r.ModID, Version: r.Version,
			FileIDs: r.FileIDs, Locked: r.Locked,
		})
	}
	return refs
}

// newBatchInstallFixture returns a plain (non-compile) service/game plus a
// registered batchInstallSource, matching the golden fixtures' shape: game
// "g1", source "test-src", profile "default".
func newBatchInstallFixture(t *testing.T) (*core.Service, *domain.Game, *batchInstallSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	src := newBatchInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game, src
}

func batchModA(t *testing.T, src *batchInstallSource, content string) *domain.Mod {
	t.Helper()
	mod := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "1.0", Author: "Alice", GameID: "g1"}
	src.addZipMod(t, mod, domain.DownloadableFile{ID: "a-file", Name: "Main", FileName: "modA.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}, "modA.esp", content)
	return mod
}

func batchModB(t *testing.T, src *batchInstallSource, content string) *domain.Mod {
	t.Helper()
	mod := &domain.Mod{ID: "mod-b", SourceID: "test-src", Name: "Mod B", Version: "2.0", Author: "Bob", GameID: "g1"}
	src.addZipMod(t, mod, domain.DownloadableFile{ID: "b-file", Name: "Main", FileName: "modB.esp", IsPrimary: true, Category: "MAIN", Version: "2.0"}, "modB.esp", content)
	return mod
}

// --- scenario: two_fresh_mods ---

func TestService_PlanInstallMany_TwoFreshMods_PicksPrimaryFilePerModNoDependencies(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	modA := batchModA(t, src, "mod a content")
	modB := batchModB(t, src, "mod b content")

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA, modB}, false)
	require.NoError(t, err)

	require.Len(t, plan.Batch, 2)
	assert.Equal(t, "g1", plan.GameID)
	assert.Equal(t, "default", plan.Profile)
	assert.Empty(t, plan.Dependencies, "the multi-select path never expands dependencies")

	assert.Equal(t, "mod-a", plan.Batch[0].Mod.ID)
	require.NotNil(t, plan.Batch[0].File)
	assert.Equal(t, "a-file", plan.Batch[0].File.ID)
	assert.Equal(t, "1.0", plan.Batch[0].Version)
	assert.False(t, plan.Batch[0].Reinstall)
	assert.Nil(t, plan.Batch[0].Locked)
	assert.Empty(t, plan.Batch[0].FetchError)

	assert.Equal(t, "mod-b", plan.Batch[1].Mod.ID)
	require.NotNil(t, plan.Batch[1].File)
	assert.Equal(t, "b-file", plan.Batch[1].File.ID)
	assert.Equal(t, "2.0", plan.Batch[1].Version)
}

func TestService_ApplyInstall_Batch_TwoFreshMods(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	modA := batchModA(t, src, "mod a content")
	modB := batchModB(t, src, "mod b content")

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA, modB}, false)
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err)

	assert.Equal(t, []string{"Mod A", "Mod B"}, installedRefNames(result.Installed))
	assert.Empty(t, result.Failed)
	assert.Empty(t, result.Skipped)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)
	assert.False(t, result.MergedPakSyncFailed)

	assert.Equal(t, []batchStateRow{
		{SourceID: "test-src", ID: "mod-a", Version: "1.0", Enabled: true, Deployed: true, FileIDs: []string{"a-file"}},
		{SourceID: "test-src", ID: "mod-b", Version: "2.0", Enabled: true, Deployed: true, FileIDs: []string{"b-file"}},
	}, batchInstalledRows(t, svc, "g1", "default"))
	assert.Equal(t, []batchStateRef{
		{SourceID: "test-src", ModID: "mod-a", Version: "1.0", FileIDs: []string{"a-file"}},
		{SourceID: "test-src", ModID: "mod-b", Version: "2.0", FileIDs: []string{"b-file"}},
	}, batchProfileRefs(t, svc, "g1", "default"))

	phases, _ := phasesOf(*seen)
	assert.Equal(t, []core.DeployPhase{
		core.InstallDepInstalling, core.InstallDepFileSelected, core.InstallDepDownloading,
		core.InstallDepDownloadDone, core.InstallChecksumComputed, core.InstallDepInstalled,
		core.InstallDepInstalling, core.InstallDepFileSelected, core.InstallDepDownloading,
		core.InstallDepDownloadDone, core.InstallChecksumComputed, core.InstallDepInstalled,
	}, phases)
}

// --- scenario: reinstall_same_version_plus_fresh ---

func TestService_ApplyInstall_Batch_ReinstallSameVersionPlusFresh(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	modA := batchModA(t, src, "mod a v1 content")

	first, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA}, false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(context.Background(), game, first, core.InstallOptions{}, nil)
	require.NoError(t, err)

	modB := batchModB(t, src, "mod b content")
	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA, modB}, false)
	require.NoError(t, err)
	assert.True(t, plan.Batch[0].Reinstall, "mod-a is installed, so the batch uninstalls it first")
	assert.False(t, plan.Batch[1].Reinstall)

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err)

	assert.Equal(t, []string{"Mod A", "Mod B"}, installedRefNames(result.Installed))
	assert.Empty(t, result.Failed)
	assert.Empty(t, result.Notes, "a clean uninstall + cache delete produces no verbose-only note")

	assert.Equal(t, []batchStateRow{
		{SourceID: "test-src", ID: "mod-a", Version: "1.0", Enabled: true, Deployed: true, FileIDs: []string{"a-file"}},
		{SourceID: "test-src", ID: "mod-b", Version: "2.0", Enabled: true, Deployed: true, FileIDs: []string{"b-file"}},
	}, batchInstalledRows(t, svc, "g1", "default"), "the reinstall upserts mod-a's row, never duplicates it")

	phases, _ := phasesOf(*seen)
	assert.Equal(t, core.InstallDepReinstalling, phases[1], "the reinstall announcement precedes file selection")
}

// --- scenario: locked_ref_different_version_skipped ---

func TestService_ApplyInstall_Batch_LockedRefDifferentVersion_SkippedBeforeUninstall(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	mod := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "1.0", Author: "Alice", GameID: "g1"}
	src.addZipMod(t, mod, domain.DownloadableFile{ID: "f1", Name: "Main", FileName: "modA.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}, "modA.esp", "v1 content")

	first, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{mod}, false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(context.Background(), game, first, core.InstallOptions{}, nil)
	require.NoError(t, err)

	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "default", "test-src", "mod-a", ""))

	latest := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "2.0", Author: "Alice", GameID: "g1"}
	src.addZipMod(t, latest, domain.DownloadableFile{ID: "f2", Name: "Main", FileName: "modA.esp", IsPrimary: true, Category: "MAIN", Version: "2.0"}, "modA.esp", "v2 content")

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{latest}, false)
	require.NoError(t, err)
	require.NotNil(t, plan.Batch[0].Locked, "the plan records the locked ref it would refuse")
	assert.Equal(t, "1.0", plan.Batch[0].Locked.Version)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err, "a locked ref skips its own mod, it never fails the batch")

	assert.Empty(t, result.Installed)
	assert.Equal(t, []string{"Mod A"}, installedRefNames(result.Failed))
	lockedRef := &domain.ModReference{SourceID: "test-src", ModID: "mod-a", Version: "1.0", Locked: true}
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Mod A", result.Skipped[0].Name)
	assert.Equal(t, core.LockedRefRefusalError(*latest, "default", lockedRef).Error(), result.Skipped[0].Reason)

	assert.Equal(t, []batchStateRow{
		{SourceID: "test-src", ID: "mod-a", Version: "1.0", Enabled: true, Deployed: true, FileIDs: []string{"f1"}},
	}, batchInstalledRows(t, svc, "g1", "default"), "the deployed lock target must be untouched")
	assert.Equal(t, []batchStateRef{
		{SourceID: "test-src", ModID: "mod-a", Version: "1.0", FileIDs: []string{"f1"}, Locked: true},
	}, batchProfileRefs(t, svc, "g1", "default"))
}

// --- scenario: fetch_failure_second_mod ---

func TestService_ApplyInstall_Batch_FetchFailureSecondMod(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	modA := batchModA(t, src, "mod a content")
	modB := &domain.Mod{ID: "mod-b", SourceID: "test-src", Name: "Mod B", Version: "2.0", Author: "Bob", GameID: "g1"}
	src.AddMod(modB.GameID, modB)
	src.getFilesErr["mod-b"] = assert.AnError

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA, modB}, false)
	require.NoError(t, err, "one mod's fetch failure is recorded on its entry, never fatal to the plan")
	assert.Empty(t, plan.Batch[0].FetchError)
	assert.Equal(t, "failed to get mod files: "+assert.AnError.Error(), plan.Batch[1].FetchError)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"Mod A"}, installedRefNames(result.Installed))
	assert.Equal(t, []string{"Mod B"}, installedRefNames(result.Failed))
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Mod B", result.Skipped[0].Name)
	assert.Equal(t, "failed to get mod files: "+assert.AnError.Error(), result.Skipped[0].Reason)

	assert.Equal(t, []batchStateRow{
		{SourceID: "test-src", ID: "mod-a", Version: "1.0", Enabled: true, Deployed: true, FileIDs: []string{"a-file"}},
	}, batchInstalledRows(t, svc, "g1", "default"))
}

// --- scenario: file_conflict_warns_continues ---

func TestService_ApplyInstall_Batch_FileConflictWarnsAndContinues(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	modA := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "1.0", GameID: "g1"}
	modB := &domain.Mod{ID: "mod-b", SourceID: "test-src", Name: "Mod B", Version: "1.0", GameID: "g1"}
	src.addZipMod(t, modA, domain.DownloadableFile{ID: "a-file", Name: "Shared", FileName: "shared.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}, "shared.esp", "mod a content")
	src.addZipMod(t, modB, domain.DownloadableFile{ID: "b-file", Name: "Shared", FileName: "shared.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}, "shared.esp", "mod b content")

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA, modB}, false)
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err)

	assert.Equal(t, []string{"Mod A", "Mod B"}, installedRefNames(result.Installed), "a file conflict warns, it never blocks the batch")
	assert.Empty(t, result.Failed)

	var conflictWarnings []string
	for _, e := range *seen {
		if w, ok := e.(core.WarningEvent); ok && w.Phase == core.InstallDepConflictWarning {
			conflictWarnings = append(conflictWarnings, w.Message)
		}
	}
	assert.Equal(t, []string{"1 file conflict(s) - will overwrite"}, conflictWarnings)
}

// --- scenario: force_with_failing_before_all ---

func TestService_ApplyInstall_Batch_ForceWithFailingBeforeAll(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	modA := batchModA(t, src, "mod a content")
	modB := batchModB(t, src, "mod b content")

	scripts := t.TempDir()
	fail := createTestScript(t, scripts, "before_all.sh", "#!/bin/bash\necho boom >&2\nexit 1\n")
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeAll: fail}})

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA, modB}, false)
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{Force: true}, sink)
	require.NoError(t, err)

	assert.Equal(t, []string{"Mod A", "Mod B"}, installedRefNames(result.Installed))
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "install.before_all hook failed (forced): ")

	phases, _ := phasesOf(*seen)
	require.NotEmpty(t, phases)
	assert.Equal(t, core.InstallBeforeAllForced, phases[0], "the forced-hook warning precedes every mod")
}

func TestService_ApplyInstall_Batch_FailingBeforeAllWithoutForceAbortsBeforeAnyMod(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	modA := batchModA(t, src, "mod a content")

	scripts := t.TempDir()
	fail := createTestScript(t, scripts, "before_all.sh", "#!/bin/bash\nexit 1\n")
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeAll: fail}})

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA}, false)
	require.NoError(t, err)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install.before_all hook failed: ")
	assert.Empty(t, result.Installed)
	assert.Empty(t, batchInstalledRows(t, svc, "g1", "default"))
}

// --- scenario: failing_after_each ---

func TestService_ApplyInstall_Batch_FailingAfterEachWarnsPerModAndKeepsBothInstalled(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	modA := batchModA(t, src, "mod a content")
	modB := batchModB(t, src, "mod b content")

	scripts := t.TempDir()
	fail := createTestScript(t, scripts, "after_each.sh", "#!/bin/bash\necho boom >&2\nexit 1\n")
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{AfterEach: fail}})

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA, modB}, false)
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err, "an after_each failure is non-fatal, per mod")

	assert.Equal(t, []string{"Mod A", "Mod B"}, installedRefNames(result.Installed))
	require.Len(t, result.Warnings, 2)
	assert.Contains(t, result.Warnings[0], "install.after_each hook failed for mod-a: ")
	assert.Contains(t, result.Warnings[1], "install.after_each hook failed for mod-b: ")

	assert.Len(t, batchInstalledRows(t, svc, "g1", "default"), 2, "a failed after_each never removes its mod")

	phases, _ := phasesOf(*seen)
	require.Len(t, phases, 14)
	assert.Equal(t, []core.DeployPhase{core.InstallWarning, core.InstallWarning}, phases[12:],
		"both after_each warnings are deferred to the very end of the batch")
}

// --- scenario: deploy_compile_merged_pak (+ its sync-failure variant) ---

// newBatchCompileFixture builds a DeployCompile game whose registered source
// is a MergeCompiler, with a base pak already installed - the shape the
// golden's two ".exmodz" mods need.
func newBatchCompileFixture(t *testing.T, mergeErr error) (*core.Service, *domain.Game, *batchCompileSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	game := &domain.Game{
		ID: "g1", Name: "Game", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"test-src": "g1"},
	}
	inner := newBatchInstallSource("test-src")
	t.Cleanup(inner.Close)
	src := &batchCompileSource{batchInstallSource: inner, mergeErr: mergeErr}
	svc.RegisterSource(src)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game, src
}

func seedBatchCompileMods(src *batchCompileSource) []*domain.Mod {
	bear := &domain.Mod{ID: "bear-mount", SourceID: "test-src", Name: "Bear Mount", Version: "1.0", GameID: "g1"}
	wolf := &domain.Mod{ID: "wolf-mount", SourceID: "test-src", Name: "Wolf Mount", Version: "1.0", GameID: "g1"}
	src.addRawMod(bear, domain.DownloadableFile{ID: "bear-exmodz", Name: "Bear Mount", FileName: "Bear_Mount.exmodz", IsPrimary: true, Category: "MAIN"}, []byte("bear-bytes"))
	src.addRawMod(wolf, domain.DownloadableFile{ID: "wolf-exmodz", Name: "Wolf Mount", FileName: "Wolf_Mount.exmodz", IsPrimary: true, Category: "MAIN"}, []byte("wolf-bytes"))
	return []*domain.Mod{bear, wolf}
}

func TestService_ApplyInstall_Batch_DeployCompile_SyncsMergedPakUnconditionally(t *testing.T) {
	svc, game, src := newBatchCompileFixture(t, nil)
	mods := seedBatchCompileMods(src)

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", mods, false)
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err)

	assert.Equal(t, []string{"Bear Mount", "Wolf Mount"}, installedRefNames(result.Installed))
	assert.False(t, result.MergedPakSyncFailed)
	assert.Empty(t, result.Warnings)

	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.InstallDepInstalled {
			assert.Zero(t, m.FilesExtracted, "a retained .exmodz deploys zero files of its own")
		}
	}

	assert.Equal(t, []batchStateRow{
		{SourceID: "test-src", ID: "bear-mount", Version: "1.0", Enabled: true, Deployed: true, FileIDs: []string{"bear-exmodz"}},
		{SourceID: "test-src", ID: "wolf-mount", Version: "1.0", Enabled: true, Deployed: true, FileIDs: []string{"wolf-exmodz"}},
	}, batchInstalledRows(t, svc, "g1", "default"))
}

func TestService_ApplyInstall_Batch_DeployCompile_SyncFailureRecordedNotFatal(t *testing.T) {
	svc, game, src := newBatchCompileFixture(t, assert.AnError)
	mods := seedBatchCompileMods(src)

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", mods, false)
	require.NoError(t, err)

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err, "a merged-pak sync failure is loud, never fatal")

	assert.Equal(t, []string{"Bear Mount", "Wolf Mount"}, installedRefNames(result.Installed))
	assert.True(t, result.MergedPakSyncFailed)
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "syncing merged pak: ")

	var syncFailures []string
	for _, e := range *seen {
		if w, ok := e.(core.WarningEvent); ok && w.Phase == core.InstallMergedPakSyncFailed {
			syncFailures = append(syncFailures, w.Message)
		}
	}
	require.Len(t, syncFailures, 1, "the sync failure gets its own phase so each frontend words it")
	assert.NotContains(t, syncFailures[0], "syncing merged pak:", "the event carries the raw error; the frontend supplies the wording")
	assert.Contains(t, syncFailures[0], assert.AnError.Error())
}

// --- ruling 5: the plan records the installed set it was computed from ---

func TestService_ApplyInstall_Batch_StalePlanRefused(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	modA := batchModA(t, src, "mod a content")
	modB := batchModB(t, src, "mod b content")

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modB}, false)
	require.NoError(t, err)

	// Install mod-a behind the plan's back: the installed set it was
	// computed against no longer describes the profile.
	other, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA}, false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(context.Background(), game, other, core.InstallOptions{}, nil)
	require.NoError(t, err)

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
	assert.Len(t, batchInstalledRows(t, svc, "g1", "default"), 1, "a stale plan applies nothing")
}

// TestService_ApplyInstall_Strict_StalePlanRefused is
// TestService_ApplyInstall_Batch_StalePlanRefused's STRICT-path counterpart
// (review Minor M4): checkPlanFresh runs as applyInstall's first statement,
// before the plan.Batch branch (see ApplyInstall's doc comment), so it
// guards the single-mod/dependency path too - but only the batch path had a
// test proving it.
func TestService_ApplyInstall_Strict_StalePlanRefused(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "content 1")
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod2", SourceID: "src", Name: "Mod Two", Version: "1.0", GameID: "g1"}, "mod2.esp", "content 2")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)

	// Install an unrelated mod behind the plan's back: the installed set it
	// was computed against no longer describes the profile.
	other, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod2", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(context.Background(), game, other, core.InstallOptions{}, nil)
	require.NoError(t, err)

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)

	_, dbErr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	assert.Error(t, dbErr, "a stale plan applies nothing")
}

// --- Minor M4: PlanInstallMany's own zero-mutation test ---

// TestService_PlanInstallMany_PerformsZeroMutations is
// TestService_PlanInstall_PerformsZeroMutations' multi-mod counterpart
// (review Minor M4): PlanInstallMany does strictly more I/O per entry (a
// GetModFiles fetch AND a GetConflicts cache walk, see review Minor M2)
// than PlanInstall, so a future edit that slips a write into the loop
// deserves its own purity regression rather than relying on a golden's
// "## state" section to catch it. An unrelated pre-existing mod's DB row,
// its profile's YAML bytes, and the whole game cache directory tree must
// all be byte-for-byte unchanged after planning two fresh mods.
func TestService_PlanInstallMany_PerformsZeroMutations(t *testing.T) {
	svc, game, src := newBatchInstallFixture(t)
	modA := batchModA(t, src, "mod a content")
	modB := batchModB(t, src, "mod b content")

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	// Unrelated pre-existing state to prove untouched.
	seedInstalledMod(t, svc, game, "test-src", "existing", "1.0", true, map[string][]byte{"existing.esp": []byte("e")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "existing", SourceID: "test-src", Version: "1.0", GameID: "g1"}, "default"))

	beforeMods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	profilePath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	beforeProfileYAML, err := os.ReadFile(profilePath)
	require.NoError(t, err)

	beforeCache := cacheTreeSnapshot(t, svc.GetGameCachePath(game))

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{modA, modB}, false)
	require.NoError(t, err)
	require.NotNil(t, plan)

	afterMods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, beforeMods, afterMods, "DB rows must be untouched after planning")

	afterProfileYAML, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	assert.Equal(t, beforeProfileYAML, afterProfileYAML, "profile YAML must be byte-identical after planning")

	afterCache := cacheTreeSnapshot(t, svc.GetGameCachePath(game))
	assert.Equal(t, beforeCache, afterCache, "planning must not touch the cache directory tree")
}

// cacheTreeSnapshot walks dir and returns relative-path -> content for every
// regular file, so a before/after comparison catches ANY write - a new
// file, a modified one, or one removed - not just a top-level entry count.
func cacheTreeSnapshot(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	snap := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snap[rel] = content
		return nil
	})
	require.NoError(t, err)
	return snap
}

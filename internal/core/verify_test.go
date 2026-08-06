package core_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/require"
)

// newVerifyTestService is newFlowsTestService (flows_test.go) plus the
// backing dataDir - TestVerify_LocalWalk_StatusesAndCounts needs direct
// sqlite-file access (see orphanChecksumRow) that the shared helper
// doesn't expose.
func newVerifyTestService(t *testing.T) (*core.Service, string) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   dataDir,
		CacheDir:  t.TempDir(),
	}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc, dataDir
}

// orphanChecksumRow deletes the installed_mods row for source/mod/game/
// profile WITHOUT cascading into installed_mod_files, reproducing the
// out-of-band DB state doVerify's "Unknown mod X - SKIPPED" branch exists
// to handle (an orphaned checksum row referencing a mod no longer
// installed). Going through the Service/DB API always cascades - its
// connection runs with PRAGMA foreign_keys=ON (internal/storage/db/db.go)
// and installed_mod_files declares ON DELETE CASCADE on exactly this key
// (migrations.go) - so this opens a private connection to the same sqlite
// file with FK enforcement off, deletes only the parent row, and closes
// again; the child row it leaves behind is now genuinely orphaned for
// every later connection, including svc's own.
func orphanChecksumRow(t *testing.T, dataDir, sourceID, modID, gameID, profileName string) {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(dataDir, "lmm.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, raw.Close()) }()

	_, err = raw.Exec("PRAGMA foreign_keys = OFF")
	require.NoError(t, err)
	_, err = raw.Exec(`DELETE FROM installed_mods WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?`,
		sourceID, modID, gameID, profileName)
	require.NoError(t, err)
}

// seedVerifyMod installs sourceID/modID as an enabled mod with the given
// FileIDs and, when storeCache is true, stores each file in the game
// cache under version. Checksums are set separately (via
// svc.SaveFileChecksum) by the caller - the checksum column starts NULL
// after SaveInstalledMod's own FileIDs replacement.
func seedVerifyMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version string, fileIDs []string, storeCache bool) {
	t.Helper()

	if storeCache {
		gameCache := svc.GetGameCache(game)
		for _, fileID := range fileIDs {
			require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, fileID, []byte("content-"+fileID)))
		}
	}

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       modID,
			SourceID: sourceID,
			Name:     name,
			Version:  version,
			GameID:   game.ID,
		},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      fileIDs,
		UpdatePolicy: domain.UpdateNotify,
	}))
}

// TestVerify_LocalWalk_StatusesAndCounts is the #224 Task 2 fixture: one
// mod fully cached+checksummed (ok), one checksummed row whose cache
// version dir is absent (missing), one row with an empty checksum
// (no_checksum), and one orphaned checksum row for an uninstalled mod
// (skipped) - proving Verify's local per-file walk ports the CLI's
// counting rules identically.
func TestVerify_LocalWalk_StatusesAndCounts(t *testing.T) {
	svc, dataDir := newVerifyTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	// (a) fully cached + checksummed -> ok
	seedVerifyMod(t, svc, game, "src", "mod-ok", "Mod OK", "1.0", []string{"ok-file"}, true)
	require.NoError(t, svc.SaveFileChecksum("src", "mod-ok", game.ID, "default", "ok-file", "checksum-ok"))

	// (b) checksum row whose version dir is absent -> missing
	seedVerifyMod(t, svc, game, "src", "mod-missing", "Mod Missing", "1.0", []string{"missing-file"}, false)
	require.NoError(t, svc.SaveFileChecksum("src", "mod-missing", game.ID, "default", "missing-file", "checksum-missing"))

	// (c) row with empty checksum -> no_checksum
	seedVerifyMod(t, svc, game, "src", "mod-no-checksum", "Mod No Checksum", "1.0", []string{"no-checksum-file"}, true)

	// (d) checksum row for an uninstalled mod -> skipped.
	seedVerifyMod(t, svc, game, "src", "mod-gone", "Mod Gone", "1.0", []string{"gone-file"}, true)
	require.NoError(t, svc.SaveFileChecksum("src", "mod-gone", game.ID, "default", "gone-file", "checksum-gone"))
	orphanChecksumRow(t, dataDir, "src", "mod-gone", game.ID, "default")

	files, err := svc.GetFilesWithChecksums(game.ID, "default")
	require.NoError(t, err)
	require.Len(t, files, 4, "precondition: four checksum rows exist")

	var events []core.VerifyEvent
	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{}, func(e core.VerifyEvent) {
		events = append(events, e)
	})
	require.NoError(t, err)

	require.True(t, result.HasFiles)
	require.Equal(t, 1, result.Issues, "only mod-missing's MISSING row counts as an issue")
	require.Equal(t, 2, result.Warnings, "mod-no-checksum's NO CHECKSUM + mod-gone's SKIPPED count as warnings")
	require.Equal(t, len(files), result.Checked)
	require.Len(t, result.Findings, len(files), "no file_count_mismatch rows expected in this fixture")

	// Build the expected findings in the exact order GetFilesWithChecksums
	// returned them (this task's engine has no ORDER BY of its own - it
	// walks the rows in DB order, same as the CLI's own loop does today).
	byFileID := map[string]core.VerifyFinding{
		"ok-file":          {ModID: "mod-ok", ModName: "Mod OK", FileID: "ok-file", Status: "ok"},
		"missing-file":     {ModID: "mod-missing", ModName: "Mod Missing", FileID: "missing-file", Status: "missing"},
		"no-checksum-file": {ModID: "mod-no-checksum", ModName: "Mod No Checksum", FileID: "no-checksum-file", Status: "no_checksum"},
		"gone-file":        {ModID: "mod-gone", FileID: "gone-file", Status: "skipped"},
	}
	var wantFindings []core.VerifyFinding
	for _, f := range files {
		wantFindings = append(wantFindings, byFileID[f.FileID])
	}
	require.Equal(t, wantFindings, result.Findings)

	// One VerifyEvBegin{HasFiles:true} followed by one VerifyEvFinding per
	// row, with Version set on the missing row.
	require.Len(t, events, 1+len(files))
	require.Equal(t, core.VerifyEvBegin, events[0].Kind)
	require.True(t, events[0].HasFiles)
	for i, ev := range events[1:] {
		require.Equal(t, core.VerifyEvFinding, ev.Kind)
		require.Equal(t, wantFindings[i], ev.Finding)
		if wantFindings[i].Status == "missing" {
			require.Equal(t, "1.0", ev.Version)
		} else {
			require.Empty(t, ev.Version)
		}
	}
}

// TestVerify_EmptyProfile_HasFilesFalse proves the #217 empty-profile path:
// no checksummed files at all means an empty result and a Begin event
// carrying HasFiles=false. Task 2's brief is explicit that the
// convergence sweep for this path lands in a later task - until then
// Verify does nothing else here.
func TestVerify_EmptyProfile_HasFilesFalse(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	var events []core.VerifyEvent
	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{}, func(e core.VerifyEvent) {
		events = append(events, e)
	})
	require.NoError(t, err)

	require.False(t, result.HasFiles)
	require.Empty(t, result.Findings)
	require.Equal(t, 0, result.Issues)
	require.Equal(t, 0, result.Warnings)
	require.Equal(t, 0, result.Checked)

	require.Len(t, events, 1)
	require.Equal(t, core.VerifyEvBegin, events[0].Kind)
	require.False(t, events[0].HasFiles)
}

// TestVerify_ModFilter_LimitsRows proves ModFilter scopes both the
// file-count pre-pass and the per-file walk to a single mod's rows: the
// other mod's rows are absent from Findings, and Checked only counts the
// filtered-in rows.
func TestVerify_ModFilter_LimitsRows(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	seedVerifyMod(t, svc, game, "src", "mod-a", "Mod A", "1.0", []string{"a-file"}, true)
	require.NoError(t, svc.SaveFileChecksum("src", "mod-a", game.ID, "default", "a-file", "checksum-a"))

	seedVerifyMod(t, svc, game, "src", "mod-b", "Mod B", "1.0", []string{"b-file"}, true)
	require.NoError(t, svc.SaveFileChecksum("src", "mod-b", game.ID, "default", "b-file", "checksum-b"))

	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{ModFilter: "mod-a"}, nil)
	require.NoError(t, err)

	require.True(t, result.HasFiles, "HasFiles reflects the unfiltered fetch")
	require.Equal(t, 1, result.Checked)
	require.Equal(t, []core.VerifyFinding{
		{ModID: "mod-a", ModName: "Mod A", FileID: "a-file", Status: "ok"},
	}, result.Findings)
}

// trapGetModFilesSource fails the test outright if GetModFiles is ever
// called - TestVerify_LocalTier_NeverTouchesNetwork's proof that
// VerifyLocal never reaches the (network-touching) version pass.
type trapGetModFilesSource struct {
	*mockSource
	t *testing.T
}

func (s *trapGetModFilesSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	s.t.Fatal("GetModFiles must not be called under VerifyLocal")
	return nil, nil
}

// TestVerify_LocalTier_NeverTouchesNetwork proves opts.Tier == VerifyLocal
// (the zero value) skips the version pass entirely: a source registered
// with a GetModFiles trap, and a source-backed mod carrying FileIDs (the
// version pass's own participation gate), never has its trap fire, and the
// result carries no version-pass rows (skipped/version_mismatch/
// version_unverifiable) - only the local per-file walk's own "ok" row.
func TestVerify_LocalTier_NeverTouchesNetwork(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	svc.RegisterSource(&trapGetModFilesSource{mockSource: newMockSource("trap-src"), t: t})

	seedVerifyMod(t, svc, game, "trap-src", "mod-a", "Mod A", "1.0", []string{"a-file"}, true)
	require.NoError(t, svc.SaveFileChecksum("trap-src", "mod-a", game.ID, "default", "a-file", "checksum-a"))

	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyLocal}, nil)
	require.NoError(t, err)

	require.Equal(t, 1, result.Checked, "only the per-file walk's row - the version pass never ran")
	require.Equal(t, []core.VerifyFinding{
		{ModID: "mod-a", ModName: "Mod A", FileID: "a-file", Status: "ok"},
	}, result.Findings, "no version_* or skipped row from a version pass that never ran")
}

// scriptedVersionSource lets a test script a per-modID GetModFiles outcome
// (files or an error) from one shared source registration -
// TestVerify_FullTier_VersionStatuses' table needs a distinct outcome per
// mod under a single source.
type scriptedVersionSource struct {
	*mockSource
	filesByModID map[string][]domain.DownloadableFile
	errByModID   map[string]error
}

func (s *scriptedVersionSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if err, ok := s.errByModID[mod.ID]; ok {
		return nil, err
	}
	return s.filesByModID[mod.ID], nil
}

// TestVerify_FullTier_VersionStatuses is the #224 Task 3 Full-tier version-
// pass fixture: five source-backed mods, each installed with a single
// checksummed+cached file (so the per-file walk contributes only quiet "ok"
// rows, isolating the version pass's own findings) - reachable+matched
// (quiet ok, Checked++ only), unreachable (skipped w/ note), no matching
// fileID (version_unverifiable), effective != recorded
// (version_mismatch w/ Recorded/Effective extras, issues++), and a locked
// ref pending convergence (ok + note row). Also proves a VerifyEvProgress
// tick fires for every installed mod.
func TestVerify_FullTier_VersionStatuses(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	src := &scriptedVersionSource{
		mockSource: newMockSource("vsrc"),
		filesByModID: map[string][]domain.DownloadableFile{
			"reachable-mod":    {{ID: "f1", Version: "1.0", IsPrimary: true}},
			"unverifiable-mod": {{ID: "other-file", Version: "1.0", IsPrimary: true}},
			"mismatch-mod":     {{ID: "f1", Version: "2.0", IsPrimary: true}},
			"locked-mod":       {{ID: "f1", Version: "1.0", IsPrimary: true}},
		},
		errByModID: map[string]error{
			"unreachable-mod": errors.New("boom"),
		},
	}
	svc.RegisterSource(src)

	// Installed in this order: GetInstalledMods' DB order (installed_at)
	// drives the version pass's iteration order, and therefore
	// VerifyEvProgress's Index/ModName sequence below.
	seedVerifyMod(t, svc, game, "vsrc", "reachable-mod", "Reachable", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum("vsrc", "reachable-mod", game.ID, "default", "f1", "cs-reachable"))

	seedVerifyMod(t, svc, game, "vsrc", "unreachable-mod", "Unreachable", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum("vsrc", "unreachable-mod", game.ID, "default", "f1", "cs-unreachable"))

	seedVerifyMod(t, svc, game, "vsrc", "unverifiable-mod", "Unverifiable", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum("vsrc", "unverifiable-mod", game.ID, "default", "f1", "cs-unverifiable"))

	seedVerifyMod(t, svc, game, "vsrc", "mismatch-mod", "Mismatch", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum("vsrc", "mismatch-mod", game.ID, "default", "f1", "cs-mismatch"))

	seedVerifyMod(t, svc, game, "vsrc", "locked-mod", "Locked", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum("vsrc", "locked-mod", game.ID, "default", "f1", "cs-locked"))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "vsrc", ModID: "locked-mod", Version: "1.0", FileIDs: []string{"f1"}}))
	require.NoError(t, pm.SetModLock(game.ID, "default", "vsrc", "locked-mod", "2.0"))

	var events []core.VerifyEvent
	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, func(e core.VerifyEvent) {
		events = append(events, e)
	})
	require.NoError(t, err)

	require.Equal(t, 1, result.Issues, "only mismatch-mod's VERSION MISMATCH counts as an issue")
	require.Equal(t, 2, result.Warnings, "unreachable-mod's skipped + unverifiable-mod's version_unverifiable")
	// Checked = 5 per-file "ok" rows + reachable-mod's and locked-mod's
	// quiet version-pass rows.
	require.Equal(t, 7, result.Checked)

	wantFindings := []core.VerifyFinding{
		// --- version pass (runs before the per-file walk) ---
		{ModID: "unreachable-mod", ModName: "Unreachable", Status: "skipped", Note: "could not check version: boom"},
		{ModID: "unverifiable-mod", ModName: "Unverifiable", Status: "version_unverifiable"},
		{ModID: "mismatch-mod", ModName: "Mismatch", Status: "version_mismatch"},
		{ModID: "locked-mod", ModName: "Locked", Status: "ok", Note: "lock pending convergence (installed v1.0, locked v2.0)"},
		// --- per-file walk (DB/checksum insertion order) ---
		{ModID: "reachable-mod", ModName: "Reachable", FileID: "f1", Status: "ok"},
		{ModID: "unreachable-mod", ModName: "Unreachable", FileID: "f1", Status: "ok"},
		{ModID: "unverifiable-mod", ModName: "Unverifiable", FileID: "f1", Status: "ok"},
		{ModID: "mismatch-mod", ModName: "Mismatch", FileID: "f1", Status: "ok"},
		{ModID: "locked-mod", ModName: "Locked", FileID: "f1", Status: "ok"},
	}
	require.Equal(t, wantFindings, result.Findings)

	// The version_mismatch finding's event carries the Recorded/Effective
	// extras (not persisted on VerifyFinding itself).
	var mismatchEvent *core.VerifyEvent
	var progressEvents []core.VerifyEvent
	for i := range events {
		if events[i].Kind == core.VerifyEvFinding && events[i].Finding.Status == "version_mismatch" {
			mismatchEvent = &events[i]
		}
		if events[i].Kind == core.VerifyEvProgress {
			progressEvents = append(progressEvents, events[i])
		}
	}
	require.NotNil(t, mismatchEvent)
	require.Equal(t, "1.0", mismatchEvent.Recorded)
	require.Equal(t, "2.0", mismatchEvent.Effective)

	require.Len(t, progressEvents, 5, "one VerifyEvProgress tick per installed mod")
	wantModNames := []string{"Reachable", "Unreachable", "Unverifiable", "Mismatch", "Locked"}
	for i, ev := range progressEvents {
		require.Equal(t, i+1, ev.Index)
		require.Equal(t, 5, ev.Total)
		require.Equal(t, wantModNames[i], ev.ModName)
	}
}

// TestVerify_CompileGameStatuses is the #224 Task 3 DeployCompile fixture:
// a stale merged-pak fingerprint (stale_compile), a pak mod whose merge
// conversion failed (conversion_failed, Note carries FailReason), and two
// pre-#221 pak cache entries (a deployable member on disk, no retained
// source) - one source-backed (needs_reingest with the redownload note) and
// one SourceLocal (the re-import note variant).
func TestVerify_CompileGameStatuses(t *testing.T) {
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc := newFlowsTestService(t)
	src := &pakConversionOutcomeSource{
		fakeCompilerSource: &fakeCompilerSource{},
		failRefs:           map[string]string{"fake-compiler:badpak": "irreconcilable pak layout"},
	}
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy, ConvertPaks: true,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	// goodmod converts cleanly; badpak's conversion is scripted to fail
	// (src.failRefs above) - both enabled and merged in the first sync.
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "goodmod", "1.0", "exmodz-file", []byte("good-bytes"))
	seedEnabledPakMod(t, svc, game, "fake-compiler", "badpak", "1.0", "pak", []byte("bad-bytes"))

	gameCache := svc.GetGameCache(game)

	// A pre-#221 pak cache entry: a deployable member on disk, but no
	// retained source (predates #221's widened ingest) - PakNeedsReingest's
	// lazy-migration case.
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", "legacypak", "1.0", "legacypak.pak", []byte("legacy-bytes")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "legacypak", SourceID: "fake-compiler", Name: "Legacy Pak", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"pak"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "legacypak", Version: "1.0", FileIDs: []string{"pak"}}))

	// The same pre-#221 shape, but SourceLocal - the note text switches on
	// this.
	require.NoError(t, gameCache.Store(game.ID, domain.SourceLocal, "locallegacy", "1.0", "locallegacy.pak", []byte("local-legacy-bytes")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "locallegacy", SourceID: domain.SourceLocal, Name: "Local Legacy Pak", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"pak"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: domain.SourceLocal, ModID: "locallegacy", Version: "1.0", FileIDs: []string{"pak"}}))

	// First (only) sync: stamps MergedPakOutcomes with badpak's conversion
	// failure and establishes an up-to-date fingerprint.
	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	// Go stale: enable a third exmodz mod WITHOUT syncing again.
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolfmod", "1.0", "exmodz-file", []byte("wolf-bytes"))

	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)

	staleFinding, ok := findFindingByStatus(result.Findings, "stale_compile")
	require.True(t, ok, "expected a stale_compile row")
	require.Equal(t, "merged-pak", staleFinding.ModID, "a merged-pak staleness row's identity is the synthetic merged-pak mod")
	require.Equal(t, "base pak updated", staleFinding.Note)

	convFinding, ok := findFindingByStatus(result.Findings, "conversion_failed")
	require.True(t, ok, "expected a conversion_failed row")
	require.Equal(t, "badpak", convFinding.ModID)
	require.Equal(t, "irreconcilable pak layout", convFinding.Note)

	var reingestFindings []core.VerifyFinding
	for _, f := range result.Findings {
		if f.Status == "needs_reingest" {
			reingestFindings = append(reingestFindings, f)
		}
	}
	require.Len(t, reingestFindings, 2)
	byModID := map[string]core.VerifyFinding{reingestFindings[0].ModID: reingestFindings[0], reingestFindings[1].ModID: reingestFindings[1]}

	legacy, ok := byModID["legacypak"]
	require.True(t, ok)
	require.Equal(t, "pak predates conversion support - run 'lmm verify --fix' to re-ingest", legacy.Note)

	localLegacy, ok := byModID["locallegacy"]
	require.True(t, ok)
	require.Equal(t, "re-import the archive to enable conversion", localLegacy.Note)
}

// findFindingByStatus returns the first finding in findings with the given
// status.
func findFindingByStatus(findings []core.VerifyFinding, status string) (core.VerifyFinding, bool) {
	for _, f := range findings {
		if f.Status == status {
			return f, true
		}
	}
	return core.VerifyFinding{}, false
}

// cancelOnFirstCallSource cancels the test's context as a side effect of
// its FIRST GetModFiles call (mirroring a real request that gets cancelled
// mid-flight), then returns normally - so the mod being processed when
// cancellation happens still completes, but the NEXT mod's iteration must
// observe ctx.Err() and stop.
type cancelOnFirstCallSource struct {
	*mockSource
	cancel context.CancelFunc
	called int
}

func (s *cancelOnFirstCallSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	s.called++
	if s.called == 1 {
		s.cancel()
	}
	return []domain.DownloadableFile{{ID: "f1", Version: "1.0", IsPrimary: true}}, nil
}

// TestVerify_ContextCancelledMidVersionPass proves the version pass honors
// ctx cancellation BETWEEN mods: cancellation triggered as a side effect of
// the first mod's GetModFiles call lets that mod's own row finish, but the
// second mod's iteration must never call GetModFiles at all, and Verify
// must return ctx.Err() alongside the partial result already accumulated.
func TestVerify_ContextCancelledMidVersionPass(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	src := &cancelOnFirstCallSource{mockSource: newMockSource("csrc"), cancel: cancel}
	svc.RegisterSource(src)

	seedVerifyMod(t, svc, game, "csrc", "mod-a", "Mod A", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum("csrc", "mod-a", game.ID, "default", "f1", "cs-a"))

	seedVerifyMod(t, svc, game, "csrc", "mod-b", "Mod B", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum("csrc", "mod-b", game.ID, "default", "f1", "cs-b"))

	result, err := svc.Verify(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)

	require.Equal(t, 1, src.called, "mod-b's GetModFiles must never be called after cancellation")
	require.Equal(t, 1, result.Checked, "mod-a's quiet-OK version check ran to completion before cancellation was noticed")
	require.Empty(t, result.Findings, "the per-file walk never ran - Verify returned before reaching it")
}

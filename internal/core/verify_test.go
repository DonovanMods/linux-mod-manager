package core_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
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
		"missing-file":     {ModID: "mod-missing", ModName: "Mod Missing", FileID: "missing-file", Status: "missing", Version: "1.0"},
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
		{ModID: "mismatch-mod", ModName: "Mismatch", Status: "version_mismatch", Recorded: "1.0", Effective: "2.0"},
		{ModID: "locked-mod", ModName: "Locked", Status: "ok", Note: "lock pending convergence (installed v1.0, locked v2.0)"},
		// --- per-file walk (DB/checksum insertion order) ---
		{ModID: "reachable-mod", ModName: "Reachable", FileID: "f1", Status: "ok"},
		{ModID: "unreachable-mod", ModName: "Unreachable", FileID: "f1", Status: "ok"},
		{ModID: "unverifiable-mod", ModName: "Unverifiable", FileID: "f1", Status: "ok"},
		{ModID: "mismatch-mod", ModName: "Mismatch", FileID: "f1", Status: "ok"},
		{ModID: "locked-mod", ModName: "Locked", FileID: "f1", Status: "ok"},
	}
	require.Equal(t, wantFindings, result.Findings)

	// The version_mismatch finding's event carries the same Recorded/
	// Effective values as the VerifyFinding row itself (wantFindings above).
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

// TestVerify_VersionMismatchFinding_CarriesRecordedEffective is the #224
// follow-up TDD guard: the Health screen's VERSION column needs the
// recorded/effective versions directly on VerifyFinding, not just the
// VerifyEvent's extras (which a non-progress-subscribed caller never sees)
// - see VerifyFinding.Recorded/Effective's own doc comment.
func TestVerify_VersionMismatchFinding_CarriesRecordedEffective(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	src := &scriptedVersionSource{
		mockSource:   newMockSource("vsrc"),
		filesByModID: map[string][]domain.DownloadableFile{"mismatch-mod": {{ID: "f1", Version: "2.0", IsPrimary: true}}},
	}
	svc.RegisterSource(src)

	seedVerifyMod(t, svc, game, "vsrc", "mismatch-mod", "Mismatch", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum("vsrc", "mismatch-mod", game.ID, "default", "f1", "checksum-mismatch"))

	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)

	mismatch, ok := findFindingByStatus(result.Findings, "version_mismatch")
	require.True(t, ok, "expected a version_mismatch row")
	require.Equal(t, "1.0", mismatch.Recorded, "VerifyFinding.Recorded must carry the pre-repair recorded version")
	require.Equal(t, "2.0", mismatch.Effective, "VerifyFinding.Effective must carry the source-reported version")
}

// TestVerify_MissingFinding_CarriesVersion is the #224 follow-up TDD guard:
// the Health screen's VERSION column needs the
// installed mod's recorded version directly on a "missing" VerifyFinding,
// mirroring the VerifyEvent.Version extra the CLI already renders from.
func TestVerify_MissingFinding_CarriesVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	seedVerifyMod(t, svc, game, "src", "missing-mod", "Missing Mod", "1.5", []string{"missing-file"}, false)
	require.NoError(t, svc.SaveFileChecksum("src", "missing-mod", game.ID, "default", "missing-file", "checksum-missing"))

	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)

	missing, ok := findFindingByStatus(result.Findings, "missing")
	require.True(t, ok, "expected a missing row")
	require.Equal(t, "1.5", missing.Version, "VerifyFinding.Version must carry the installed mod's recorded version")
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

// --- #224 Task 4: fix mode I - redownload repairs (missing / no_checksum /
// needs_reingest) --------------------------------------------------------
//
// errRedownloadRepair is a fixed sentinel error used by the fix-mode fake
// sources below so a failed repair's finding.Note (the bare err.Error(),
// what --json exposes) and its RepairDetail.Detail (a full sentence, what
// the CLI's text rendering shows) can be asserted against each other
// precisely, independent of the real downloader's own wrapped error text.
var errRedownloadRepair = errors.New("redownload boom")

// redownloadURLFailingSource wraps mockSource with a GetDownloadURL that
// always fails errRedownloadRepair - the MISSING/NO CHECKSUM --fix
// redownload-failure fixture. mockSource's own GetModFiles (a single file ID
// "1") still succeeds, so the failure is attributable specifically to the
// download step, matching a real expired-URL/network failure.
type redownloadURLFailingSource struct {
	*mockSource
}

func (s *redownloadURLFailingSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", errRedownloadRepair
}

// nonArchiveDownloadSource wraps mockSourceWithDownloads but serves a file
// named "<mod.ID>.dat" instead of mockSource's hardcoded "<mod.ID>.zip" - the
// extension drives DownloadModToCache's copy-vs-extract branch
// (Extractor.CanExtract), and the fix-mode redownload success tests want the
// plain copy path so arbitrary byte content downloads cleanly without having
// to be a real archive.
type nonArchiveDownloadSource struct {
	*mockSourceWithDownloads
}

func (s *nonArchiveDownloadSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{{ID: "1", Name: "Main File", FileName: mod.ID + ".dat", IsPrimary: true}}, nil
}

// newEmptyDirSource builds (but does not register) a custom.Directory source
// over a root containing one EMPTY mod directory named modID.
// ingestLocalToCache's digestDirectoryMembers returns "" for zero members -
// the only path in this codebase where a download succeeds with no checksum
// to store (redownloadModFile's own doc comment) - the fixture for the
// "re-downloaded, but no checksum was available to store" branch of both the
// MISSING and NO CHECKSUM --fix repairs. The directory source's own
// GetModFiles always serves a single synthetic file ID, "main".
func newEmptyDirSource(t *testing.T, sourceID, modID string) source.ModSource {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, modID), 0o755))
	src, err := custom.New(custom.SourceDefinition{
		ID:        sourceID,
		Name:      "Empty Dir Source",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: root},
	})
	require.NoError(t, err)
	return src
}

// repairDetails filters events down to its VerifyEvRepairDetail entries, in
// order - the fix-mode tests' sub-line assertions.
func repairDetails(events []core.VerifyEvent) []core.VerifyEvent {
	var out []core.VerifyEvent
	for _, e := range events {
		if e.Kind == core.VerifyEvRepairDetail {
			out = append(out, e)
		}
	}
	return out
}

// newFixTestGame is the shared fixture for the MISSING/NO CHECKSUM --fix
// tests: a plain (non-DeployCompile) game with a "default" profile ready for
// a single seeded mod.
func newFixTestGame(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	return svc, game
}

// TestVerify_Fix_Missing_Redownload is the #224 Task 4 MISSING repair
// fixture: a checksummed file whose cache entry is absent, verified with
// --fix, across redownloadModFile's three possible outcomes - a successful
// redownload resolves the row to "ok" and backs the issue out; a redownload
// that succeeds but yields no checksum (the empty-directory-source case)
// demotes the row to "no_checksum" (issue backed out, warning added
// instead); and a failed redownload leaves the row "missing" with the
// failure reason recorded as its Note.
func TestVerify_Fix_Missing_Redownload(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		svc, game := newFixTestGame(t)
		mock := newMockSourceWithDownloads("rsrc")
		t.Cleanup(mock.Close)
		mock.AddDownload("1", []byte("fresh content"))
		svc.RegisterSource(&nonArchiveDownloadSource{mockSourceWithDownloads: mock})
		seedVerifyMod(t, svc, game, "rsrc", "mod1", "Mod One", "1.0", []string{"1"}, false)
		require.NoError(t, svc.SaveFileChecksum("rsrc", "mod1", game.ID, "default", "1", "old-checksum"))

		var events []core.VerifyEvent
		result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) { events = append(events, e) })
		require.NoError(t, err)

		require.Equal(t, 0, result.Issues, "a successful redownload resolves the missing issue")
		require.Equal(t, 0, result.Warnings)
		require.Equal(t, []core.VerifyFinding{{ModID: "mod1", ModName: "Mod One", FileID: "1", Status: "ok"}}, result.Findings)

		details := repairDetails(events)
		require.Len(t, details, 1)
		require.Equal(t, "Re-downloaded OK", details[0].Detail)
		require.True(t, details[0].Green)
	})

	t.Run("no_checksum_available", func(t *testing.T) {
		svc, game := newFixTestGame(t)
		svc.RegisterSource(newEmptyDirSource(t, "rsrc", "mod1"))
		seedVerifyMod(t, svc, game, "rsrc", "mod1", "Mod One", "1.0", []string{"main"}, false)
		require.NoError(t, svc.SaveFileChecksum("rsrc", "mod1", game.ID, "default", "main", "old-checksum"))

		var events []core.VerifyEvent
		result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) { events = append(events, e) })
		require.NoError(t, err)

		require.Equal(t, 0, result.Issues, "the cache was restored, so the missing issue is backed out")
		require.Equal(t, 1, result.Warnings, "but no checksum could be stored, so a warning replaces it")
		require.Equal(t, []core.VerifyFinding{
			{ModID: "mod1", ModName: "Mod One", FileID: "main", Status: "no_checksum", Note: "re-downloaded, but no checksum was available to store"},
		}, result.Findings)

		details := repairDetails(events)
		require.Len(t, details, 1)
		require.Equal(t, "Re-downloaded, but no checksum was available to store - NO CHECKSUM remains", details[0].Detail)
		require.False(t, details[0].Green)
	})

	t.Run("error", func(t *testing.T) {
		svc, game := newFixTestGame(t)
		svc.RegisterSource(&redownloadURLFailingSource{mockSource: newMockSource("rsrc")})
		seedVerifyMod(t, svc, game, "rsrc", "mod1", "Mod One", "1.0", []string{"1"}, false)
		require.NoError(t, svc.SaveFileChecksum("rsrc", "mod1", game.ID, "default", "1", "old-checksum"))

		var events []core.VerifyEvent
		result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) { events = append(events, e) })
		require.NoError(t, err)

		require.Equal(t, 1, result.Issues, "the failed redownload leaves the missing issue outstanding")
		require.Equal(t, 0, result.Warnings)
		require.Len(t, result.Findings, 1)
		wantErr := "getting download URL: redownload boom"
		require.Equal(t, "missing", result.Findings[0].Status)
		require.Equal(t, wantErr, result.Findings[0].Note, "the finding's Note is the bare error, feeding --json directly")

		details := repairDetails(events)
		require.Len(t, details, 1)
		require.Equal(t, "Re-download failed: "+wantErr, details[0].Detail)
		require.False(t, details[0].Green)
	})
}

// TestVerify_Fix_NoChecksum_Redownload is the #224 Task 4 NO CHECKSUM repair
// fixture: a cached file whose checksum was never stored, verified with
// --fix, across the same three redownloadModFile outcomes as MISSING - but
// NO CHECKSUM's row is never pre-emitted before the repair attempt (unlike
// MISSING's), so each branch emits its own final row directly (brief: "port
// the exact branching from lines 804-846").
func TestVerify_Fix_NoChecksum_Redownload(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		svc, game := newFixTestGame(t)
		mock := newMockSourceWithDownloads("rsrc")
		t.Cleanup(mock.Close)
		mock.AddDownload("1", []byte("fresh content"))
		svc.RegisterSource(&nonArchiveDownloadSource{mockSourceWithDownloads: mock})
		seedVerifyMod(t, svc, game, "rsrc", "mod1", "Mod One", "1.0", []string{"1"}, true)

		var events []core.VerifyEvent
		result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) { events = append(events, e) })
		require.NoError(t, err)

		require.Equal(t, 0, result.Issues)
		require.Equal(t, 0, result.Warnings, "a persisted checksum never counted as a warning in the first place")
		require.Equal(t, []core.VerifyFinding{{ModID: "mod1", ModName: "Mod One", FileID: "1", Status: "ok"}}, result.Findings)

		// The "checksum populated" outcome is a main-line "ok" row, not a
		// RepairDetail sub-line - the CLI renders it via Variant, not an
		// indented detail.
		require.Empty(t, repairDetails(events))
		var findingEvent *core.VerifyEvent
		for i := range events {
			if events[i].Kind == core.VerifyEvFinding {
				findingEvent = &events[i]
			}
		}
		require.NotNil(t, findingEvent)
		require.Equal(t, "checksum_populated", findingEvent.Variant)
	})

	t.Run("no_checksum_available", func(t *testing.T) {
		svc, game := newFixTestGame(t)
		svc.RegisterSource(newEmptyDirSource(t, "rsrc", "mod1"))
		seedVerifyMod(t, svc, game, "rsrc", "mod1", "Mod One", "1.0", []string{"main"}, true)

		var events []core.VerifyEvent
		result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) { events = append(events, e) })
		require.NoError(t, err)

		require.Equal(t, 0, result.Issues)
		require.Equal(t, 1, result.Warnings)
		require.Equal(t, []core.VerifyFinding{
			{ModID: "mod1", ModName: "Mod One", FileID: "main", Status: "no_checksum", Note: "re-downloaded, but no checksum was available to store"},
		}, result.Findings)

		details := repairDetails(events)
		require.Len(t, details, 1)
		require.Equal(t, "Re-downloaded, but no checksum was available to store", details[0].Detail, "no ' - NO CHECKSUM remains' suffix here - that phrasing is MISSING's own")
		require.False(t, details[0].Green)
	})

	t.Run("error", func(t *testing.T) {
		svc, game := newFixTestGame(t)
		svc.RegisterSource(&redownloadURLFailingSource{mockSource: newMockSource("rsrc")})
		seedVerifyMod(t, svc, game, "rsrc", "mod1", "Mod One", "1.0", []string{"1"}, true)

		var events []core.VerifyEvent
		result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) { events = append(events, e) })
		require.NoError(t, err)

		require.Equal(t, 0, result.Issues)
		require.Equal(t, 1, result.Warnings)
		require.Len(t, result.Findings, 1)
		wantErr := "getting download URL: redownload boom"
		require.Equal(t, "no_checksum", result.Findings[0].Status)
		require.Equal(t, wantErr, result.Findings[0].Note)

		details := repairDetails(events)
		require.Len(t, details, 1)
		require.Equal(t, "Re-download to populate checksum failed: "+wantErr, details[0].Detail)
		require.False(t, details[0].Green)
	})
}

// newByteServer starts an httptest.Server that always responds with
// content, closed automatically via t.Cleanup - the download source backing
// the needs_reingest --fix success test (fakeCompilerSource's own
// downloadURL field, unlike mockSourceWithDownloads, has no server built
// in).
func newByteServer(t *testing.T, content []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// reingestFixSource lets the #224 Task 4 needs_reingest --fix tests exercise
// redownloadModFile's real GetModFiles/DownloadMod path against the same
// legacypak fixture PakNeedsReingest's read-side tests use
// (TestVerify_CompileGameStatuses): fakeCompilerSource's own GetModFiles
// always fails with ErrNotSupported (fine for those read-side tests, which
// never call it), but --fix's redownload repair calls GetModFiles first to
// resolve the "pak" file ID. Embeds *fakeCompilerSource so ValidateSource/
// MergeCompile (source.MergeCompiler) are still promoted - the pak-ingest
// path itself runs for real, genuinely retaining the source on success.
type reingestFixSource struct {
	*fakeCompilerSource
}

func (s *reingestFixSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{{ID: "pak", Name: mod.Name, FileName: mod.ID + ".pak", IsPrimary: true}}, nil
}

// reingestFixErrorSource is reingestFixSource with a GetDownloadURL that
// always fails errRedownloadRepair - the needs_reingest --fix error-path
// fixture.
type reingestFixErrorSource struct {
	*reingestFixSource
}

func (s *reingestFixErrorSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", errRedownloadRepair
}

// newNeedsReingestFixGame builds the DeployCompile fixture PakNeedsReingest's
// lazy-migration case needs: a pre-#221 pak cache entry (a deployable member
// on disk, no retained source) for a source-backed mod, registered under src
// - the same legacypak shape TestVerify_CompileGameStatuses's read-side
// fixture uses, trimmed to just what the --fix repair tests need.
func newNeedsReingestFixGame(t *testing.T, src source.ModSource, sourceID string) (*core.Service, *domain.Game) {
	t.Helper()
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc := newFlowsTestService(t)
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy, ConvertPaks: true,
		SourceIDs: map[string]string{sourceID: "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, sourceID, "legacypak", "1.0", "legacypak.pak", []byte("legacy-bytes")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "legacypak", SourceID: sourceID, Name: "Legacy Pak", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"pak"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: "legacypak", Version: "1.0", FileIDs: []string{"pak"}}))

	return svc, game
}

// TestVerify_Fix_NeedsReingest_Redownload is the #224 Task 4 NEEDS REINGEST
// repair fixture: a pre-#221 pak cache entry, verified with --fix. Unlike
// MISSING/NO CHECKSUM, redownloadModFile's persisted flag plays no role here
// (brief: only the error is branched on) - a successful re-ingest always
// resolves to fixed_needs_reingest.
func TestVerify_Fix_NeedsReingest_Redownload(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dlSrv := newByteServer(t, []byte("legacy-pak-bytes"))
		src := &reingestFixSource{fakeCompilerSource: &fakeCompilerSource{downloadURL: dlSrv}}
		svc, game := newNeedsReingestFixGame(t, src, "fake-compiler")

		var events []core.VerifyEvent
		result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) { events = append(events, e) })
		require.NoError(t, err)

		require.Equal(t, 0, result.Warnings, "a successful re-ingest backs out the warning")
		reingest, ok := findFindingByStatus(result.Findings, "fixed_needs_reingest")
		require.True(t, ok, "expected a fixed_needs_reingest row: %+v", result.Findings)
		require.Equal(t, "legacypak", reingest.ModID)
		require.Equal(t, "re-ingested with retained source for pak conversion", reingest.Note)

		details := repairDetails(events)
		require.Len(t, details, 1)
		require.Equal(t, "Re-ingested for pak conversion", details[0].Detail)
		require.True(t, details[0].Green)
	})

	t.Run("error", func(t *testing.T) {
		src := &reingestFixErrorSource{reingestFixSource: &reingestFixSource{fakeCompilerSource: &fakeCompilerSource{}}}
		svc, game := newNeedsReingestFixGame(t, src, "fake-compiler")

		var events []core.VerifyEvent
		result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) { events = append(events, e) })
		require.NoError(t, err)

		require.Equal(t, 1, result.Warnings, "a failed re-ingest leaves the warning outstanding")
		finding, ok := findFindingByStatus(result.Findings, "needs_reingest")
		require.True(t, ok, "expected the row to stay needs_reingest: %+v", result.Findings)
		require.Equal(t, "legacypak", finding.ModID)
		wantErr := "getting download URL: redownload boom"
		require.Equal(t, "re-ingest failed: "+wantErr, finding.Note, "the finding's Note is lowercase, unlike the printed sub-line")

		details := repairDetails(events)
		require.Len(t, details, 1)
		require.Equal(t, "Re-ingest failed: "+wantErr, details[0].Detail)
		require.False(t, details[0].Green)
	})
}

// TestVerify_Fix_SourceLocal_NoRepairAttempted proves the "any+SourceLocal"
// case: --fix's guard (opts.Fix && mod.SourceID != domain.SourceLocal) is
// identical across all three repair sites, so a SourceLocal mod's finding is
// left exactly as the read-side pass reported it - no redownload is
// attempted (there is no source to redownload from), no RepairDetail event
// fires, and Issues/Warnings are untouched by any repair.
func TestVerify_Fix_SourceLocal_NoRepairAttempted(t *testing.T) {
	svc, game := newFixTestGame(t)
	seedVerifyMod(t, svc, game, domain.SourceLocal, "mod1", "Mod One", "1.0", []string{"1"}, false)
	require.NoError(t, svc.SaveFileChecksum(domain.SourceLocal, "mod1", game.ID, "default", "1", "old-checksum"))

	var events []core.VerifyEvent
	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) { events = append(events, e) })
	require.NoError(t, err)

	require.Equal(t, 1, result.Issues, "no repair was attempted - the missing issue stands")
	require.Equal(t, 0, result.Warnings)
	require.Equal(t, []core.VerifyFinding{{ModID: "mod1", ModName: "Mod One", FileID: "1", Status: "missing", Version: "1.0"}}, result.Findings)
	require.Empty(t, repairDetails(events), "SourceLocal must never reach a redownload attempt")
}

// --- #224 Task 5: fix mode II - version repair, siblings, locked refusals
// ---------------------------------------------------------------------------
//
// newVersionRepairFixGame builds the shared version_mismatch --fix fixture:
// a "test-src"/"mod1" mod recorded at "1.5" whose stored file ID "2" is, per
// the (scripted) source, actually version "1.0" - the same recorded/
// effective shape the pre-refactor CLI's setupDoVerifyFixTest used. The
// profile ref is upserted to match (so pm.UpsertMod's rename path is
// exercised for real, and a lock test has a ref to lock). When deployed is
// true, the row is marked Deployed via a real installer.Install call (not
// hand-rolled) so the pre-state - a symlink pointing INTO the "1.5" cache
// dir - matches what a real install would have produced before the repair
// renames it.
func newVersionRepairFixGame(t *testing.T, deployed bool) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := newFixTestGame(t)

	src := &scriptedVersionSource{
		mockSource: newMockSource("test-src"),
		filesByModID: map[string][]domain.DownloadableFile{
			"mod1": {{ID: "2", Version: "1.0", IsPrimary: true}},
		},
	}
	svc.RegisterSource(src)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", "1.5", "2", []byte("plugin content")))

	seedVerifyMod(t, svc, game, "test-src", "mod1", "Mod One", "1.5", []string{"2"}, false)
	require.NoError(t, svc.SaveFileChecksum("test-src", "mod1", game.ID, "default", "2", "deadbeef"))

	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: "1.5", FileIDs: []string{"2"}}))

	if deployed {
		require.NoError(t, svc.SetModDeployed("test-src", "mod1", game.ID, "default", true))
		require.NoError(t, svc.SetModLinkMethod("test-src", "mod1", game.ID, "default", domain.LinkSymlink))
		mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
		require.NoError(t, err)
		require.NoError(t, svc.GetInstaller(game).Install(context.Background(), game, &mod.Mod, "default"))
	}

	return svc, game
}

// addVersionRepairSibling adds profileName as a sibling profile carrying its
// own installed_mods row for test-src/mod1 at recordedVersion/fileIDs -
// newVersionRepairFixGame's sibling-fixture helper for the sibling-repaired/
// differs/locked scenarios.
func addVersionRepairSibling(t *testing.T, svc *core.Service, game *domain.Game, profileName, recordedVersion string, fileIDs []string) {
	t.Helper()
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, profileName)
	require.NoError(t, err)
	require.NoError(t, pm.UpsertMod(game.ID, profileName, domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: recordedVersion, FileIDs: fileIDs}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: recordedVersion, GameID: game.ID},
		ProfileName:  profileName,
		Enabled:      true,
		FileIDs:      fileIDs,
		UpdatePolicy: domain.UpdateNotify,
	}))
}

// runVersionRepairFix runs a Fix:true, VerifyFull Verify against game/
// "default" and returns the result plus every emitted event.
func runVersionRepairFix(t *testing.T, svc *core.Service, game *domain.Game) (*core.VerifyResult, []core.VerifyEvent) {
	t.Helper()
	var events []core.VerifyEvent
	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull, Fix: true}, func(e core.VerifyEvent) {
		events = append(events, e)
	})
	require.NoError(t, err)
	return result, events
}

// mismatchFinding returns the version-check finding (FileID == "") for
// mod1, failing the test if none is present.
func mismatchFinding(t *testing.T, findings []core.VerifyFinding) core.VerifyFinding {
	t.Helper()
	for _, f := range findings {
		if f.ModID == "mod1" && f.FileID == "" {
			return f
		}
	}
	t.Fatalf("expected a mod1 version-check finding: %+v", findings)
	return core.VerifyFinding{}
}

// TestVerify_Fix_VersionMismatch_NotDeployed_RepairsCacheAndRecord is the
// clean-repair scenario: not deployed, so the cache dir is renamed to the
// effective version, the DB row and profile ref are corrected, and the
// version-check finding flips from version_mismatch to ok with no note
// (nothing was blocked). Issues drops back to 0.
func TestVerify_Fix_VersionMismatch_NotDeployed_RepairsCacheAndRecord(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)

	result, events := runVersionRepairFix(t, svc, game)

	require.Equal(t, 0, result.Issues, "a successful repair must decrement issues back out")
	require.Equal(t, 0, result.Warnings)

	f := mismatchFinding(t, result.Findings)
	require.Equal(t, "ok", f.Status)
	require.Empty(t, f.Note, "no blocked-rename/sibling note - nothing to report")

	gameCache := svc.GetGameCache(game)
	require.True(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.0"), "cache must exist under the effective version")
	require.False(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.5"), "cache must no longer exist under the recorded version")

	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	require.Equal(t, "1.0", mod.Version, "DB row must be corrected to the effective version")

	pm := svc.NewProfileManager()
	profile, err := pm.Get(game.ID, "default")
	require.NoError(t, err)
	ref := profile.FindRef("test-src", "mod1")
	require.NotNil(t, ref)
	require.Equal(t, "1.0", ref.Version, "profile YAML ref must be corrected")

	details := repairDetails(events)
	require.Len(t, details, 1)
	require.Equal(t, "Repaired: 1.5 → 1.0", details[0].Detail)
	require.True(t, details[0].Green)
}

// TestVerify_Fix_VersionMismatch_RenameBlocked_NoRelink is the
// blocked-rename scenario: a cache dir already exists under the effective
// version, so the rename is skipped (a note is recorded instead of
// clobbering it), but the DB/profile record is still corrected. The mod is
// Deployed via symlink, proving the blocked rename also suppresses the
// re-link - the working deployment still points at the intact
// recorded-version cache dir and must be left alone.
func TestVerify_Fix_VersionMismatch_RenameBlocked_NoRelink(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, true)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", "1.0", "2", []byte("unvetted 1.0 content")))

	deployedPath := filepath.Join(game.ModPath, "2")
	preTarget, err := os.Readlink(deployedPath)
	require.NoError(t, err)
	require.Contains(t, preTarget, filepath.Join("test-src-mod1", "1.5"))

	result, events := runVersionRepairFix(t, svc, game)

	require.Equal(t, 0, result.Issues)
	f := mismatchFinding(t, result.Findings)
	require.Equal(t, "ok", f.Status)
	require.Equal(t, "cache entry for 1.0 already exists; left 1.5 in place", f.Note)

	require.True(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.5"), "old cache entry must be left in place")
	require.True(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.0"), "pre-existing new cache entry must be left untouched")

	postTarget, err := os.Readlink(deployedPath)
	require.NoError(t, err)
	require.Equal(t, preTarget, postTarget, "the deployment must still point at the original (intact) recorded-version cache dir - no relink")

	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	require.Equal(t, "1.0", mod.Version, "DB is still corrected even though the deployment was left alone")
	require.True(t, mod.Deployed, "Deployed must remain true - the existing deployment is still valid")

	details := repairDetails(events)
	require.Len(t, details, 2)
	require.Equal(t, "Repaired: 1.5 → 1.0", details[0].Detail)
	require.True(t, details[0].Green)
	require.Equal(t, "Note: cache entry for 1.0 already exists; left 1.5 in place", details[1].Detail)
	require.False(t, details[1].Green)
}

// TestVerify_Fix_VersionMismatch_LockedPrimary_RefusesRepair is the
// locked-primary scenario: the profile ref is locked at the recorded
// version, so --fix must refuse the rewrite entirely - the row stays
// version_mismatch, issues stays incremented, and the row's Note is the
// short "locked" marker (the findings-slice/JSON contract), while the
// RepairDetail event carries the full refusal sentence (the text-render
// contract).
func TestVerify_Fix_VersionMismatch_LockedPrimary_RefusesRepair(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)

	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "default", "test-src", "mod1", "1.5"))

	result, events := runVersionRepairFix(t, svc, game)

	require.Equal(t, 1, result.Issues, "a locked mod's version mismatch must still be reported and counted as an issue")
	f := mismatchFinding(t, result.Findings)
	require.Equal(t, "version_mismatch", f.Status, "status must NOT flip to ok - --fix refused the repair")
	require.Equal(t, "locked", f.Note)

	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	require.Equal(t, "1.5", mod.Version, "a locked record must not be rewritten by --fix")

	gameCache := svc.GetGameCache(game)
	require.True(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.5"))
	require.False(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.0"), "cache must not be renamed to the effective version for a locked mod")

	details := repairDetails(events)
	require.Len(t, details, 1)
	wantRefusal := "--fix skipped: Mod One is locked at v1.5 in profile default — the record is the lock's target; move the lock with 'lmm mod lock -s test-src -p default mod1 <version>' or unlock with 'lmm mod unlock -s test-src -p default mod1' instead of rewriting it."
	require.Equal(t, wantRefusal, details[0].Detail)
	require.False(t, details[0].Green)
}

// TestVerify_Fix_VersionMismatch_SiblingRepaired covers the sibling-repaired
// scenario AND the ordering trap: a sibling profile ("second") sharing the
// same stale recorded version and file selection must be corrected too, and
// its "Repaired (profile second): ..." RepairDetail must be emitted BEFORE
// the primary row's own "Repaired: ..." detail - the sibling repair happens
// DURING repairModVersion, while the primary's own resolution detail is
// only emitted by versionPass after repairModVersion returns.
func TestVerify_Fix_VersionMismatch_SiblingRepaired(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})

	result, events := runVersionRepairFix(t, svc, game)

	require.Equal(t, 0, result.Issues)
	require.Equal(t, 0, result.Warnings)

	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	require.Equal(t, "1.0", secondMod.Version, "sibling DB row must be corrected to the effective version")

	pm := svc.NewProfileManager()
	secondProfile, err := pm.Get(game.ID, "second")
	require.NoError(t, err)
	secondRef := secondProfile.FindRef("test-src", "mod1")
	require.NotNil(t, secondRef)
	require.Equal(t, "1.0", secondRef.Version, "sibling profile YAML ref must be corrected")

	f := mismatchFinding(t, result.Findings)
	require.Equal(t, "ok", f.Status)
	require.Equal(t, "also repaired in profile(s): second", f.Note, "note must mention the repaired sibling profile")

	details := repairDetails(events)
	require.Len(t, details, 3, "sibling detail + primary Repaired detail + Note detail (the sibling repair produced a non-empty note)")
	require.Equal(t, "Repaired (profile second): 1.5 → 1.0", details[0].Detail, "ordering trap: the sibling detail must be emitted FIRST")
	require.True(t, details[0].Green)
	require.Equal(t, "Repaired: 1.5 → 1.0", details[1].Detail, "the primary row's own resolution detail comes AFTER the sibling's")
	require.True(t, details[1].Green)
	require.Equal(t, "Note: also repaired in profile(s): second", details[2].Detail)
	require.False(t, details[2].Green)
}

// TestVerify_Fix_VersionMismatch_SiblingDiffers_DeclinedWithWarning covers
// the sibling-differs scenario: a sibling recording the same stale version
// but a DIFFERENT file selection must not be auto-repaired - it's declined
// with a warning instead, and that decline counts toward warnings via
// siblingFailures.
func TestVerify_Fix_VersionMismatch_SiblingDiffers_DeclinedWithWarning(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "differs", "1.5", []string{"3"})

	result, events := runVersionRepairFix(t, svc, game)

	require.Equal(t, 0, result.Issues, "the primary row's own repair still succeeds")
	require.Equal(t, 1, result.Warnings, "the declined sibling counts as a warning")

	differsMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "differs")
	require.NoError(t, err)
	require.Equal(t, "1.5", differsMod.Version, "a sibling with different FileIDs must NOT be auto-repaired")

	f := mismatchFinding(t, result.Findings)
	wantNote := "differs in file selection in profile(s): differs (run verify --fix -p <profile>)"
	require.Equal(t, wantNote, f.Note)

	details := repairDetails(events)
	require.Len(t, details, 3)
	require.Equal(t, "Warning: profile differs records the same version but differs in file selection; run verify --fix -p differs", details[0].Detail, "the decline detail must be emitted before the primary's own Repaired detail")
	require.False(t, details[0].Green)
	require.Equal(t, "Repaired: 1.5 → 1.0", details[1].Detail)
	require.Equal(t, "Note: "+wantNote, details[2].Detail)
}

// TestVerify_Fix_VersionMismatch_SiblingLocked_DeclinedWithWarning covers
// the sibling-locked scenario: a sibling ref locked in ITS OWN profile must
// not be rewritten, even though its version/FileIDs match the primary's
// pre-repair state - declined with a warning naming the mod (not the
// profile) as locked, with -s/-p flagged remedies. Counts toward warnings.
func TestVerify_Fix_VersionMismatch_SiblingLocked_DeclinedWithWarning(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})

	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "second", "test-src", "mod1", ""))

	result, events := runVersionRepairFix(t, svc, game)

	require.Equal(t, 0, result.Issues)
	require.Equal(t, 1, result.Warnings, "the declined locked sibling counts as a warning")

	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	require.Equal(t, "1.5", secondMod.Version, "a locked sibling must NOT be auto-repaired")

	secondProfile, err := pm.Get(game.ID, "second")
	require.NoError(t, err)
	secondRef := secondProfile.FindRef("test-src", "mod1")
	require.NotNil(t, secondRef)
	require.True(t, secondRef.Locked, "the sibling must remain locked")

	f := mismatchFinding(t, result.Findings)
	wantNote := "locked in profile(s): second (move the lock or unlock instead)"
	require.Equal(t, wantNote, f.Note)

	details := repairDetails(events)
	require.Len(t, details, 3)
	wantWarning := "Warning: Mod One is locked at v1.5 in profile second; run 'lmm mod lock -s test-src -p second mod1 <version>' or unlock with 'lmm mod unlock -s test-src -p second mod1' instead of rewriting it"
	require.Equal(t, wantWarning, details[0].Detail, "the sibling decline detail must be emitted before the primary's own Repaired detail")
	require.Equal(t, "Repaired: 1.5 → 1.0", details[1].Detail)
	require.Equal(t, "Note: "+wantNote, details[2].Detail)
}

// TestVerify_Fix_VersionMismatch_Deployed_RelinkFailure_ClearsDeployedFlag
// covers the relink-failure scenario: once the cache re-key and DB save
// succeed, mod.Version == effective, so a re-link failure (e.g. a read-only
// game dir) must not roll that back - but it must clear Deployed so the DB
// stays honest about the now-dangling deployment, and it must be reported
// as a FAILURE (row stays version_mismatch, issue stays counted), not
// folded into a false "Repaired" success.
func TestVerify_Fix_VersionMismatch_Deployed_RelinkFailure_ClearsDeployedFlag(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	svc, game := newVersionRepairFixGame(t, true)

	require.NoError(t, os.Chmod(game.ModPath, 0o555))
	t.Cleanup(func() { _ = os.Chmod(game.ModPath, 0o755) })

	result, events := runVersionRepairFix(t, svc, game)

	require.Equal(t, 1, result.Issues, "the re-link failure must leave the issue outstanding")
	f := mismatchFinding(t, result.Findings)
	require.Equal(t, "version_mismatch", f.Status, "status must stay version_mismatch - the repair did not fully succeed")
	require.Contains(t, f.Note, "repair failed: relinking deployed files:")

	mod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	require.Equal(t, "1.0", mod.Version, "the version correction from the cache/DB/profile steps must stand even though the re-link failed")
	require.False(t, mod.Deployed, "Deployed must be cleared when the re-link fails")

	// A read-only game dir makes BOTH relinkDeployedRow's undeploy-then-
	// install steps fail (removing the stale symlink also needs write
	// permission on the directory) - the undeploy failure is non-fatal
	// (relinkDeployedRow's own doc) and surfaced as its own detail emitted
	// DURING the primary's re-link attempt, before the final "Repair
	// failed" detail that versionPass emits once repairModVersion returns.
	details := repairDetails(events)
	require.Len(t, details, 2)
	require.Contains(t, details[0].Detail, "Warning: undeploy Mod One:")
	require.False(t, details[0].Green)
	require.Contains(t, details[1].Detail, "Repair failed: relinking deployed files:")
	require.False(t, details[1].Green)
}

// TestVerify_Fix_VersionMismatch_PrimaryRelinkFails_SiblingStillRepaired
// nails the ordering trap under partial failure: the PRIMARY row's own
// re-link fails, but the sibling repair still runs (it's gated on the cache
// rename, not the primary's re-link outcome) and its RepairDetail must
// still be emitted BEFORE the primary's own "Repair failed" detail. The
// note on the primary's (still version_mismatch) row must carry the
// successful sibling repair alongside the failure reason.
func TestVerify_Fix_VersionMismatch_PrimaryRelinkFails_SiblingStillRepaired(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
	svc, game := newVersionRepairFixGame(t, true)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})

	require.NoError(t, os.Chmod(game.ModPath, 0o555))
	t.Cleanup(func() { _ = os.Chmod(game.ModPath, 0o755) })

	result, events := runVersionRepairFix(t, svc, game)

	require.Equal(t, 1, result.Issues, "the primary row's own repair failed")
	f := mismatchFinding(t, result.Findings)
	require.Equal(t, "version_mismatch", f.Status)
	require.Contains(t, f.Note, "second", "the successful sibling repair must still be visible in the note despite the primary relink failure")
	require.Contains(t, f.Note, "repair failed: relinking deployed files:")

	secondMod, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	require.Equal(t, "1.0", secondMod.Version, "the sibling repair must have gone through independently of the primary's relink failure")

	// Three details, all emitted before versionPass's own final resolution
	// event: (1) the primary's own undeploy warning, emitted inside
	// repairModVersion's step-4 re-link attempt; (2) the sibling's Repaired
	// detail, emitted inside repairModVersion's step-5 sibling pass (which
	// runs regardless of step 4's outcome); (3) the "Repair failed" detail,
	// emitted by versionPass only AFTER repairModVersion returns.
	details := repairDetails(events)
	require.Len(t, details, 3)
	require.Contains(t, details[0].Detail, "Warning: undeploy Mod One:")
	require.False(t, details[0].Green)
	require.Equal(t, "Repaired (profile second): 1.5 → 1.0", details[1].Detail, "ordering trap: the sibling detail (emitted DURING repairModVersion) must precede the primary's own failure detail")
	require.True(t, details[1].Green)
	require.Contains(t, details[2].Detail, "Repair failed: relinking deployed files:", "the primary's own failure detail (emitted by versionPass AFTER repairModVersion returns) comes last")
	require.False(t, details[2].Green)
}

// --- #224 Task 6: convergence pass + merged-pak sync (engine completion) --

// strayDanglingSymlink plants a symlink in game.ModPath pointing INTO the
// game's own cache root at a path that was never actually stored - the
// simplest ConvergeDeployedFiles candidate (converge_test.go's own
// TestConverge_DanglingCacheLinkSwept fixture): no deployed_files row is
// needed at all, only a dangling cache-rooted symlink.
func strayDanglingSymlink(t *testing.T, svc *core.Service, game *domain.Game, name string) {
	t.Helper()
	cacheRoot := svc.GetGameCachePath(game)
	target := filepath.Join(cacheRoot, game.ID, "src-stray", "1.0", name)
	require.NoError(t, os.Symlink(target, filepath.Join(game.ModPath, name)))
}

// TestVerify_EmptyProfile_DanglingLink_DryRun proves the #217 empty-profile
// path (no checksummed files at all) now runs the convergence pass: a
// dangling cache-rooted symlink with no installed mods at all is reported
// as a stale_deployment warning, and Issues stays 0 (convergence never adds
// to Issues - only Warnings, matching the CLI's own reportConvergencePass).
func TestVerify_EmptyProfile_DanglingLink_DryRun(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	strayDanglingSymlink(t, svc, game, "stray.pak")

	var events []core.VerifyEvent
	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{}, func(e core.VerifyEvent) {
		events = append(events, e)
	})
	require.NoError(t, err)

	require.False(t, result.HasFiles)
	require.Equal(t, 0, result.Issues)
	require.Equal(t, 1, result.Warnings)
	require.Equal(t, []core.VerifyFinding{
		{FileID: "stray.pak", Status: "stale_deployment", Note: "dangling link into lmm cache"},
	}, result.Findings)

	require.Len(t, events, 2, "Begin, then the one convergence finding - nothing else runs on this path")
	require.Equal(t, core.VerifyEvBegin, events[0].Kind)
	require.False(t, events[0].HasFiles)
	require.Equal(t, core.VerifyEvFinding, events[1].Kind)
	require.Equal(t, result.Findings[0], events[1].Finding)

	_, statErr := os.Lstat(filepath.Join(game.ModPath, "stray.pak"))
	require.NoError(t, statErr, "a dry run (opts.Fix false) must not remove anything")
}

// TestVerify_EmptyProfile_DanglingLink_Fix is the --fix counterpart: the
// same dangling link is actually removed, reported as fixed_stale_
// deployment with the fixed_green variant, and does NOT count as a warning
// (a resolved problem, same convention as every other successful --fix
// repair in this engine).
func TestVerify_EmptyProfile_DanglingLink_Fix(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	strayDanglingSymlink(t, svc, game, "stray.pak")

	var events []core.VerifyEvent
	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) {
		events = append(events, e)
	})
	require.NoError(t, err)

	require.Equal(t, 0, result.Issues)
	require.Equal(t, 0, result.Warnings, "a successful fix is a resolved problem, not an outstanding one")
	require.Equal(t, []core.VerifyFinding{
		{FileID: "stray.pak", Status: "fixed_stale_deployment", Note: "dangling link into lmm cache"},
	}, result.Findings)

	require.Len(t, events, 2)
	require.Equal(t, core.VerifyEvFinding, events[1].Kind)
	require.Equal(t, "fixed_green", events[1].Variant, "the CLI renders the whole main line green for a fixed convergence row")

	_, statErr := os.Lstat(filepath.Join(game.ModPath, "stray.pak"))
	require.True(t, os.IsNotExist(statErr), "--fix must remove the dangling link")
}

// TestVerify_MainPath_ConvergenceAfterFileWalk proves the convergence pass
// also runs in the normal (HasFiles true) flow, and that its rows land
// AFTER every per-file-walk row in Findings - the phase order this task's
// brief pins (sync, then convergence, both after the per-file walk).
func TestVerify_MainPath_ConvergenceAfterFileWalk(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	seedVerifyMod(t, svc, game, "src", "mod-ok", "Mod OK", "1.0", []string{"ok-file"}, true)
	require.NoError(t, svc.SaveFileChecksum("src", "mod-ok", game.ID, "default", "ok-file", "checksum-ok"))

	strayDanglingSymlink(t, svc, game, "stray.pak")

	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)

	require.Equal(t, []core.VerifyFinding{
		{ModID: "mod-ok", ModName: "Mod OK", FileID: "ok-file", Status: "ok"},
		{FileID: "stray.pak", Status: "stale_deployment", Note: "dangling link into lmm cache"},
	}, result.Findings)
	require.Equal(t, 1, result.Warnings)
}

// TestVerify_Fix_SyncMergedPak_WarningsSurfaceAsSyncEvents proves --fix's
// merged-pak resync (SyncMergedPak) runs before the convergence pass, and
// every warning it returns surfaces as its own VerifyEvSyncWarning event -
// ported from doVerify's own fmt.Fprintf(os.Stderr, "Warning: %s\n", w) loop
// (cmd/lmm/verify.go:818-824).
func TestVerify_Fix_SyncMergedPak_WarningsSurfaceAsSyncEvents(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	srcRaw, err := svc.GetSource("fake-compiler")
	require.NoError(t, err)
	src, ok := srcRaw.(*fakeCompilerSource)
	require.True(t, ok)
	src.mergeWarnings = []string{"asset collision: fixture warning"}
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	var events []core.VerifyEvent
	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) {
		events = append(events, e)
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	var syncEvents []core.VerifyEvent
	for _, e := range events {
		if e.Kind == core.VerifyEvSyncWarning {
			syncEvents = append(syncEvents, e)
		}
	}
	require.Len(t, syncEvents, 1)
	require.Equal(t, "asset collision: fixture warning", syncEvents[0].Detail)
}

// TestVerify_Fix_SyncMergedPak_NonCompileGame_NoWarningEvents proves a
// plain (non-DeployCompile) game under --fix never emits a sync warning
// event - SyncMergedPak's own no-op guard for a non-compile game means
// there is nothing to react to, matching TestSyncMergedPak_NonCompileGame_NoOp.
func TestVerify_Fix_SyncMergedPak_NonCompileGame_NoWarningEvents(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	seedVerifyMod(t, svc, game, "src", "mod-ok", "Mod OK", "1.0", []string{"ok-file"}, true)
	require.NoError(t, svc.SaveFileChecksum("src", "mod-ok", game.ID, "default", "ok-file", "checksum-ok"))

	var events []core.VerifyEvent
	_, err = svc.Verify(context.Background(), game, "default", core.VerifyOptions{Fix: true}, func(e core.VerifyEvent) {
		events = append(events, e)
	})
	require.NoError(t, err)

	for _, e := range events {
		require.NotEqual(t, core.VerifyEvSyncWarning, e.Kind, "a non-DeployCompile game's sync must be a silent no-op")
	}
}

// TestVerify_FullOrder_Integration is this task's full-order fixture: one
// mod each of file_count_mismatch, stale_compile, conversion_failed,
// needs_reingest, missing, plus a stray dangling convergence candidate -
// asserting the EXACT Findings sequence the complete engine produces,
// matching every phase's documented order: file-count pre-pass, merged-pak
// staleness, conversion outcomes, the per-file walk (in
// GetFilesWithChecksums' own row order - fetched directly here, same
// precedent as TestVerify_LocalWalk_StatusesAndCounts), then convergence
// last.
func TestVerify_FullOrder_Integration(t *testing.T) {
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

	gameCache := svc.GetGameCache(game)

	// file_count_mismatch: cache dir exists (still holds the version
	// directory itself) but is empty despite a recorded non-zero FileIDs
	// count - the fileCountPrePass fixture.
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", "fc-mod", "1.0", "fc-file", []byte("x")))
	require.NoError(t, os.Remove(gameCache.GetFilePath(game.ID, "fake-compiler", "fc-mod", "1.0", "fc-file")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "fc-mod", SourceID: "fake-compiler", Name: "FC Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"fc-file"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "fc-mod", Version: "1.0", FileIDs: []string{"fc-file"}}))
	require.NoError(t, svc.SaveFileChecksum("fake-compiler", "fc-mod", game.ID, "default", "fc-file", "checksum-fc"))

	// goodmod converts cleanly; badpak's conversion is scripted to fail -
	// the conversion_failed fixture (TestVerify_CompileGameStatuses' own
	// shape).
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "goodmod", "1.0", "exmodz-file", []byte("good-bytes"))
	seedEnabledPakMod(t, svc, game, "fake-compiler", "badpak", "1.0", "pak", []byte("bad-bytes"))

	// legacypak: a pre-#221 pak cache entry (deployable member, no retained
	// source) - the needs_reingest fixture.
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", "legacypak", "1.0", "legacypak.pak", []byte("legacy-bytes")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "legacypak", SourceID: "fake-compiler", Name: "Legacy Pak", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"pak"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "legacypak", Version: "1.0", FileIDs: []string{"pak"}}))

	// missing-mod: a checksummed row with no cache entry at all - the
	// missing fixture.
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "missing-mod", SourceID: "fake-compiler", Name: "Missing Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"missing-file"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "missing-mod", Version: "1.0", FileIDs: []string{"missing-file"}}))
	require.NoError(t, svc.SaveFileChecksum("fake-compiler", "missing-mod", game.ID, "default", "missing-file", "checksum-missing"))

	// First (only) sync: stamps MergedPakOutcomes with badpak's conversion
	// failure and establishes an up-to-date fingerprint.
	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	// Go stale: enable a third exmodz mod WITHOUT syncing again - the
	// stale_compile fixture.
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolfmod", "1.0", "exmodz-file", []byte("wolf-bytes"))

	// A stray dangling symlink for the convergence pass to find.
	strayDanglingSymlink(t, svc, game, "stray.pak")

	files, err := svc.GetFilesWithChecksums(game.ID, "default")
	require.NoError(t, err)

	result, err := svc.Verify(context.Background(), game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)

	// The per-file walk's own row for every checksummed row, keyed by
	// mod:file (exmodz-file and pak are each shared by two mods above) - the
	// same by-key-then-walk-files-in-order technique
	// TestVerify_LocalWalk_StatusesAndCounts uses, walked in
	// GetFilesWithChecksums' own (fetched-here) row order.
	byModAndFile := map[string]core.VerifyFinding{
		"fc-mod:fc-file":           {ModID: "fc-mod", ModName: "FC Mod", FileID: "fc-file", Status: "ok"},
		"goodmod:exmodz-file":      {ModID: "goodmod", ModName: "goodmod", FileID: "exmodz-file", Status: "no_checksum"},
		"wolfmod:exmodz-file":      {ModID: "wolfmod", ModName: "wolfmod", FileID: "exmodz-file", Status: "no_checksum"},
		"badpak:pak":               {ModID: "badpak", ModName: "badpak", FileID: "pak", Status: "no_checksum"},
		"legacypak:pak":            {ModID: "legacypak", ModName: "Legacy Pak", FileID: "pak", Status: "needs_reingest", Note: "pak predates conversion support - run 'lmm verify --fix' to re-ingest"},
		"missing-mod:missing-file": {ModID: "missing-mod", ModName: "Missing Mod", FileID: "missing-file", Status: "missing", Version: "1.0"},
	}
	var perFileRows []core.VerifyFinding
	for _, f := range files {
		want, ok := byModAndFile[f.ModID+":"+f.FileID]
		require.True(t, ok, "unexpected file row in fixture: %s:%s", f.ModID, f.FileID)
		perFileRows = append(perFileRows, want)
	}

	want := []core.VerifyFinding{
		{ModID: "fc-mod", ModName: "FC Mod", Status: "file_count_mismatch"},
		{ModID: "merged-pak", ModName: "Icarus Merged Pak", Status: "stale_compile", Note: "base pak updated"},
		{ModID: "badpak", ModName: "badpak", Status: "conversion_failed", Note: "irreconcilable pak layout"},
	}
	want = append(want, perFileRows...)
	want = append(want, core.VerifyFinding{FileID: "stray.pak", Status: "stale_deployment", Note: "dangling link into lmm cache"})

	require.Equal(t, want, result.Findings)
}

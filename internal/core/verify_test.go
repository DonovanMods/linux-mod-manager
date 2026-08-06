package core_test

import (
	"context"
	"database/sql"
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

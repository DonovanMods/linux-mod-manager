package core_test

// Tests for Service.ApplyUpdate - the behavior-preserving extraction of
// cmd/lmm/update.go's applyUpdate, per Phase 5b Task 3. See
// internal/core/flows.go's ApplyUpdate/UpdateApplyResult/UpdateOptions doc
// comments for the exact behavior being tested here, and
// .superpowers/sdd/task-3-report.md for the full mapping/decision log.
//
// These tests reuse newFlowsTestService (flows_test.go),
// mockSourceWithDownloads/multiFileDownloadSource (service_test.go/
// flows_install_test.go), and createTestScript (installer_batch_test.go) -
// all in this same core_test package.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// seedUpdatableMod seeds an installed, deployed "old version" mod ready to
// be passed to ApplyUpdate: its cache entry holds files (deployed into the
// game directory), its DB row carries the given FileIDs (seedInstalledMod,
// used by the install-flow tests, doesn't set these - ApplyUpdate's
// FileIDReplacements resolution and downloadedFileIDs bookkeeping both
// depend on them being present), and its profile YAML entry already exists
// (matching the realistic precondition: an update is only ever applied to a
// mod a prior install already upserted into the profile - applyUpdate itself
// never calls the lazy profile-creation helper ApplyInstall does).
func seedUpdatableMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version string, fileIDs []string, files map[string][]byte) *domain.InstalledMod {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, path, content))
	}

	im := &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: name, Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   domain.LinkSymlink,
		FileIDs:      fileIDs,
	}
	require.NoError(t, svc.SaveInstalledMod(im))

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &im.Mod, "default"))

	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		_, cerr := pm.Create(game.ID, "default")
		require.NoError(t, cerr)
	}
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: version, FileIDs: fileIDs}))

	updated, err := svc.GetInstalledMod(sourceID, modID, game.ID, "default")
	require.NoError(t, err)
	return updated
}

// TestService_ApplyUpdate_HappyPathEndToEnd covers ApplyUpdate's base case:
// a new version is fetched, its file downloaded, Replace'd over the old
// deployment, the DB row updated (version, FileIDs, PreviousVersion,
// PreviousFileIDs), the link method persisted, and the profile YAML upserted
// with the new version/FileIDs.
func TestService_ApplyUpdate_HappyPathEndToEnd(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, []string{"Mod One 1.0 → 2.0"}, result.Applied)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)

	// Deployment: old file gone, new file present.
	_, statErr := os.Lstat(filepath.Join(gameDir, "mod1-old.esp"))
	assert.True(t, os.IsNotExist(statErr), "old file must be undeployed")
	newContent, err := os.ReadFile(filepath.Join(gameDir, "mod1-new.esp"))
	require.NoError(t, err, "new file must be deployed")
	assert.Equal(t, "new-content", string(newContent))

	// DB sequencing: version, FileIDs, PreviousVersion/PreviousFileIDs.
	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated.Version)
	assert.Equal(t, []string{"new-1"}, updated.FileIDs)
	assert.Equal(t, "1.0", updated.PreviousVersion)
	assert.Equal(t, []string{"old-1"}, updated.PreviousFileIDs)
	assert.Equal(t, domain.LinkSymlink, updated.LinkMethod)

	// Profile YAML upserted.
	pm := svc.NewProfileManager()
	profile, err := pm.Get("g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "2.0", profile.Mods[0].Version)
	assert.Equal(t, []string{"new-1"}, profile.Mods[0].FileIDs)
}

// TestApplyUpdate_StoredFileIDsGoneUpstream_FallsBackToPrimary guards #95's
// retained update-path fallback (selectDeployFiles's allowFallback=true):
// when the installed mod's stored FileIDs don't appear among the NEW
// version's files at all, and FileIDReplacements offers no substitution,
// ApplyUpdate must still succeed by falling back to the new version's
// primary file - unlike deploy/switch/import, which now hard-fail via
// allowFallback=false. This is correct update semantics: a source pruning
// old file IDs after a version bump (CurseForge routinely does) should
// resolve to the new version's primary file, not an error - see
// selectDeployFiles's doc comment and ApplyUpdate's own
// FileIDReplacements-resolution doc comment for why this differs from the
// other three flows.
func TestApplyUpdate_StoredFileIDsGoneUpstream_FallsBackToPrimary(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	// No FileIDReplacements - the old stored ID "old-1" simply isn't among
	// the new version's files at all, forcing the primary-file fallback.
	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err, "update must succeed via the primary-file fallback, not fail")
	require.NotNil(t, result)
	assert.Equal(t, []string{"Mod One 1.0 → 2.0"}, result.Applied)

	newContent, err := os.ReadFile(filepath.Join(gameDir, "mod1-new.esp"))
	require.NoError(t, err, "the new version's primary file must be deployed")
	assert.Equal(t, "new-content", string(newContent))

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"new-1"}, updated.FileIDs, "the fallback primary file's ID must be recorded")
}

// TestService_ApplyUpdate_HookOrder proves ApplyUpdate's hook ordering
// exactly matches applyUpdate's own: uninstall.before_each (old mod) ->
// install.before_each (new mod) -> Replace -> uninstall.after_each (old
// mod) -> install.after_each (new mod). Unlike ApplyInstall, there is no
// before_all/after_all pair at all - applyUpdate never ran one (each
// CLI-side update-loop iteration calls it once, per mod, with no enclosing
// before_all/after_all of its own).
func TestService_ApplyUpdate_HookOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	callLog := scriptsDir + "/calls.log"
	uninstallBeforeEach := createTestScript(t, scriptsDir, "u_before_each.sh", `#!/bin/bash
echo "uninstall.before_each:$LMM_MOD_ID:$LMM_MOD_VERSION" >> `+callLog+`
exit 0`)
	installBeforeEach := createTestScript(t, scriptsDir, "i_before_each.sh", `#!/bin/bash
echo "install.before_each:$LMM_MOD_ID:$LMM_MOD_VERSION" >> `+callLog+`
exit 0`)
	uninstallAfterEach := createTestScript(t, scriptsDir, "u_after_each.sh", `#!/bin/bash
echo "uninstall.after_each:$LMM_MOD_ID:$LMM_MOD_VERSION" >> `+callLog+`
exit 0`)
	installAfterEach := createTestScript(t, scriptsDir, "i_after_each.sh", `#!/bin/bash
echo "install.after_each:$LMM_MOD_ID:$LMM_MOD_VERSION" >> `+callLog+`
exit 0`)

	hooks := &core.ResolvedHooks{
		Install:   domain.HookConfig{BeforeEach: installBeforeEach, AfterEach: installAfterEach},
		Uninstall: domain.HookConfig{BeforeEach: uninstallBeforeEach, AfterEach: uninstallAfterEach},
	}
	runner := core.NewHookRunner(5 * time.Second)

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{Hooks: hooks, HookRunner: runner}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "uninstall.before_each:mod1:1.0\ninstall.before_each:mod1:2.0\nuninstall.after_each:mod1:1.0\ninstall.after_each:mod1:2.0\n", string(logContent))
}

// TestService_ApplyUpdate_FileIDReplacements covers FileIDReplacements
// resolution, tracing applyUpdate's own logic exactly: each of mod.FileIDs is
// looked up in the replacement map; a HIT substitutes the new ID, a MISS
// retains the ORIGINAL id verbatim (never silently dropped, never defaulted
// to the primary file on its own - selectDeployFiles's own primary-fallback
// only kicks in if NONE of the resulting IDs are found among the new
// version's files at all).
func TestService_ApplyUpdate_FileIDReplacements(t *testing.T) {
	t.Run("replacement present substitutes the new file ID", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old")})

		mock := &multiFileDownloadSource{
			mockSourceWithDownloads: newMockSourceWithDownloads("src"),
			files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
		}
		defer mock.Close()
		svc.RegisterSource(mock)
		mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
		mock.AddDownload("new-1", []byte("new-content"))

		upd := domain.Update{InstalledMod: *old, NewVersion: "2.0", FileIDReplacements: map[string]string{"old-1": "new-1"}}
		result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"Mod One 1.0 → 2.0"}, result.Applied)

		updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, err)
		assert.Equal(t, []string{"new-1"}, updated.FileIDs)
	})

	t.Run("missing replacement retains the original file ID, not a silent drop", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		// Two old file IDs; only "old-1" has a replacement entry. "old-2" has
		// none, so applyUpdate's own logic keeps "old-2" literally - and,
		// crucially, the NEW version's file list still contains a file with
		// that same literal ID, so it resolves directly (no primary
		// fallback needed) - proving the ID was retained, not dropped.
		old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1", "old-2"}, map[string][]byte{"mod1-old.esp": []byte("old")})

		mock := &multiFileDownloadSource{
			mockSourceWithDownloads: newMockSourceWithDownloads("src"),
			files: []domain.DownloadableFile{
				{ID: "new-1", Name: "New File", FileName: "mod1-new.esp"},
				{ID: "old-2", Name: "Unchanged File", FileName: "mod1-extra.esp"},
			},
		}
		defer mock.Close()
		svc.RegisterSource(mock)
		mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
		mock.AddDownload("new-1", []byte("new-main"))
		mock.AddDownload("old-2", []byte("unchanged-extra"))

		upd := domain.Update{InstalledMod: *old, NewVersion: "2.0", FileIDReplacements: map[string]string{"old-1": "new-1"}}
		result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"Mod One 1.0 → 2.0"}, result.Applied)

		updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"new-1", "old-2"}, updated.FileIDs, "the un-replaced ID must be retained verbatim, not dropped or defaulted to primary")
	})

	// PR #142 review, Important 1: the subtest above passes even with a
	// version-narrowing selector, because its fixture files carry no Version
	// labels at all - so the narrowing never engages and the contract is
	// never actually exercised. This is the same case with labels present,
	// which is the shape #143's selectUpdateDeployFiles has to get right:
	// "old-2" is an unchanged extra that legitimately still reports the OLD
	// version's label, and dropping it would silently un-deploy a file the
	// mod is still using. Multi-FileIDs mods are a documented shape
	// (docs/configuration.md), and #96 pins the same "keep the whole stored
	// set" rule for the switch path.
	t.Run("missing replacement retains the original file ID when files carry version labels", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1", "old-2"}, map[string][]byte{"mod1-old.esp": []byte("old")})

		mock := &multiFileDownloadSource{
			mockSourceWithDownloads: newMockSourceWithDownloads("src"),
			files: []domain.DownloadableFile{
				{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", Version: "2.0", IsPrimary: true},
				{ID: "old-2", Name: "Unchanged File", FileName: "mod1-extra.esp", Version: "1.0"},
			},
		}
		defer mock.Close()
		svc.RegisterSource(mock)
		mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
		mock.AddDownload("new-1", []byte("new-main"))
		mock.AddDownload("old-2", []byte("unchanged-extra"))

		upd := domain.Update{InstalledMod: *old, NewVersion: "2.0", FileIDReplacements: map[string]string{"old-1": "new-1"}}
		_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
		require.NoError(t, err)

		updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"new-1", "old-2"}, updated.FileIDs,
			"version labels must not turn the documented retain-verbatim rule into a silent drop")

		_, statErr := os.Stat(filepath.Join(gameDir, "mod1-extra.esp"))
		assert.NoError(t, statErr, "the retained file must still be deployed, not just recorded")
	})

	// PR #142 review, Important 2: an ID that came from a FileIDReplacements
	// hit names a NEW file by construction - it cannot be the stale
	// old-version anchor #143 targets - so the target-version narrowing must
	// never discard it. Reachable on NexusMods, whose CheckUpdates sets
	// NewVersion to the MOD-level version when the mod version moved and a
	// file-update chain exists (internal/source/nexusmods), while a
	// superseded optional/patch file keeps its own, different label.
	t.Run("a superseded file wins over the target version's primary even when its own label differs", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.3", []string{"patch13"}, map[string][]byte{"mod1-patch13.esp": []byte("patch13")})

		mock := &multiFileDownloadSource{
			mockSourceWithDownloads: newMockSourceWithDownloads("src"),
			files: []domain.DownloadableFile{
				{ID: "main20", Name: "Main", FileName: "mod1-main.esp", Version: "2.0", IsPrimary: true},
				{ID: "patch14", Name: "Patch", FileName: "mod1-patch14.esp", Version: "1.4"},
			},
		}
		defer mock.Close()
		svc.RegisterSource(mock)
		mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
		mock.AddDownload("main20", []byte("main-content"))
		mock.AddDownload("patch14", []byte("patch14-content"))

		upd := domain.Update{InstalledMod: *old, NewVersion: "2.0", FileIDReplacements: map[string]string{"patch13": "patch14"}}
		_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
		require.NoError(t, err)

		updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, err)
		assert.Equal(t, []string{"patch14"}, updated.FileIDs,
			"the explicit supersede mapping must outrank the target version's primary file")

		_, statErr := os.Stat(filepath.Join(gameDir, "mod1-patch14.esp"))
		assert.NoError(t, statErr, "the superseding file must be the one deployed")
		_, statErr = os.Stat(filepath.Join(gameDir, "mod1-main.esp"))
		assert.True(t, os.IsNotExist(statErr), "the unrelated main file must not be pulled in")
	})
}

// TestService_ApplyUpdate_RollbackPreconditionPreserved is the mandatory
// rollback-precondition test: `lmm update rollback` depends on the previous
// version's cache entry surviving an update - a silent regression here
// destroys user data recovery. ApplyUpdate must never delete any cache
// entry (Installer.Replace itself never touches the cache, only the game
// directory/deployed-file tracking - see internal/core/installer.go), and
// PreviousVersion must be recorded so doUpdateRollback's own precondition
// checks (mod.PreviousVersion != "" and the cache entry existing) hold.
func TestService_ApplyUpdate_RollbackPreconditionPreserved(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	require.True(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"))

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"), "the previous version's cache entry must survive an update, for rollback")

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.PreviousVersion, "doUpdateRollback's precondition: PreviousVersion must be set")
	assert.True(t, svc.GetGameCache(game).Exists("g1", updated.SourceID, updated.ID, updated.PreviousVersion), "doUpdateRollback's precondition: the previous version must still be cached")
}

// TestService_ApplyUpdate_DownloadFailure covers the download-failure trace
// question: a download failure returns immediately (mirroring applyUpdate's
// own `return fmt.Errorf("downloading update: %w", err)`, reached BEFORE any
// hook runs or Replace happens) - so the old version stays deployed and the
// DB/profile rows are left completely untouched. A partial (empty) result is
// still returned alongside the error, matching the established
// partial-result-on-error convention.
func TestService_ApplyUpdate_DownloadFailure(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	// Deliberately no AddDownload("new-1", ...) - the download 404s.

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "downloading update:")
	require.NotNil(t, result, "a partial result must be returned alongside the error")
	assert.Empty(t, result.Applied)

	oldContent, err2 := os.ReadFile(filepath.Join(gameDir, "mod1-old.esp"))
	require.NoError(t, err2, "the originally-deployed file must survive untouched")
	assert.Equal(t, "old-content", string(oldContent))
	assert.False(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "2.0"))

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version, "DB row must be unchanged")
	assert.Equal(t, "", updated.PreviousVersion)
}

// TestService_ApplyUpdate_ContextCancelledBetweenDownloadAndDeploy_ReturnsPartialResultWithCtxErr
// guards Task 6 item d: ApplyUpdate must check ctx BETWEEN the download step
// and the deploy step (Replace) at minimum - a cancelled ctx aborts there,
// never mid-download and never mid-Replace, leaving the OLD version fully
// deployed and untouched (the partial-result convention -
// TestService_ApplyUpdate_DownloadFailure above pins the identical
// untouched-old-version outcome for a download failure; this is the same
// shape for a cancellation instead). The progress callback cancels on
// UpdateDownloadDone, which fires exactly at that boundary (see
// UpdateDownloadDone's doc comment).
func TestService_ApplyUpdate_ContextCancelledBetweenDownloadAndDeploy_ReturnsPartialResultWithCtxErr(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	ctx, cancel := context.WithCancel(context.Background())
	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	result, err := svc.ApplyUpdate(ctx, game, "default", upd, core.UpdateOptions{}, func(p core.DeployProgress) {
		if p.Phase == core.UpdateDownloadDone {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result, "a partial result must be returned alongside the error")
	assert.Empty(t, result.Applied)

	oldContent, err2 := os.ReadFile(filepath.Join(gameDir, "mod1-old.esp"))
	require.NoError(t, err2, "the OLD version must still be fully deployed - Replace must never have run")
	assert.Equal(t, "old-content", string(oldContent))
	_, statErr := os.Lstat(filepath.Join(gameDir, "mod1-new.esp"))
	assert.True(t, os.IsNotExist(statErr), "the NEW version must never have been deployed")

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version, "DB row must be unchanged")
}

// TestService_ApplyUpdate_ProgressEvents covers the download percent
// sequence with mod attribution, and a nil progress callback being safe.
func TestService_ApplyUpdate_ProgressEvents(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	// Kept under net/http's bufferBeforeChunkingSize (2048 bytes) so the
	// httptest server auto-computes Content-Length instead of switching to
	// chunked transfer encoding - otherwise resp.ContentLength is -1 and
	// UpdateDownloading's TotalBytes>0 gate (matching applyUpdate's own
	// verbose-gated print, which required a known total) never fires.
	mock.AddDownload("new-1", []byte(strings.Repeat("x", 1024)))

	var events []core.DeployProgress
	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	var sawDownloading, sawDone bool
	for _, e := range events {
		switch e.Phase {
		case core.UpdateDownloading:
			sawDownloading = true
			assert.Equal(t, "Mod One", e.ModName)
			assert.Equal(t, "mod1", e.ModID)
			assert.Equal(t, "src", e.SourceID)
			assert.GreaterOrEqual(t, e.Percent, 0.0)
		case core.UpdateDownloadDone:
			sawDone = true
		}
	}
	assert.True(t, sawDownloading, "at least one UpdateDownloading tick expected for a known-size download")
	assert.True(t, sawDone, "UpdateDownloadDone must fire once the download step finishes successfully")

	// A nil progress callback must be safe (no panic) - apply a second,
	// independent update.
	old2 := seedUpdatableMod(t, svc, game, "src", "mod2", "Mod Two", "1.0", []string{"m2-old"}, map[string][]byte{"mod2-old.esp": []byte("old")})
	mock.AddMod("g1", &domain.Mod{ID: "mod2", SourceID: "src", Name: "Mod Two", Version: "2.0", GameID: "g1"})
	upd2 := domain.Update{InstalledMod: *old2, NewVersion: "2.0"}
	_, err = svc.ApplyUpdate(context.Background(), game, "default", upd2, core.UpdateOptions{}, nil)
	require.NoError(t, err)
}

// TestService_ApplyUpdate_GameIDNormalization is the P3-class regression
// test the brief calls for: ApplyUpdate's DB/profile writes must key off the
// GAME's own ID (game.ID) throughout, never a possibly-different GameID a
// source's GetMod stamps onto the freshly-fetched newMod (mirroring how a
// source like NexusMods may map/rewrite GameID for querying purposes - see
// resolveInstallDependencies' gameIDForFetch comment in flows.go). Traced:
// unlike ApplyInstall's SaveInstalledMod (an INSERT that writes a mod's
// GameID column and therefore needs explicit normalization),
// ApplyModUpdate/SetModLinkMethod/UpsertMod are all UPDATES keyed by the
// game.ID argument passed in explicitly - none of them ever reads a GameID
// field off the mod structs - so this test is a guard against future
// regression, not a fix for an existing bug.
func TestService_ApplyUpdate_GameIDNormalization(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	inner := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer inner.Close()
	mock := &gameIDStampingSource{multiFileDownloadSource: inner, stampGameID: "mapped-game-id"}
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err, "the fetched newMod's mismatched GameID must not break the update")
	assert.Equal(t, []string{"Mod One 1.0 → 2.0"}, result.Applied)

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err, "the DB row must still be found under the real game ID")
	assert.Equal(t, "2.0", updated.Version)

	pm := svc.NewProfileManager()
	profile, err := pm.Get("g1", "default")
	require.NoError(t, err, "the profile row must still be found under the real game ID")
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "2.0", profile.Mods[0].Version)
}

// gameIDStampingSource wraps multiFileDownloadSource but stamps a
// caller-chosen (mismatched) GameID onto every Mod GetMod returns, simulating
// a source that maps/rewrites GameID for its own querying purposes (see
// resolveInstallDependencies' gameIDForFetch comment in flows.go).
type gameIDStampingSource struct {
	*multiFileDownloadSource
	stampGameID string
}

func (s *gameIDStampingSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	mod, err := s.multiFileDownloadSource.GetMod(ctx, gameID, modID)
	if err != nil {
		return nil, err
	}
	stamped := *mod
	stamped.GameID = s.stampGameID
	return &stamped, nil
}

// TestService_ApplyUpdate_HookFailureSemantics covers the Force-gate/fatal
// semantics for ApplyUpdate's two before_each hooks (uninstall.before_each
// for the OLD mod, install.before_each for the NEW mod - each Force-gated
// exactly like applyUpdate's own two near-identical checks) and the
// always-non-fatal semantics for its two after_each hooks (uninstall.after_each,
// install.after_each - recorded as Warnings regardless of Force).
func TestService_ApplyUpdate_HookFailureSemantics(t *testing.T) {
	newSetup := func(t *testing.T) (*core.Service, *domain.Game, *domain.InstalledMod, *multiFileDownloadSource) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
		old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
		mock := &multiFileDownloadSource{
			mockSourceWithDownloads: newMockSourceWithDownloads("src"),
			files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
		}
		t.Cleanup(mock.Close)
		svc.RegisterSource(mock)
		mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
		mock.AddDownload("new-1", []byte("new-content"))
		return svc, game, old, mock
	}
	failingScript := func(t *testing.T, dir, name string) string {
		return createTestScript(t, dir, name, "#!/bin/bash\necho boom >&2\nexit 1")
	}

	t.Run("uninstall.before_each fatal without Force", func(t *testing.T) {
		svc, game, old, _ := newSetup(t)
		scriptsDir := t.TempDir()
		hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}}
		runner := core.NewHookRunner(5 * time.Second)

		upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
		result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{Hooks: hooks, HookRunner: runner}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "uninstall.before_each hook failed")
		assert.Empty(t, result.Applied)

		updated, gerr := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "1.0", updated.Version, "a fatal before_each hook must leave the DB row untouched")
	})

	t.Run("uninstall.before_each forced warns and proceeds", func(t *testing.T) {
		svc, game, old, _ := newSetup(t)
		scriptsDir := t.TempDir()
		hooks := &core.ResolvedHooks{Uninstall: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}}
		runner := core.NewHookRunner(5 * time.Second)

		var events []core.DeployProgress
		upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
		result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{Hooks: hooks, HookRunner: runner, Force: true}, func(p core.DeployProgress) {
			events = append(events, p)
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"Mod One 1.0 → 2.0"}, result.Applied)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "uninstall.before_each hook failed (forced):")

		var sawForced bool
		for _, e := range events {
			if e.Phase == core.UpdateBeforeEachForced {
				sawForced = true
				assert.Equal(t, result.Warnings[0], e.Detail)
			}
		}
		assert.True(t, sawForced, "an UpdateBeforeEachForced event must fire")
	})

	t.Run("install.before_each fatal without Force", func(t *testing.T) {
		svc, game, old, _ := newSetup(t)
		scriptsDir := t.TempDir()
		hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}}
		runner := core.NewHookRunner(5 * time.Second)

		upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
		_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{Hooks: hooks, HookRunner: runner}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install.before_each hook failed")
	})

	t.Run("install.before_each forced warns and proceeds", func(t *testing.T) {
		svc, game, old, _ := newSetup(t)
		scriptsDir := t.TempDir()
		hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeEach: failingScript(t, scriptsDir, "fail.sh")}}
		runner := core.NewHookRunner(5 * time.Second)

		upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
		result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{Hooks: hooks, HookRunner: runner, Force: true}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"Mod One 1.0 → 2.0"}, result.Applied)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "install.before_each hook failed (forced):")
	})

	t.Run("after_each hook failures are always non-fatal warnings", func(t *testing.T) {
		svc, game, old, _ := newSetup(t)
		scriptsDir := t.TempDir()
		hooks := &core.ResolvedHooks{
			Uninstall: domain.HookConfig{AfterEach: failingScript(t, scriptsDir, "u_after.sh")},
			Install:   domain.HookConfig{AfterEach: failingScript(t, scriptsDir, "i_after.sh")},
		}
		runner := core.NewHookRunner(5 * time.Second)

		var events []core.DeployProgress
		upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
		result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{Hooks: hooks, HookRunner: runner}, func(p core.DeployProgress) {
			events = append(events, p)
		})
		require.NoError(t, err, "after_each hook failures must never fail the update")
		assert.Equal(t, []string{"Mod One 1.0 → 2.0"}, result.Applied)
		require.Len(t, result.Warnings, 2)
		assert.Contains(t, result.Warnings[0], "uninstall.after_each hook failed")
		assert.Contains(t, result.Warnings[1], "install.after_each hook failed")

		var warningCount int
		for _, e := range events {
			if e.Phase == core.UpdateWarning {
				warningCount++
			}
		}
		assert.Equal(t, 2, warningCount)

		updated, gerr := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, gerr)
		assert.Equal(t, "2.0", updated.Version, "the update itself must still have applied")
	})
}

// fileVersionDivergesSource serves a single file whose Version diverges from
// the mod-level version GetMod reports, simulating the routine NexusMods case
// where a file's own version string (e.g. a beta re-upload) differs from the
// mod page's headline version.
type fileVersionDivergesSource struct{ *mockSourceWithDownloads }

func (s *fileVersionDivergesSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	// Mod-level says "2.0"; the actual file says "2.0b" (routine on NexusMods).
	return []domain.DownloadableFile{
		{ID: "20", Name: "Main", FileName: mod.ID + "-new.esp", Version: "2.0b", IsPrimary: true, Category: "MAIN"},
	}, nil
}

// TestApplyUpdate_RecordsEffectiveFileVersion is the #96/#94 regression test:
// ApplyUpdate must record what was actually installed (the selected file's
// own Version, via domain.EffectiveInstalledVersion) rather than stamping the
// mod-level upd.NewVersion verbatim - the DB row, cache directory key, and
// profile ModReference must all agree with the deployed bytes, matching every
// other install/deploy flow (#94's original fix) instead of being the one
// recording path still stamping the mod-level string.
func TestApplyUpdate_RecordsEffectiveFileVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"10"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	mock := &fileVersionDivergesSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("20", []byte("new-content"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0b", updated.Version, "the file's own version must be recorded, not the mod-level NewVersion")
	assert.Equal(t, "1.0", updated.PreviousVersion, "rollback must still target the prior installed version")

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists("g1", "src", "mod1", "2.0b"), "cache must be keyed by the effective file version")
	assert.False(t, gameCache.Exists("g1", "src", "mod1", "2.0"), "cache must not be keyed by the mod-level version")

	pm := svc.NewProfileManager()
	profile, err := pm.Get("g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "2.0b", profile.Mods[0].Version, "the profile ref must record the effective file version")
}

// TestApplyUpdate_LockedRefRefusesUpdate covers #97's whole contract for
// ApplyUpdate: a locked profile ref refuses the update entirely, before any
// network or hook side effect - nothing downloaded, nothing changed in the
// DB. Reuses the same seed/mock scaffolding as
// TestApplyUpdate_RecordsEffectiveFileVersion, but marks the profile ref
// locked before calling ApplyUpdate.
func TestApplyUpdate_LockedRefRefusesUpdate(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock("g1", "default", "src", "mod1", ""))

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModLocked)
	assert.Contains(t, err.Error(), "locked at v")
	assert.Contains(t, err.Error(), "in profile default", "the remedy must name the profile actually holding the lock (#142 round 4)")
	assert.Contains(t, err.Error(), "lmm mod lock -s src -p default mod1", "the remedy must carry -s/-p so a copy-paste can never resolve against a different source/profile")
	assert.Contains(t, err.Error(), "lmm mod unlock -s src -p default mod1")

	assert.Equal(t, 0, mock.DownloadCount(), "a locked mod must never be downloaded")

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version, "the DB row must be unchanged")
	assert.Equal(t, []string{"old-1"}, updated.FileIDs, "the DB row must be unchanged")

	profile, err := pm.Get("g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "1.0", profile.Mods[0].Version, "the profile ref must be unchanged")
}

// TestApplyUpdate_UnlockedRefStillUpdates is the explicit control for
// TestApplyUpdate_LockedRefRefusesUpdate: an unlocked ref must apply the
// update exactly as before the #97 gate was added.
func TestApplyUpdate_UnlockedRefStillUpdates(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, mock.DownloadCount())

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated.Version)
}

// TestApplyUpdate_OldFileStillListedUpstream_AdvancesToNewVersion is the
// PR #142 smoke-report regression test (root cause is pre-existing in
// v1.25.0, not introduced by #97): a source that keeps its HISTORICAL file
// entries listed alongside the new ones - NexusMods routinely does, and it
// is the norm when an author "rebuilds the mod files" and uploads brand-new
// entries with no FileUpdates chain linking old -> new, so
// upd.FileIDReplacements is empty.
//
// ApplyUpdate's file selection is anchored on the mod's STORED file IDs.
// Those IDs are still present in the new listing, so the version-blind
// selectDeployFiles happily re-selects the ALREADY-INSTALLED 1.0.1 file:
// the "update" re-downloads and re-deploys 1.0.1, EffectiveInstalledVersion
// stamps 1.0.1 back onto the row (previous_version == version, the DB
// signature the user's machine showed), no new cache directory is ever
// created, and the very next update check re-finds the identical update -
// the user's infinite "u -> confirm -> nothing happens" loop, with no error
// surfaced anywhere.
func TestApplyUpdate_OldFileStillListedUpstream_AdvancesToNewVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	// Installed at 1.0.1, recorded against that version's file ID ("101").
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0.1", []string{"101"}, map[string][]byte{"mod1-101.esp": []byte("v101")})

	// The source lists every version's file, oldest first - the historical
	// entries the installed ID still resolves against.
	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "100", Name: "Main 1.0.0", FileName: "mod1-100.esp", Version: "1.0.0", Category: "MAIN"},
			{ID: "101", Name: "Main 1.0.1", FileName: "mod1-101.esp", Version: "1.0.1", Category: "MAIN"},
			{ID: "102", Name: "Main 1.0.2", FileName: "mod1-102.esp", Version: "1.0.2", Category: "MAIN"},
			{ID: "103", Name: "Main 1.0.3", FileName: "mod1-103.esp", Version: "1.0.3", IsPrimary: true, Category: "MAIN"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0.3", GameID: "g1"})
	// Every listed file is downloadable, exactly like upstream - so a
	// wrong selection fails SILENTLY (the smoke report's "no error I
	// noticed") instead of 404-ing.
	mock.AddDownload("101", []byte("v101"))
	mock.AddDownload("103", []byte("v103"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "1.0.3"}
	_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0.3", updated.Version, "the update must advance the recorded version, not re-stamp the installed one")
	assert.Equal(t, []string{"103"}, updated.FileIDs, "the NEW version's file must be selected, not the still-listed old one")
	assert.Equal(t, "1.0.1", updated.PreviousVersion, "previous_version must be the version actually superseded")
	assert.NotEqual(t, updated.Version, updated.PreviousVersion,
		"previous_version == version is the smoke report's DB signature for a no-op 'update'")

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists("g1", "src", "mod1", "1.0.3"), "the new version must be cached under its own key")

	newContent, err := os.ReadFile(filepath.Join(gameDir, "mod1-103.esp"))
	require.NoError(t, err, "the new version's file must be deployed")
	assert.Equal(t, "v103", string(newContent))

	pm := svc.NewProfileManager()
	profile, err := pm.Get("g1", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "1.0.3", profile.Mods[0].Version, "the profile ref must advance too")

	// The loop actually closes: a fresh check must now find nothing (the
	// user's "press u again, same three updates" symptom).
	//
	// PR #142 review round 2: this must run against a source whose
	// CheckUpdates actually COMPARES versions. mockSourceWithDownloads'
	// returns nil unconditionally, so asserting Empty on it passed for any
	// selection whatsoever - the assertion did no work. updateMockSource
	// (updater_test.go) reports an update iff the installed version differs
	// from the source's current version, which is exactly the comparison
	// the real loop hinges on.
	loopRegistry := source.NewRegistry()
	loopRegistry.Register(&updateMockSource{
		id:         "src",
		currentMod: &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0.3", GameID: "g1"},
	})
	again, err := core.NewUpdater(loopRegistry).CheckUpdates(context.Background(), game, []domain.InstalledMod{*updated})
	require.NoError(t, err)
	assert.Empty(t, again, "a re-check after a successful update must find no further update")

	// ...and the same check DOES still fire for a row left on the old
	// version, proving the assertion above can actually fail.
	stale := *updated
	stale.Version = "1.0.1"
	staleAgain, err := core.NewUpdater(loopRegistry).CheckUpdates(context.Background(), game, []domain.InstalledMod{stale})
	require.NoError(t, err)
	require.Len(t, staleAgain, 1, "control: a row still on 1.0.1 must still be reported as updatable")

	// And `lmm verify` agrees: the version record matches what the STORED
	// file IDs actually are, so `--fix` has nothing to repair downward.
	// (Before this fix, the record and the stored IDs disagreed, which is
	// what made verify --fix knock these very mods back down - see
	// cmd/lmm/verify.go's EffectiveInstalledVersion comparison.)
	sourceFiles, err := svc.GetModFiles(context.Background(), "src", &updated.Mod)
	require.NoError(t, err)
	var matched []*domain.DownloadableFile
	for _, id := range updated.FileIDs {
		for i := range sourceFiles {
			if sourceFiles[i].ID == id {
				matched = append(matched, &sourceFiles[i])
				break
			}
		}
	}
	require.NotEmpty(t, matched, "the recorded file IDs must still resolve upstream")
	assert.Equal(t, updated.Version, domain.EffectiveInstalledVersion(updated.Version, matched),
		"verify must see no VERSION MISMATCH, so --fix cannot repair the record back downward")
}

// TestApplyUpdate_PartialFileIDReplacements_StillAdvances is PR #142 review
// round 2, Important 1. A PARTIAL FileIDReplacements map is the NORM, not an
// edge case: internal/source/nexusmods adds a map entry only for stored files
// that actually have a FileUpdates chain, so any multi-file mod with one
// superseded-in-place file and one rebuilt file produces a map covering some
// stored IDs and not others.
//
// Round 1's per-UPDATE discriminator ("any mapping at all disables the
// narrowing") therefore switched the #143 fix off for exactly the mods most
// likely to hit it, reproducing the original loop signature. The
// discriminator is now per-FILE: a replacement HIT is authoritative and
// always kept, while an uncovered stored ID goes through the narrowing.
//
// Expected outcome, reasoning from both contracts at once: extra999 is
// explicitly superseded, so extra1000 is kept on the map's authority even
// though its label is not consulted. main101 is uncovered and still listed
// at exactly the installed version - the stale anchor - so it is replaced by
// the target version's primary, main103. The mod keeps its two-file shape,
// both files land on 1.0.3, and the record advances.
func TestApplyUpdate_PartialFileIDReplacements_StillAdvances(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0.1",
		[]string{"main101", "extra999"},
		map[string][]byte{"mod1-main101.esp": []byte("main101"), "mod1-extra999.esp": []byte("extra999")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "main101", Name: "Main 1.0.1", FileName: "mod1-main101.esp", Version: "1.0.1", Category: "MAIN"},
			{ID: "extra999", Name: "Extra 1.0.1", FileName: "mod1-extra999.esp", Version: "1.0.1", Category: "OPTIONAL"},
			{ID: "main103", Name: "Main 1.0.3", FileName: "mod1-main103.esp", Version: "1.0.3", IsPrimary: true, Category: "MAIN"},
			{ID: "extra1000", Name: "Extra 1.0.3", FileName: "mod1-extra1000.esp", Version: "1.0.3", Category: "OPTIONAL"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0.3", GameID: "g1"})
	mock.AddDownload("main103", []byte("main103-content"))
	mock.AddDownload("extra1000", []byte("extra1000-content"))
	mock.AddDownload("main101", []byte("main101"))
	mock.AddDownload("extra999", []byte("extra999"))

	upd := domain.Update{
		InstalledMod:       *old,
		NewVersion:         "1.0.3",
		FileIDReplacements: map[string]string{"extra999": "extra1000"},
	}
	_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0.3", updated.Version, "a partial map must not switch the #143 fix off")
	assert.ElementsMatch(t, []string{"extra1000", "main103"}, updated.FileIDs,
		"the mapped file is kept on the map's authority; the uncovered stale anchor is replaced by the target primary")
	assert.NotEqual(t, updated.Version, updated.PreviousVersion, "the original loop signature must not reappear")

	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-main103.esp"))
	assert.NoError(t, statErr, "the target version's main file must be deployed")
	_, statErr = os.Stat(filepath.Join(gameDir, "mod1-main101.esp"))
	assert.True(t, os.IsNotExist(statErr), "the stale anchor must not stay deployed alongside it")
}

// TestApplyUpdate_LabelAmbiguousExtra_IsSurfacedAsAWarning covers PR #142
// review round 2, Important 2. With no mapping at all, two stored files both
// still listed at exactly the installed version are label-INDISTINGUISHABLE:
// either could be the stale anchor, either could be a genuine unchanged
// extra. The target version offers one unselected file, so exactly one anchor
// can be replaced 1:1; the other cannot be resolved either way.
//
// Per the round-2 ruling this ambiguity must never be silent. See
// selectUpdateDeployFiles' doc comment for why the unresolvable file is
// RETAINED-and-warned rather than dropped-and-warned (retaining cannot cause
// the loop the ruling exists to prevent - guardNoOpUpdateSelection proves
// that independently - while dropping would silently un-deploy a file the
// mod is still using, which is the round-1 regression).
func TestApplyUpdate_LabelAmbiguousExtra_IsSurfacedAsAWarning(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0.1",
		[]string{"main101", "extra999"},
		map[string][]byte{"mod1-main101.esp": []byte("main101"), "mod1-extra999.esp": []byte("extra999")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "main101", Name: "Main 1.0.1", FileName: "mod1-main101.esp", Version: "1.0.1", Category: "MAIN"},
			{ID: "extra999", Name: "Extra 1.0.1", FileName: "mod1-extra999.esp", Version: "1.0.1", Category: "OPTIONAL"},
			{ID: "main103", Name: "Main 1.0.3", FileName: "mod1-main103.esp", Version: "1.0.3", IsPrimary: true, Category: "MAIN"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0.3", GameID: "g1"})
	mock.AddDownload("main103", []byte("main103-content"))
	mock.AddDownload("extra999", []byte("extra999"))
	mock.AddDownload("main101", []byte("main101"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "1.0.3"}
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0.3", updated.Version, "the record must still advance")

	// PR #142 review round 3: WHICH ambiguous file the single target file
	// stands in for is decided by Category, not by list order. main103 is
	// MAIN, so it pairs with main101 (MAIN) and the OPTIONAL extra999 is the
	// one retained-and-warned. Before this, the pairing consumed DB order and
	// could just as easily replace the OPTIONAL and retain the stale MAIN -
	// leaving the old main pak deployed beside the new one, wrong half the
	// time.
	assert.ElementsMatch(t, []string{"main103", "extra999"}, updated.FileIDs,
		"the target MAIN must replace the stale MAIN, retaining the OPTIONAL")

	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-main103.esp"))
	assert.NoError(t, statErr, "the new main file must be deployed")
	_, statErr = os.Stat(filepath.Join(gameDir, "mod1-main101.esp"))
	assert.True(t, os.IsNotExist(statErr), "the stale main must NOT be double-deployed beside the new one")

	require.NotEmpty(t, result.Warnings, "a label-ambiguous stored file must never be resolved silently")
	joined := strings.Join(result.Warnings, "\n")
	assert.Contains(t, joined, "extra999", "the warning must name the file that was retained unresolved")
	assert.Contains(t, joined, "1.0.1", "the warning must name the version that makes it ambiguous")
	assert.NotContains(t, joined, "main103", "the cleanly replaced file is not ambiguous and must not be warned about")
}

// TestApplyUpdate_FileOnlyUpdate_SameVersionStringApplies is PR #142 review
// round 3, Important: "the selection's effective version equals the installed
// version" is NOT proof of a no-op.
//
// internal/source/nexusmods reports an update when the mod version moved OR
// any installed file was superseded. In the second class (hasFileUpdate &&
// !modVersionNewer) it sets NewVersion to the new FILE's own version, which
// is routinely the SAME string as the installed one - the author re-uploaded
// a fixed archive under an unchanged version label. That is a real update
// with real new bytes, and it must apply.
//
// The round-2 backstop hard-failed exactly this shape ("would re-install the
// installed version"), a regression against v1.25.0. The true no-op test is
// "the selection IS what is already installed" - an ID-set comparison - not a
// version-string comparison.
func TestApplyUpdate_FileOnlyUpdate_SameVersionStringApplies(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"fileA"}, map[string][]byte{"mod1-fileA.esp": []byte("A")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "fileA", Name: "Old Archive", FileName: "mod1-fileA.esp", Version: "1.0", Category: "MAIN"},
			{ID: "fileB", Name: "Fixed Archive", FileName: "mod1-fileB.esp", Version: "1.0", IsPrimary: true, Category: "MAIN"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	mock.AddDownload("fileB", []byte("B-content"))

	upd := domain.Update{
		InstalledMod:       *old,
		NewVersion:         "1.0",
		FileIDReplacements: map[string]string{"fileA": "fileB"},
	}
	_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err, "a file-only update whose version string is unchanged must still apply")

	updated, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"fileB"}, updated.FileIDs, "the superseding file must be recorded")

	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-fileB.esp"))
	assert.NoError(t, statErr, "the new file must be deployed")

	// Deliberately NOT asserting that mod1-fileA.esp is undeployed. The cache
	// is keyed by version (#94/#96), so a file-only update - whose version
	// string does not change - shares ONE cache directory between the old and
	// new files, and Installer.Replace deploys whatever that directory holds.
	// That is pre-existing behavior of the cache keying, identical on v1.25.0
	// and independent of file selection; it is out of scope here. See §9 of
	// the smoke-bug report.
}

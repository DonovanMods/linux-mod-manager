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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

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
	require.NoError(t, svc.SaveInstalledMod(context.Background(), im))

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &im.Mod, "default"))

	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		_, cerr := pm.Create(game.ID, "default")
		require.NoError(t, cerr)
	}
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: version, FileIDs: fileIDs}))

	updated, err := svc.GetInstalledMod(context.Background(), sourceID, modID, game.ID, "default")
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
	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

		updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

		updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

		updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

		updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

// setupTwoFileUpdate is TestService_ApplyUpdate_ProgressEvents' fixture with
// a 2-file replacement (multiFileDownloadSource, two entries) so a per-file
// cancellation test has more than one file to observe. The old mod's own
// FileIDs are stamped to match the two new file IDs directly: with
// installedVersion/targetVersion both set and neither served file carrying
// an explicit Version, resolveUpdateSelection's classification falls
// straight through to selectDeployFiles, which returns every stored-ID match
// verbatim - the simplest way to pin an exact 2-file selection without
// fighting the version-drift heuristics the ambiguous-file path exists for.
func setupTwoFileUpdate(t *testing.T) (*core.Service, *domain.Game, *multiFileDownloadSource, domain.Update) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"new-1", "new-2"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	src := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "new-1", Name: "New File 1", FileName: "mod1-new-1.esp", IsPrimary: true},
			{ID: "new-2", Name: "New File 2", FileName: "mod1-new-2.esp"},
		},
	}
	svc.RegisterSource(src)
	src.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	src.AddDownload("new-1", []byte("new-1-content"))
	src.AddDownload("new-2", []byte("new-2-content"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	return svc, game, src, upd
}

// ctxBlindTransport forwards every request with a fresh background context,
// so cancelling the caller's ctx cannot abort an in-flight download or make
// the client refuse the next one. It exists to take the ctx-aware transport
// OUT of a per-file cancellation test: with it in place, the caller's loop
// guard is the only thing left that can stop the loop, which is exactly what
// such a test claims to pin (final-review Important 2).
type ctxBlindTransport struct{ base http.RoundTripper }

func (t ctxBlindTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req.WithContext(context.Background()))
}

// TestService_ApplyUpdate_ContextCancelledBetweenDownloads pins that
// ApplyUpdate's download loop checks ctx per file, not only after the loop:
// with a two-file update and a ctx cancelled once file 1 has been served,
// file 2's iteration never starts.
//
// ApplyUpdate emits nothing between files, so - unlike the install-side test
// - there is no sink event to hang the cancel off; the cancel stays on the
// server hook and the transport is neutered instead (technique (a)):
//
//   - a ctx-blind transport means file 1 completes for real even though the
//     hook cancels while its response is still in flight, and means a file 2
//     that the loop wrongly started would genuinely be fetched rather than
//     refused by the client;
//   - the assertion is on GetDownloadURL calls, the first thing
//     DownloadModToCache does for a file and the earliest observable proof
//     that an iteration ran at all.
//
// Nothing else on file 1's path consults ctx (no existing cache entry to
// seed from, a non-archive payload, so no prepareStaging copy and no
// extraction), so a cancelled ctx cannot make file 1 fail by itself.
func TestService_ApplyUpdate_ContextCancelledBetweenDownloads(t *testing.T) {
	svc, game, src, upd := setupTwoFileUpdate(t)
	svc.SetDownloadClientForTest(&http.Client{Transport: ctxBlindTransport{base: http.DefaultTransport}})

	ctx, cancel := context.WithCancel(context.Background())
	src.onDownload = func() { cancel() } // fires once file 1's bytes are written

	_, err := svc.ApplyUpdate(ctx, game, "default", upd, core.UpdateOptions{}, nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int64(1), src.urlRequests.Load(), "file 2's iteration must never start: the loop head, not the transport, has to stop it")
	assert.Equal(t, int64(1), src.downloads.Load())
}

// TestService_ApplyUpdate_GameIDNormalization is the P3-class regression
// test the brief calls for: ApplyUpdate's DB/profile writes must key off the
// GAME's own ID (game.ID) throughout, never a possibly-different GameID a
// source's GetMod stamps onto the freshly-fetched newMod (mirroring how a
// source like NexusMods may map/rewrite GameID for querying purposes - see
// resolveInstallDependencies' gameID doc comment in flows.go). Traced:
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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
// resolveInstallDependencies' gameID doc comment in flows.go).
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

		updated, gerr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

		updated, gerr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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
	again, err := core.NewUpdater(loopRegistry).CheckUpdates(context.Background(), game, []domain.InstalledMod{*updated}, nil)
	require.NoError(t, err)
	assert.Empty(t, again, "a re-check after a successful update must find no further update")

	// ...and the same check DOES still fire for a row left on the old
	// version, proving the assertion above can actually fail.
	stale := *updated
	stale.Version = "1.0.1"
	staleAgain, err := core.NewUpdater(loopRegistry).CheckUpdates(context.Background(), game, []domain.InstalledMod{stale}, nil)
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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

// TestApplyUpdate_CategoryLessAmbiguousPair_PrimaryBreaksTie covers #144
// item 1, case (a): custom sources (directory/manifest/api) NEVER populate
// DownloadableFile.Category, so the Category pairing that
// TestApplyUpdate_LabelAmbiguousExtra_IsSurfacedAsAWarning relies on decides
// nothing and pairing used to fall straight to list order - a coin flip on
// which stored file the single target-version file replaces. With the
// unchanged extra listed before the stale main (as here), list order consumed
// the EXTRA and retained the stale main: old main pak deployed beside the new
// one, exactly the double-deploy the round-3 Category pairing exists to
// prevent. IsPrimary is the secondary signal (#144): custom sources DO set it
// (directory always, manifest per-file via `primary:`, api for single-file
// mods), so the primary replacement pairs with the primary ambiguous entry.
func TestApplyUpdate_CategoryLessAmbiguousPair_PrimaryBreaksTie(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	// Stored order lists the extra FIRST so the pre-#144 list-order pairing
	// provably consumes the wrong entry.
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0.1",
		[]string{"extra101", "main101"},
		map[string][]byte{"mod1-extra101.esp": []byte("extra101"), "mod1-main101.esp": []byte("main101")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "extra101", Name: "Extra 1.0.1", FileName: "mod1-extra101.esp", Version: "1.0.1"},
			{ID: "main101", Name: "Main 1.0.1", FileName: "mod1-main101.esp", Version: "1.0.1", IsPrimary: true},
			{ID: "main103", Name: "Main 1.0.3", FileName: "mod1-main103.esp", Version: "1.0.3", IsPrimary: true},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0.3", GameID: "g1"})
	mock.AddDownload("main103", []byte("main103-content"))
	mock.AddDownload("extra101", []byte("extra101"))
	mock.AddDownload("main101", []byte("main101"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "1.0.3"}
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0.3", updated.Version, "the record must still advance")
	assert.ElementsMatch(t, []string{"main103", "extra101"}, updated.FileIDs,
		"the primary target file must replace the stale primary, retaining the category-less extra")

	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-main103.esp"))
	assert.NoError(t, statErr, "the new main file must be deployed")
	_, statErr = os.Stat(filepath.Join(gameDir, "mod1-main101.esp"))
	assert.True(t, os.IsNotExist(statErr), "the stale main must NOT be double-deployed beside the new one")
	_, statErr = os.Stat(filepath.Join(gameDir, "mod1-extra101.esp"))
	assert.NoError(t, statErr, "the retained extra must stay deployed")

	require.NotEmpty(t, result.Warnings, "the retained ambiguous file must still be surfaced")
	joined := strings.Join(result.Warnings, "\n")
	assert.Contains(t, joined, "extra101", "the warning must name the retained extra, not the replaced main")
	assert.NotContains(t, joined, "main101", "the cleanly replaced stale main must not be warned about")
}

// TestApplyUpdate_NonMatchingCategoryAmbiguousPair_PrimaryBreaksTie covers
// #144 item 1, case (b) - the reviewer-confirmed sibling of the category-less
// case: Category can be POPULATED and still decide nothing, because nothing
// guarantees the replacement's category appears among the ambiguous entries
// at all. This fixture's vocabulary borrows CurseForge's release types
// (release/beta/alpha, from releaseTypeName), which routinely shift across
// versions - but note its FLAG pattern is manifest-like (two IsPrimary files
// in one listing): real CurseForge marks only the globally-first file primary
// (IsPrimary: i == 0), so there the IsPrimary loop finds no match and
// behavior stays list order, unchanged. The fix's real-world winner for
// case (b) is a manifest/directory source with per-file primary flags.
// Pre-#144 the inner Category loop fell out to list order, same coin
// flip as case (a); IsPrimary must break the tie here too.
func TestApplyUpdate_NonMatchingCategoryAmbiguousPair_PrimaryBreaksTie(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0",
		[]string{"extra-old", "main-old"},
		map[string][]byte{"mod1-extra-old.esp": []byte("extra-old"), "mod1-main-old.esp": []byte("main-old")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "extra-old", Name: "Extra 1.0", FileName: "mod1-extra-old.esp", Version: "1.0", Category: "alpha"},
			{ID: "main-old", Name: "Main 1.0", FileName: "mod1-main-old.esp", Version: "1.0", Category: "beta", IsPrimary: true},
			{ID: "main-new", Name: "Main 2.0", FileName: "mod1-main-new.esp", Version: "2.0", Category: "release", IsPrimary: true},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("main-new", []byte("main-new-content"))
	mock.AddDownload("extra-old", []byte("extra-old"))
	mock.AddDownload("main-old", []byte("main-old"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated.Version, "the record must still advance")
	assert.ElementsMatch(t, []string{"main-new", "extra-old"}, updated.FileIDs,
		"a populated-but-non-matching Category must not fall to list order: the primary pairing wins")

	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-main-new.esp"))
	assert.NoError(t, statErr, "the new main file must be deployed")
	_, statErr = os.Stat(filepath.Join(gameDir, "mod1-main-old.esp"))
	assert.True(t, os.IsNotExist(statErr), "the stale main must NOT be double-deployed beside the new one")

	require.NotEmpty(t, result.Warnings)
	joined := strings.Join(result.Warnings, "\n")
	assert.Contains(t, joined, "extra-old", "the warning must name the retained extra")
	assert.NotContains(t, joined, "main-old", "the cleanly replaced stale main must not be warned about")
}

// TestApplyUpdate_NoOpGuard_NothingNewUnderTarget_ErrorsWithLabellingHint
// covers #144 item 2: guardNoOpUpdateSelection's error branch had NO test -
// a refactor could have silently neutered the backstop that stops the
// re-install loop. This is the reviewer-probed reachable shape for its
// "nothing to add" (!added) sub-case: the installed mod holds the old
// primary (still labelled the installed version) AND the target version's
// only file (an optional installed earlier); the update fires because the
// mod-level version moved, but every file the source offers under the
// target is already installed, the repair drops the old primary and finds
// nothing new to add, and the record provably cannot advance. A loud error
// is correct - silently proceeding is the infinite loop #142 fixed.
//
// The wording is the branch-specific one (#144): the user already holds
// everything the source offers under the target, so "reinstall the mod or
// use --file to pick one explicitly" would be misleading - there is nothing
// else to pick. The error must instead point at the source's file labelling
// offering nothing new. The error surfaces during selection, before any
// download, hook, or write - the old deployment and records stay untouched.
func TestApplyUpdate_NoOpGuard_NothingNewUnderTarget_ErrorsWithLabellingHint(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0",
		[]string{"main-old", "opt-new"},
		map[string][]byte{"mod1-main-old.esp": []byte("main-old"), "mod1-opt-new.esp": []byte("opt-new")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "main-old", Name: "Main 1.0", FileName: "mod1-main-old.esp", Version: "1.0", IsPrimary: true},
			{ID: "opt-new", Name: "Optional 2.0", FileName: "mod1-opt-new.esp", Version: "2.0"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})

	upd := domain.Update{InstalledMod: *old, NewVersion: "2.0"}
	_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.Error(t, err, "a selection that provably cannot advance the record must fail loudly, not loop")
	assert.Contains(t, err.Error(), `update to "2.0" would re-install exactly what is already installed`)
	assert.Contains(t, err.Error(), "every file the source offers under \"2.0\" is already installed",
		"the !added branch must say the source offers nothing new")
	assert.Contains(t, err.Error(), "file labelling",
		"the !added branch must point at the source-side labelling quirk")
	assert.NotContains(t, err.Error(), "use --file to pick one explicitly",
		"the update-side pick-another-file remedy belongs to the added-but-not-advancing branch only")
	assert.Contains(t, err.Error(), "lmm install --file",
		"the one honest remedy in this shape: reinstall keeping only the wanted file undeploys the stale one and advances the record")

	// The error fires during selection - before any hook, download, or write -
	// so the old version must remain fully installed and deployed.
	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version, "the record must be untouched")
	assert.ElementsMatch(t, []string{"main-old", "opt-new"}, updated.FileIDs)
	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-main-old.esp"))
	assert.NoError(t, statErr, "the old deployment must be untouched")
}

// TestApplyUpdate_NoOpGuard_RepairStillNotAdvancing_ErrorsWithFileHint is the
// companion to the !added test above, pinning the guard's OTHER error
// sub-case (#144 item 2): the repair DOES add a target-version file, but the
// repaired selection's effective version still equals the installed one.
// Reachable end-to-end as a same-version-string "update" with no
// FileIDReplacements map: the source lists a second file under the very
// version installed, the ambiguous classification re-selects the installed
// primary (it IS the version's primary), and the repair drops it only to
// re-add it as the version's best pick. Here the source genuinely does offer
// another file (fileA), the user just has to choose it - so the established
// "reinstall the mod or use --file to pick one explicitly" remedy stays.
func TestApplyUpdate_NoOpGuard_RepairStillNotAdvancing_ErrorsWithFileHint(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0",
		[]string{"fileB"}, map[string][]byte{"mod1-fileB.esp": []byte("B")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "fileA", Name: "Alt Archive", FileName: "mod1-fileA.esp", Version: "1.0"},
			{ID: "fileB", Name: "Main Archive", FileName: "mod1-fileB.esp", Version: "1.0", IsPrimary: true},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	upd := domain.Update{InstalledMod: *old, NewVersion: "1.0"}
	_, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.Error(t, err, "re-selecting exactly the installed file must fail loudly, not loop")
	assert.Contains(t, err.Error(), `update to "1.0" would re-install exactly what is already installed`)
	assert.Contains(t, err.Error(), "reinstall the mod or use --file to pick one explicitly",
		"when the source does offer other files, the pick-one-explicitly remedy stays")
	assert.NotContains(t, err.Error(), "labelling",
		"the labelling-quirk hint belongs to the nothing-new-to-add branch only")

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version)
	assert.Equal(t, []string{"fileB"}, updated.FileIDs, "the record must be untouched")
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

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"fileB"}, updated.FileIDs, "the superseding file must be recorded")

	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-fileB.esp"))
	assert.NoError(t, statErr, "the new file must be deployed")

	// Deliberately NOT asserting anything about mod1-fileA.esp here. The cache
	// is keyed by version (#94/#96), so a file-only update - whose version
	// string does not change - shares ONE cache directory between the old and
	// new files. This mod was seeded WITHOUT member manifests (the legacy
	// pre-manifest cache shape), so Installer.Replace falls back to deploying
	// the union - see §9 of the smoke-bug report for the original behavior,
	// and TestApplyUpdate_SameVersionFileOnlyUpdate_UndeploysSupersededMember
	// (#144 item 4) for the manifest-backed shape where the superseded file
	// IS undeployed.
}

// --- #144 item 4: same-version cache sharing on file-only updates ---

// seedSameVersionManifest stamps fileID's member manifest onto the seeded old
// mod's cache entry, upgrading seedUpdatableMod's legacy (marker-less) seed to
// the shape every real install has written since manifests were introduced.
func seedSameVersionManifest(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version, fileID string, members []string) {
	t.Helper()
	versionDir := svc.GetGameCache(game).ModPath(game.ID, sourceID, modID, version)
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, fileID, members))
}

// TestApplyUpdate_SameVersionFileOnlyUpdate_UndeploysSupersededMember flips
// the stance TestApplyUpdate_FileOnlyUpdate_SameVersionStringApplies
// deliberately declined: with member manifests on both sides, a file-only
// update whose version string does not change (one SHARED cache dir - the
// version-keyed cache cannot tell the old and new files apart) must undeploy
// the superseded file's members instead of leaving the union deployed
// (pre-existing bug, 9047992-era; #144 item 4).
func TestApplyUpdate_SameVersionFileOnlyUpdate_UndeploysSupersededMember(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"fileA"}, map[string][]byte{"mod1-fileA.esp": []byte("A")})
	seedSameVersionManifest(t, svc, game, "src", "mod1", "1.0", "fileA", []string{"mod1-fileA.esp"})

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
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Warnings)

	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-fileB.esp"))
	assert.NoError(t, statErr, "the new file must be deployed")
	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileA.esp"))
	assert.True(t, os.IsNotExist(statErr),
		"the superseded file's member must be UNDEPLOYED despite the shared same-version cache dir")

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"fileB"}, updated.FileIDs)
}

// TestApplyUpdate_SameVersionFileOnlyUpdate_SharedMemberSurvives: a member
// shipped by BOTH the superseded file and its replacement (the new archive
// overwrites it in the shared dir) is not solely owned and must stay
// deployed. Downloading the replacement as a real ZIP also exercises the
// DeployExtract capture path end to end: the manifest records EXTRACTED
// member names, unrelated to the archive's own FileName.
func TestApplyUpdate_SameVersionFileOnlyUpdate_SharedMemberSurvives(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"fileA"},
		map[string][]byte{"shared.esp": []byte("shared"), "a-only.esp": []byte("a")})
	seedSameVersionManifest(t, svc, game, "src", "mod1", "1.0", "fileA", []string{"shared.esp", "a-only.esp"})

	zipB, err := os.ReadFile(createTestZip(t, t.TempDir(), map[string]string{"shared.esp": "shared-v2", "b-only.esp": "b"}))
	require.NoError(t, err)

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "fileA", Name: "Old Archive", FileName: "mod1-fileA.zip", Version: "1.0", Category: "MAIN"},
			{ID: "fileB", Name: "Fixed Archive", FileName: "mod1-fileB.zip", Version: "1.0", IsPrimary: true, Category: "MAIN"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	mock.AddDownload("fileB", zipB)

	upd := domain.Update{
		InstalledMod:       *old,
		NewVersion:         "1.0",
		FileIDReplacements: map[string]string{"fileA": "fileB"},
	}
	_, err = svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	_, statErr := os.Lstat(filepath.Join(gameDir, "a-only.esp"))
	assert.True(t, os.IsNotExist(statErr), "the superseded file's sole member must be undeployed")
	sharedContent, err := os.ReadFile(filepath.Join(gameDir, "shared.esp"))
	require.NoError(t, err, "a member also listed in the surviving file's manifest must stay deployed")
	assert.Equal(t, "shared-v2", string(sharedContent), "the shared member carries the new file's content")
	_, statErr = os.Stat(filepath.Join(gameDir, "b-only.esp"))
	assert.NoError(t, statErr, "the new file's member must be deployed")
}

// TestApplyUpdate_SameVersionFileOnlyUpdate_ChainedUpdatesStayUndeployed:
// two same-version file-only updates in a row (A superseded by B, then B by
// C) share the SAME version dir throughout, and every generation's marker
// stays behind. A's stale marker must not act as a "survivor" protecting
// a.esp in the second update - the survivor set is the mod's CURRENT file
// IDs, not every marker present - or update 2 would re-deploy the member
// update 1 correctly removed, and it would persist forever (A never returns
// to the installed set).
func TestApplyUpdate_SameVersionFileOnlyUpdate_ChainedUpdatesStayUndeployed(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"fileA"}, map[string][]byte{"mod1-fileA.esp": []byte("A")})
	seedSameVersionManifest(t, svc, game, "src", "mod1", "1.0", "fileA", []string{"mod1-fileA.esp"})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "fileA", Name: "Archive r1", FileName: "mod1-fileA.esp", Version: "1.0", Category: "MAIN"},
			{ID: "fileB", Name: "Archive r2", FileName: "mod1-fileB.esp", Version: "1.0", Category: "MAIN"},
			{ID: "fileC", Name: "Archive r3", FileName: "mod1-fileC.esp", Version: "1.0", IsPrimary: true, Category: "MAIN"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	mock.AddDownload("fileB", []byte("B-content"))
	mock.AddDownload("fileC", []byte("C-content"))

	// Update 1: A -> B.
	upd1 := domain.Update{InstalledMod: *old, NewVersion: "1.0", FileIDReplacements: map[string]string{"fileA": "fileB"}}
	_, err := svc.ApplyUpdate(context.Background(), game, "default", upd1, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	_, statErr := os.Lstat(filepath.Join(gameDir, "mod1-fileA.esp"))
	require.True(t, os.IsNotExist(statErr), "update 1 must undeploy A's member")

	// Update 2: B -> C, against the reloaded record.
	mid, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	require.Equal(t, []string{"fileB"}, mid.FileIDs)
	upd2 := domain.Update{InstalledMod: *mid, NewVersion: "1.0", FileIDReplacements: map[string]string{"fileB": "fileC"}}
	_, err = svc.ApplyUpdate(context.Background(), game, "default", upd2, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	_, statErr = os.Stat(filepath.Join(gameDir, "mod1-fileC.esp"))
	assert.NoError(t, statErr, "update 2 must deploy C's member")
	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileB.esp"))
	assert.True(t, os.IsNotExist(statErr), "update 2 must undeploy B's member")
	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileA.esp"))
	assert.True(t, os.IsNotExist(statErr),
		"A's STALE marker must not resurrect a.esp - survivors are the current file IDs, not every marker in the dir")

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"fileC"}, updated.FileIDs)
}

// TestApplyUpdate_SameVersionFileOnlyUpdate_PureRemovalCompensationStaysNarrow:
// the shared-dir gate must be SYMMETRIC - "the transition changes the
// installed ID set" - not "some old ID departs". On a pure-removal update
// (old={A,B} -> new={B}: the author merged two files into one) the forward
// replace narrows, but the compensation call sees the swapped transition
// {B} -> {A,B}, where NO old ID departs - an asymmetric gate falls back to
// union there and deploys a stale generation's member that was never
// deployed before the update. After a compensated failure the game dir must
// hold exactly the pre-update deployment: a.esp and b.esp, never s.esp.
func TestApplyUpdate_SameVersionFileOnlyUpdate_PureRemovalCompensationStaysNarrow(t *testing.T) {
	configDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"fileA", "fileB"},
		map[string][]byte{"mod1-fileA.esp": []byte("A"), "mod1-fileB.esp": []byte("B")})
	seedSameVersionManifest(t, svc, game, "src", "mod1", "1.0", "fileA", []string{"mod1-fileA.esp"})
	seedSameVersionManifest(t, svc, game, "src", "mod1", "1.0", "fileB", []string{"mod1-fileB.esp"})
	// A stale, departed generation: its member sits in the shared dir with
	// recorded provenance but was NOT deployed before the update.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store("g1", "src", "mod1", "1.0", "mod1-fileS.esp", []byte("S")))
	seedSameVersionManifest(t, svc, game, "src", "mod1", "1.0", "fileS", []string{"mod1-fileS.esp"})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "fileA", Name: "Part A", FileName: "mod1-fileA.esp", Version: "1.0", Category: "MAIN"},
			{ID: "fileB", Name: "Part B", FileName: "mod1-fileB.esp", Version: "1.0", IsPrimary: true, Category: "MAIN"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	mock.AddDownload("fileB", []byte("B-content"))

	// Force the profile upsert - the LAST write - to fail.
	profilePath := filepath.Join(configDir, "games", "g1", "profiles", "default.yaml")
	require.NoError(t, os.Chmod(profilePath, 0444))
	t.Cleanup(func() { _ = os.Chmod(profilePath, 0644) })

	// fileA was merged into fileB: a pure-removal transition {A,B} -> {B}.
	upd := domain.Update{
		InstalledMod:       *old,
		NewVersion:         "1.0",
		FileIDReplacements: map[string]string{"fileA": "fileB"},
	}
	_, err = svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "updating profile")

	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-fileA.esp"))
	assert.NoError(t, statErr, "the removed part must be restored by the compensation")
	_, statErr = os.Stat(filepath.Join(gameDir, "mod1-fileB.esp"))
	assert.NoError(t, statErr, "the retained part must stay deployed")
	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileS.esp"))
	assert.True(t, os.IsNotExist(statErr),
		"the reversed compensation must narrow too - a stale generation's member never deployed pre-update must not appear")

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"fileA", "fileB"}, updated.FileIDs, "RollbackModVersion must have restored the record")
}

// TestApplyUpdate_SameVersionFileOnlyUpdate_LegacyCacheFallsBackToUnion is
// the hard backward-compat rule for pre-manifest cache entries: the old
// file's provenance is unrecorded (seedUpdatableMod writes no markers - the
// exact on-disk shape of every cache entry made before manifests existed),
// so nothing may be undeployed, nothing may error, and nothing may warn -
// the historical union behavior, silently.
func TestApplyUpdate_SameVersionFileOnlyUpdate_LegacyCacheFallsBackToUnion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"fileA"}, map[string][]byte{"mod1-fileA.esp": []byte("A")})
	// Deliberately NO seedSameVersionManifest: legacy entries carry no
	// manifest, and absence must never be guessed around.

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
	result, err := svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.NoError(t, err, "a legacy cache entry must never make the update fail")
	assert.Empty(t, result.Warnings, "the fallback must be silent - no warning storm for every old cache")

	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-fileA.esp"))
	assert.NoError(t, statErr, "without provenance nothing may be undeployed - union behavior preserved")
	_, statErr = os.Stat(filepath.Join(gameDir, "mod1-fileB.esp"))
	assert.NoError(t, statErr)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"fileB"}, updated.FileIDs, "the record still advances")
}

// TestApplyUpdate_SameVersionFileOnlyUpdate_CompensatedFailureRestoresSuperseded
// pins rollback fidelity (#144 item 4 point 5): when the update deploys but a
// later write fails (here: UpsertMod, via a read-only profiles dir),
// ApplyUpdate's best-effort reverse Replace must restore the superseded
// member and remove the uncommitted new file's member - the compensation call
// carries the NEW file IDs as its superseded set, so the shared-dir undeploy
// runs in reverse.
func TestApplyUpdate_SameVersionFileOnlyUpdate_CompensatedFailureRestoresSuperseded(t *testing.T) {
	configDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"fileA"}, map[string][]byte{"mod1-fileA.esp": []byte("A")})
	seedSameVersionManifest(t, svc, game, "src", "mod1", "1.0", "fileA", []string{"mod1-fileA.esp"})

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

	// Make the profile upsert - the LAST write in ApplyUpdate's sequence -
	// fail deterministically (SaveProfile os.WriteFile-truncates the existing
	// YAML in place, so the FILE must be unwritable, not its directory).
	profilePath := filepath.Join(configDir, "games", "g1", "profiles", "default.yaml")
	require.NoError(t, os.Chmod(profilePath, 0444))
	t.Cleanup(func() { _ = os.Chmod(profilePath, 0644) })

	upd := domain.Update{
		InstalledMod:       *old,
		NewVersion:         "1.0",
		FileIDReplacements: map[string]string{"fileA": "fileB"},
	}
	_, err = svc.ApplyUpdate(context.Background(), game, "default", upd, core.UpdateOptions{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "updating profile")

	_, statErr := os.Stat(filepath.Join(gameDir, "mod1-fileA.esp"))
	assert.NoError(t, statErr, "the compensated failure must NOT leave the superseded member undeployed")
	_, statErr = os.Lstat(filepath.Join(gameDir, "mod1-fileB.esp"))
	assert.True(t, os.IsNotExist(statErr), "the uncommitted new file's member must be removed by the reverse Replace")

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"fileA"}, updated.FileIDs, "RollbackModVersion must have restored the record")
	assert.Equal(t, "1.0", updated.Version)
}

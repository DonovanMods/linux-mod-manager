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
	mock.AddDownload("new-1", []byte(strings.Repeat("x", 8192)))

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

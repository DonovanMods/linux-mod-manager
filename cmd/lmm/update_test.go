package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateCmd_NoGame(t *testing.T) {
	// Reset flags. configDir must point at an empty tempdir so requireGame
	// does not pick up a default-game from the user's real ~/.config/lmm.
	gameID = ""
	configDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(updateCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"update"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestUpdateCmd_Structure(t *testing.T) {
	assert.Equal(t, "update [mod-id]", updateCmd.Use)
	assert.NotEmpty(t, updateCmd.Short)
	assert.NotEmpty(t, updateCmd.Long)

	// Check flags exist
	assert.NotNil(t, updateCmd.Flags().Lookup("source"))
	assert.NotNil(t, updateCmd.Flags().Lookup("profile"))
	assert.NotNil(t, updateCmd.Flags().Lookup("all"))
	assert.NotNil(t, updateCmd.Flags().Lookup("dry-run"))
}

func TestUpdateRollbackCmd_Structure(t *testing.T) {
	assert.Equal(t, "rollback <mod-id>", updateRollbackCmd.Use)
	assert.NotEmpty(t, updateRollbackCmd.Short)
	assert.NotEmpty(t, updateRollbackCmd.Long)

	// Check flags exist
	assert.NotNil(t, updateRollbackCmd.Flags().Lookup("source"))
	assert.NotNil(t, updateRollbackCmd.Flags().Lookup("profile"))
}

func TestUpdateRollbackCmd_NoGame(t *testing.T) {
	// Reset flags. configDir must point at an empty tempdir so requireGame
	// does not pick up a default-game from the user's real ~/.config/lmm.
	gameID = ""
	configDir = t.TempDir()

	cmd := &cobra.Command{Use: "test"}
	updateCmdCopy := &cobra.Command{Use: "update"}
	updateCmdCopy.AddCommand(updateRollbackCmd)
	cmd.AddCommand(updateCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"update", "rollback", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestPolicyToString(t *testing.T) {
	tests := []struct {
		policy   int
		expected string
	}{
		{0, "notify"}, // domain.UpdateNotify
		{1, "auto"},   // domain.UpdateAuto
		{2, "pinned"}, // domain.UpdatePinned
	}

	for _, tt := range tests {
		// Use internal knowledge that UpdatePolicy is an int
		result := policyToString(domain.UpdatePolicy(tt.policy))
		assert.Equal(t, tt.expected, result)
	}
}

// --- applyUpdate refit (Phase 5b Task 3) ---
//
// fakeUpdateSource is a minimal source.ModSource for the update refit's
// tests, backed by a real httptest server so Service.ApplyUpdate's actual
// DownloadMod path runs end to end - mirrors install_test.go's
// fakeInstallSource (internal/core's own test-only mock sources live in a
// different package and aren't visible here). Unlike fakeInstallSource, it
// has a REAL CheckUpdates: it compares each installed mod's version against
// the "new version" Mod registered via AddMod, producing a domain.Update
// when they differ - update.go's doUpdate/applySingleUpdate both drive
// entirely off Service.NewUpdater().CheckUpdates, which delegates straight
// to the registered source's own CheckUpdates.
type fakeUpdateSource struct {
	id           string
	mods         map[string]*domain.Mod
	files        map[string][]domain.DownloadableFile
	downloads    map[string][]byte
	changelogs   map[string]string
	replacements map[string]map[string]string
	authRequired bool
	srv          *httptest.Server
}

func newFakeUpdateSource(id string) *fakeUpdateSource {
	s := &fakeUpdateSource{
		id:           id,
		mods:         make(map[string]*domain.Mod),
		files:        make(map[string][]domain.DownloadableFile),
		downloads:    make(map[string][]byte),
		changelogs:   make(map[string]string),
		replacements: make(map[string]map[string]string),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fileID := strings.TrimPrefix(r.URL.Path, "/")
		if content, ok := s.downloads[fileID]; ok {
			_, _ = w.Write(content)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	s.srv = httptest.NewServer(mux)
	return s
}

func (s *fakeUpdateSource) Close()          { s.srv.Close() }
func (s *fakeUpdateSource) ID() string      { return s.id }
func (s *fakeUpdateSource) Name() string    { return "Fake Update Source" }
func (s *fakeUpdateSource) AuthURL() string { return "" }
func (s *fakeUpdateSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, nil
}
func (s *fakeUpdateSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (s *fakeUpdateSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if mod, ok := s.mods[modID]; ok {
		return mod, nil
	}
	return nil, domain.ErrModNotFound
}
func (s *fakeUpdateSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *fakeUpdateSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return s.files[mod.ID], nil
}
func (s *fakeUpdateSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return s.srv.URL + "/" + fileID, nil
}
func (s *fakeUpdateSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	if s.authRequired {
		return nil, domain.ErrAuthRequired
	}
	var updates []domain.Update
	for _, im := range installed {
		avail, ok := s.mods[im.ID]
		if !ok || avail.Version == im.Version {
			continue
		}
		updates = append(updates, domain.Update{
			InstalledMod:       im,
			NewVersion:         avail.Version,
			Changelog:          s.changelogs[im.ID],
			FileIDReplacements: s.replacements[im.ID],
		})
	}
	return updates, nil
}

// AddMod registers modID's "new version" definition and its downloadable
// files (what GetMod/GetModFiles return, and what CheckUpdates compares an
// installed mod's version against).
func (s *fakeUpdateSource) AddMod(mod *domain.Mod, files []domain.DownloadableFile) {
	s.mods[mod.ID] = mod
	s.files[mod.ID] = files
}

func (s *fakeUpdateSource) AddDownload(fileID string, content []byte) {
	s.downloads[fileID] = content
}

// setupDoUpdateTest builds a *core.Service, a game configured for
// fakeUpdateSource, and resets update's package-level flag globals to sane
// defaults for calling doUpdate/applySingleUpdate/doUpdateRollback directly,
// following setupDoInstallTest's pattern.
func setupDoUpdateTest(t *testing.T) (*core.Service, *domain.Game, *fakeUpdateSource) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := newFakeUpdateSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"test-src": "g1"},
	}

	oldSource, oldProfile, oldAll, oldDryRun, oldForce := updateSource, updateProfile, updateAll, updateDryRun, updateForce
	oldVerbose, oldNoColor, oldNoHooks := verbose, noColor, noHooks
	updateSource = "test-src"
	updateProfile = ""
	updateAll = false
	updateDryRun = false
	updateForce = false
	verbose = false
	noColor = true
	noHooks = false
	t.Cleanup(func() {
		updateSource, updateProfile, updateAll, updateDryRun, updateForce = oldSource, oldProfile, oldAll, oldDryRun, oldForce
		verbose, noColor, noHooks = oldVerbose, oldNoColor, oldNoHooks
	})

	return svc, game, src
}

// seedInstalledForUpdate seeds an installed, deployed, profile-tracked "old
// version" mod ready to be passed through doUpdate/applySingleUpdate's
// refit onto Service.ApplyUpdate.
func seedInstalledForUpdate(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version string, fileIDs []string, files map[string][]byte) *domain.InstalledMod {
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

// TestApplySingleUpdate_HappyPath_PrintsExpectedOutput guards the refit's
// output for `lmm update <mod-id>`: byte-identical to the pre-refit CLI in
// both --verbose and non-verbose modes.
func TestApplySingleUpdate_HappyPath_PrintsExpectedOutput(t *testing.T) {
	t.Run("verbose", func(t *testing.T) {
		svc, game, src := setupDoUpdateTest(t)
		verbose = true
		mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
		// Kept under net/http's 2048-byte auto-Content-Length threshold so
		// TotalBytes is known and the "\r  Downloading: %.1f%%" line fires.
		src.AddDownload("new-1", []byte(strings.Repeat("x", 1024)))

		out := captureStdout(t, func() error {
			return applySingleUpdate(context.Background(), svc, game, mod, "default")
		})

		assert.Contains(t, out, "Updating Mod One 1.0 → 2.0...\n")
		assert.Contains(t, out, "Downloading: 100.0%")
		assert.Contains(t, out, "\n✓ Updated: Mod One 1.0 → 2.0\n")
		assert.Contains(t, out, "  Previous version preserved for rollback\n")

		_, err := os.Lstat(filepath.Join(game.ModPath, "mod1-new.esp"))
		assert.NoError(t, err, "new file must be deployed")
		_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
		assert.True(t, os.IsNotExist(err), "old file must be undeployed")
	})

	t.Run("non-verbose", func(t *testing.T) {
		svc, game, src := setupDoUpdateTest(t)
		verbose = false
		mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
		src.AddDownload("new-1", []byte(strings.Repeat("x", 1024)))

		out := captureStdout(t, func() error {
			return applySingleUpdate(context.Background(), svc, game, mod, "default")
		})

		assert.Contains(t, out, "Updating Mod One 1.0 → 2.0...\n")
		assert.NotContains(t, out, "Downloading:", "download progress must be --verbose-gated")
		assert.Contains(t, out, "\n✓ Updated: Mod One 1.0 → 2.0\n")
		assert.Contains(t, out, "  Previous version preserved for rollback\n")
	})
}

// TestDoUpdate_BatchAutoAndAll_MidBatchFailureContinues guards doUpdate's own
// loop: auto-policy updates apply automatically, a failure mid-batch prints
// "✗ %s: %v" and CONTINUES to the next update (never aborts), and --all
// applies the remaining notify-policy updates afterward.
func TestDoUpdate_BatchAutoAndAll_MidBatchFailureContinues(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	updateAll = true

	// mod1: auto-policy, succeeds.
	mod1 := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"m1-old"}, map[string][]byte{"mod1-old.esp": []byte("old1")})
	mod1.UpdatePolicy = domain.UpdateAuto
	require.NoError(t, svc.SaveInstalledMod(mod1))
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "m1-new", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("m1-new", []byte("new1"))

	// mod2: auto-policy, download fails (no AddDownload registered).
	mod2 := seedInstalledForUpdate(t, svc, game, "test-src", "mod2", "Mod Two", "1.0", []string{"m2-old"}, map[string][]byte{"mod2-old.esp": []byte("old2")})
	mod2.UpdatePolicy = domain.UpdateAuto
	require.NoError(t, svc.SaveInstalledMod(mod2))
	src.AddMod(&domain.Mod{ID: "mod2", SourceID: "test-src", Name: "Mod Two", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "m2-new", FileName: "mod2-new.esp", IsPrimary: true}})

	// mod3: notify-policy, only applied via --all, succeeds.
	seedInstalledForUpdate(t, svc, game, "test-src", "mod3", "Mod Three", "1.0", []string{"m3-old"}, map[string][]byte{"mod3-old.esp": []byte("old3")})
	src.AddMod(&domain.Mod{ID: "mod3", SourceID: "test-src", Name: "Mod Three", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "m3-new", FileName: "mod3-new.esp", IsPrimary: true}})
	src.AddDownload("m3-new", []byte("new3"))

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "\nApplying 2 auto-update(s)...\n")
	assert.Contains(t, out, "  ✓ Mod One 1.0 → 2.0\n")
	assert.Contains(t, out, "  ✗ Mod Two: ")
	assert.Contains(t, out, "\nApplying 1 remaining update(s)...\n")
	assert.Contains(t, out, "  ✓ Mod Three 1.0 → 2.0\n")

	updated1, err := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated1.Version, "mod1 must have applied")

	updated2, err := svc.GetInstalledMod("test-src", "mod2", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated2.Version, "mod2's failure must not have applied, but the batch must have continued past it")

	updated3, err := svc.GetInstalledMod("test-src", "mod3", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated3.Version, "mod3 (--all, notify-policy) must have applied")
}

// TestDoUpdate_DryRun_ZeroSideEffectsAndOutputUnchanged guards --dry-run:
// applyUpdate (and therefore Service.ApplyUpdate) must never be called at
// all - proven concretely via a before_each hook script that would append to
// a log file if invoked, which must never happen - and the printed output
// must be unchanged from the pre-refit CLI.
func TestDoUpdate_DryRun_ZeroSideEffectsAndOutputUnchanged(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	updateDryRun = true

	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	mod.UpdatePolicy = domain.UpdateAuto
	require.NoError(t, svc.SaveInstalledMod(mod))
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	// Deliberately no AddDownload - if ApplyUpdate were ever called, the
	// download would 404, surfacing as a visible failure and failing this
	// test outright.

	scriptsDir := t.TempDir()
	callLog := filepath.Join(scriptsDir, "calls.log")
	hookScript := filepath.Join(scriptsDir, "before_each.sh")
	require.NoError(t, os.WriteFile(hookScript, []byte("#!/bin/bash\necho called >> "+callLog+"\nexit 0\n"), 0o755))
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeEach: hookScript}}

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "\nWould auto-update 1 mod(s):\n")
	assert.Contains(t, out, "  - Mod One 1.0 → 2.0\n")
	assert.Contains(t, out, "\nUse without --dry-run to apply updates.\n")

	_, err := os.Stat(callLog)
	assert.True(t, os.IsNotExist(err), "no hook (and therefore no flow) must ever run under --dry-run")

	updated, err := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version, "dry-run must not mutate anything")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the old file must remain deployed")
}

// TestApplySingleUpdate_AuthRequired_ShowsAuthPrompt guards the
// auth-required rendering path: this logic lives in applySingleUpdate's own
// CheckUpdates error handling (untouched by the applyUpdate refit - it runs
// before applyUpdate is ever called), but the brief calls for verifying it
// still renders correctly after the refit.
func TestApplySingleUpdate_AuthRequired_ShowsAuthPrompt(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	src.authRequired = true
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})

	err := applySingleUpdate(context.Background(), svc, game, mod, "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication required")
	assert.Contains(t, err.Error(), "lmm auth login test-src")
}

// TestDoUpdateRollback_Integration_AfterApplySingleUpdate is the mandatory
// integration test: `lmm update rollback` must still work end to end after
// the applyUpdate refit - update then rollback restores the previous
// version, and the previous version's cache entry (never deleted by
// Service.ApplyUpdate) is what makes it possible.
func TestDoUpdateRollback_Integration_AfterApplySingleUpdate(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	require.NoError(t, captureStdoutOnlyErr(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	}))

	updated, err := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	require.Equal(t, "2.0", updated.Version)
	require.Equal(t, "1.0", updated.PreviousVersion)
	require.True(t, svc.GetGameCache(game).Exists("g1", "test-src", "mod1", "1.0"), "the previous version must still be cached for rollback")

	out := captureStdout(t, func() error {
		return doUpdateRollback(context.Background(), svc, game, "mod1")
	})

	assert.Contains(t, out, "Rolling back Mod One 2.0 → 1.0...\n")
	assert.Contains(t, out, "\n✓ Rolled back: Mod One 2.0 → 1.0\n")

	rolledBack, err := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", rolledBack.Version, "rollback must restore the previous version")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the original file must be redeployed after rollback")
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-new.esp"))
	assert.True(t, os.IsNotExist(err), "the updated version's file must be undeployed after rollback")
}

// captureStdoutOnlyErr runs fn with stdout discarded, returning only its
// error - for setup steps in an integration test where the console output
// isn't the thing under test.
func captureStdoutOnlyErr(t *testing.T, fn func() error) error {
	t.Helper()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer devNull.Close()
	old := os.Stdout
	os.Stdout = devNull
	defer func() { os.Stdout = old }()
	return fn()
}

// TestApplyUpdate_ForcedBeforeEachHookFailure_PrintsWarningAndApplies guards
// the one gap the Task 3 review found in the applyUpdate refit: no committed
// test drove update.go's applyUpdate closure itself (as opposed to
// applySingleUpdate/doUpdate, which only exercise it indirectly) far enough
// to observe its progress-callback switch translate a core.UpdateBeforeEachForced
// event into a printed line. With --force, a failing uninstall.before_each
// hook must produce the exact "Warning: uninstall.before_each hook failed
// (forced): <err>" line on stderr (see update.go's `case
// core.UpdateBeforeEachForced, core.UpdateWarning:` - `fmt.Fprintf(os.Stderr,
// "Warning: %s\n", p.Detail)` - and core's ApplyUpdate, which formats
// p.Detail as "uninstall.before_each hook failed (forced): %v"), and the
// update must still apply (Force downgrades the hook failure to a warning
// rather than aborting).
func TestApplyUpdate_ForcedBeforeEachHookFailure_PrintsWarningAndApplies(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	updateForce = true

	scriptsDir := t.TempDir()
	failScript := filepath.Join(scriptsDir, "before_each.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
	game.Hooks = domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: failScript}}

	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	upd := domain.Update{InstalledMod: *mod, NewVersion: "2.0"}
	stderr, err := captureStderrErr(t, func() error {
		return applyUpdate(context.Background(), svc, game, upd, "default")
	})

	require.NoError(t, err, "a forced before_each hook failure must not abort the update")
	assert.Contains(t, stderr,
		"Warning: uninstall.before_each hook failed (forced): hook failed with exit code 1: "+failScript+"\n")

	updated, err := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated.Version, "the update must still apply despite the forced hook failure")
}

// TestApplyUpdate_AfterEachHookFailures_PrintWarningsAndSucceed guards the
// applyUpdate closure's other branch of the same review finding: both
// after_each hooks are always non-fatal (regardless of Force - unlike the
// before_each hooks above), so their core.UpdateWarning progress events must
// each still reach stderr as a "Warning: <hook> hook failed: <err>" line, and
// the update must succeed overall.
func TestApplyUpdate_AfterEachHookFailures_PrintWarningsAndSucceed(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	updateForce = false // after_each hooks are non-fatal even without --force

	scriptsDir := t.TempDir()
	uninstallScript := filepath.Join(scriptsDir, "uninstall_after_each.sh")
	installScript := filepath.Join(scriptsDir, "install_after_each.sh")
	require.NoError(t, os.WriteFile(uninstallScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
	require.NoError(t, os.WriteFile(installScript, []byte("#!/bin/bash\nexit 1\n"), 0o755))
	game.Hooks = domain.GameHooks{
		Uninstall: domain.HookConfig{AfterEach: uninstallScript},
		Install:   domain.HookConfig{AfterEach: installScript},
	}

	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	upd := domain.Update{InstalledMod: *mod, NewVersion: "2.0"}
	stderr, err := captureStderrErr(t, func() error {
		return applyUpdate(context.Background(), svc, game, upd, "default")
	})

	require.NoError(t, err, "after_each hook failures must never fail the update")
	assert.Contains(t, stderr,
		"Warning: uninstall.after_each hook failed: hook failed with exit code 1: "+uninstallScript+"\n")
	assert.Contains(t, stderr,
		"Warning: install.after_each hook failed: hook failed with exit code 1: "+installScript+"\n")

	updated, err := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated.Version, "the update must have applied despite both after_each hook failures")
}

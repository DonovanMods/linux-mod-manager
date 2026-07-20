package tui_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/tui"
)

// stubSource implements source.ModSource with canned search results.
// Only Search and identity methods matter; the rest are unreachable in
// these tests. id defaults to "stub" when unset, preserving existing
// single-source test fixtures; set it to register multiple distinct stubs
// (e.g. for all-sources search tests).
type stubSource struct {
	id     string
	result source.SearchResult
	err    error
}

func (s *stubSource) ID() string {
	if s.id != "" {
		return s.id
	}
	return "stub"
}
func (s *stubSource) Name() string    { return "Stub Source" }
func (s *stubSource) AuthURL() string { return "" }
func (s *stubSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return s.result, s.err
}
func (s *stubSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", errors.New("not implemented")
}
func (s *stubSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, errors.New("not implemented")
}

func newCoreProviderFixture(t *testing.T) (tui.DataProvider, *core.Service, *domain.Game) {
	t.Helper()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID:          "test-game",
		Name:        "Test Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "101",
			SourceID: "nexusmods",
			GameID:   game.ID,
			Name:     "SkyUI",
			Author:   "schlangster",
			Version:  "5.2",
		},
		ProfileName: "default",
		Enabled:     true,
		Deployed:    true,
	}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "102",
			SourceID: "nexusmods",
			GameID:   game.ID,
			Name:     "USSEP",
			Author:   "Arthmoor",
			Version:  "4.3",
		},
		ProfileName: "default",
		Enabled:     false,
	}))

	return tui.NewCoreProvider(svc, game, "default"), svc, game
}

func TestCoreProviderOverview(t *testing.T) {
	provider, _, _ := newCoreProviderFixture(t)

	summary, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Test Game", summary.GameName)
	require.Equal(t, "default", summary.ProfileName)
	require.Equal(t, 2, summary.Installed)
	require.Equal(t, 1, summary.Enabled)
	require.Equal(t, -1, summary.Updates, "updates are unknown until an update check runs")
	require.Equal(t, -1, summary.Conflicts, "conflicts are unknown in the read-only phase")

	require.Len(t, mods, 2)
	byName := map[string]tui.ModItem{}
	for _, m := range mods {
		byName[m.Name] = m
	}
	require.Equal(t, "deployed", byName["SkyUI"].Status)
	require.Equal(t, "nexusmods", byName["SkyUI"].Source)
	require.Equal(t, "5.2", byName["SkyUI"].Version)
	require.Equal(t, "101", byName["SkyUI"].ID, "ModItem.ID must carry the installed mod's ID")
	require.Equal(t, "disabled", byName["USSEP"].Status)
	require.Equal(t, "102", byName["USSEP"].ID)
}

func TestCoreProviderProfiles(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "hardcore")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "hardcore", domain.ModReference{SourceID: "nexusmods", ModID: "101"}))

	profiles, err := provider.Profiles(context.Background())
	require.NoError(t, err)
	require.Len(t, profiles, 2)

	byName := map[string]tui.ProfileItem{}
	for _, p := range profiles {
		byName[p.Name] = p
	}
	require.True(t, byName["default"].Active)
	require.False(t, byName["hardcore"].Active)
	require.Equal(t, 1, byName["hardcore"].ModCount, "ModCount should map from profile YAML mods, not the DB")
}

func TestCoreProviderSourcesAreSortedGameSources(t *testing.T) {
	provider, _, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"nexusmods": "testgame", "curseforge": "testgame"}

	require.Equal(t, []string{"curseforge", "nexusmods"}, provider.Sources())
}

func TestCoreProviderSearchMarksInstalled(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"stub": "testgame"}
	svc.RegisterSource(&stubSource{result: source.SearchResult{
		Mods: []domain.Mod{
			{ID: "101", SourceID: "stub", Name: "SkyUI-Stub", Author: "a", Version: "5.2"},
			{ID: "999", SourceID: "stub", Name: "NewMod", Author: "b", Version: "1.0"},
		},
		TotalCount: 2, Page: 0, PageSize: 10,
	}})
	// Fixture installed mod 101 under sourceID "nexusmods"; install one under "stub" too:
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:         domain.Mod{ID: "101", SourceID: "stub", GameID: game.ID, Name: "SkyUI-Stub", Version: "5.2"},
		ProfileName: "default", Enabled: true,
	}))

	page, err := provider.Search(context.Background(), "stub", "sky", 0)
	require.NoError(t, err)
	require.Equal(t, "sky", page.Query)
	require.Equal(t, "stub", page.Source)
	require.Equal(t, 2, page.TotalCount)
	require.Len(t, page.Results, 2)

	byName := map[string]tui.ModItem{}
	for _, r := range page.Results {
		byName[r.Name] = r
	}
	require.Equal(t, "installed", byName["SkyUI-Stub"].Status)
	require.Equal(t, "101", byName["SkyUI-Stub"].ID, "ModItem.ID must carry the search result's mod ID")
	require.Equal(t, "available", byName["NewMod"].Status)
	require.Equal(t, "999", byName["NewMod"].ID)
}

func TestCoreProviderSearchDoesNotCrossSourceMarkInstalled(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"stub": "testgame"}
	svc.RegisterSource(&stubSource{result: source.SearchResult{
		Mods: []domain.Mod{
			// Same mod ID as the fixture's nexusmods install, but from "stub" —
			// ModKey(source, id) must keep these distinct.
			{ID: "101", SourceID: "stub", Name: "SkyUI-Stub", Author: "a", Version: "5.2"},
		},
		TotalCount: 1, Page: 0, PageSize: 10,
	}})

	page, err := provider.Search(context.Background(), "stub", "sky", 0)
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.Equal(t, "available", page.Results[0].Status,
		"mod 101 is installed under nexusmods, not stub — no cross-source marking")
}

func TestCoreProviderSearchPropagatesAuthRequired(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"stub": "testgame"}
	svc.RegisterSource(&stubSource{err: fmt.Errorf("%w: key required", domain.ErrAuthRequired)})

	_, err := provider.Search(context.Background(), "stub", "x", 0)
	require.ErrorIs(t, err, domain.ErrAuthRequired)
}

func TestCoreProviderSearchAllSources(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"alpha": "testgame", "beta": "testgame"}
	svc.RegisterSource(&stubSource{id: "alpha", result: source.SearchResult{
		Mods:       []domain.Mod{{ID: "a1", SourceID: "alpha", Name: "Alpha Mod", Version: "1.0"}},
		TotalCount: 1,
	}})
	svc.RegisterSource(&stubSource{id: "beta", result: source.SearchResult{
		Mods:       []domain.Mod{{ID: "b1", SourceID: "beta", Name: "Beta Mod", Version: "1.0"}},
		TotalCount: 1,
	}})

	page, err := provider.Search(context.Background(), "", "quer", 0)
	require.NoError(t, err)
	// Results from both sources present, each row's Source set:
	sources := map[string]bool{}
	for _, item := range page.Results {
		sources[item.Source] = true
	}
	assert.Len(t, sources, 2)
}

func TestCoreProviderSearchAllSourcesWarnings(t *testing.T) {
	const failingSourceID = "flaky"
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"good": "testgame", failingSourceID: "testgame"}
	svc.RegisterSource(&stubSource{id: "good", result: source.SearchResult{
		Mods:       []domain.Mod{{ID: "g1", SourceID: "good", Name: "Good Mod", Version: "1.0"}},
		TotalCount: 1,
	}})
	svc.RegisterSource(&stubSource{id: failingSourceID, err: errors.New("connection refused")})

	page, err := provider.Search(context.Background(), "", "quer", 0)
	require.NoError(t, err)
	require.Len(t, page.Warnings, 1)
	assert.Contains(t, page.Warnings[0], failingSourceID)
	assert.NotEmpty(t, page.Results, "good source's results survive the failure")
}

// --- coreProvider: ActionProvider ---

// newCoreActionsFixture mirrors newCoreProviderFixture, but returns the
// write-side ActionProvider (tui.NewCoreActions) for the same
// svc/game/"default"-profile triple, proving both constructors are wireable
// from the identical already-resolved (svc, game, profileName) values.
func newCoreActionsFixture(t *testing.T) (tui.ActionProvider, *core.Service, *domain.Game) {
	t.Helper()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID:          "test-game",
		Name:        "Test Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	return tui.NewCoreActions(svc, game, "default"), svc, game
}

// seedActionMod stores files (if non-nil) in game's cache and saves an
// InstalledMod DB row for sourceID/modID/version, mirroring
// internal/core/flows_test.go's seedInstalledMod (unexported there, so
// duplicated here for this package's tests - see that file's identical
// helper for the original).
func seedActionMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version string, enabled bool, files map[string][]byte) {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, path, content))
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
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

// seedActionProfileMod adds sourceID/modID/version to profileName's YAML,
// creating the profile first if needed - mirrors
// internal/core/flows_test.go's seedProfileWithMod (also unexported there).
func seedActionProfileMod(t *testing.T, svc *core.Service, gameID, profileName, sourceID, modID, version string) {
	t.Helper()
	pm := svc.NewProfileManager()
	if _, err := pm.Get(gameID, profileName); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(gameID, profileName)
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(gameID, profileName, domain.ModReference{SourceID: sourceID, ModID: modID, Version: version}))
}

// createActionsTestScript mirrors internal/core's createTestScript
// (unexported there, so duplicated here).
func createActionsTestScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	scriptPath := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(scriptPath, []byte(content), 0755))
	return scriptPath
}

func TestCoreProviderActions_EnableMod_DeploysAndReturnsMessage(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "1", "Test Mod", "1.0", false, map[string][]byte{"plugin.esp": []byte("data")})

	outcome, err := actions.EnableMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Test Mod"})
	require.NoError(t, err)
	assert.Equal(t, `Enabled "Test Mod"`, outcome.Message)
	assert.Empty(t, outcome.Warnings)

	mod, err := svc.GetInstalledMod("src", "1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled)

	_, err = os.Lstat(filepath.Join(game.ModPath, "plugin.esp"))
	assert.NoError(t, err, "EnableMod must actually deploy the mod's files")
}

func TestCoreProviderActions_EnableMod_AlreadyEnabledMessage(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})

	outcome, err := actions.EnableMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Test Mod"})
	require.NoError(t, err)
	assert.Equal(t, `"Test Mod" is already enabled`, outcome.Message)
}

func TestCoreProviderActions_EnableMod_UnknownModErrors(t *testing.T) {
	actions, _, _ := newCoreActionsFixture(t)

	_, err := actions.EnableMod(context.Background(), tui.ModItem{ID: "missing", Source: "src", Name: "Ghost"})
	require.Error(t, err)
}

func TestCoreProviderActions_DisableMod_UndeploysAndReturnsMessage(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	outcome, err := actions.DisableMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Test Mod"})
	require.NoError(t, err)
	assert.Equal(t, `Disabled "Test Mod"`, outcome.Message)

	mod, err := svc.GetInstalledMod("src", "1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled)

	_, err = os.Lstat(filepath.Join(game.ModPath, "plugin.esp"))
	assert.True(t, os.IsNotExist(err), "DisableMod must undeploy the mod's files")
}

func TestCoreProviderActions_DisableMod_AlreadyDisabledMessage(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "1", "Test Mod", "1.0", false, nil)

	outcome, err := actions.DisableMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Test Mod"})
	require.NoError(t, err)
	assert.Equal(t, `"Test Mod" is already disabled`, outcome.Message)
}

func TestCoreProviderActions_UninstallMod_RemovesAndReturnsMessage(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "1", "1.0")
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	outcome, err := actions.UninstallMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Test Mod"})
	require.NoError(t, err)
	assert.Equal(t, `Uninstalled "Test Mod"`, outcome.Message)
	assert.Empty(t, outcome.Warnings)

	_, err = svc.GetInstalledMod("src", "1", game.ID, "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

// TestCoreProviderActions_UninstallMod_RunsUninstallHooksMatchingCLIConfig
// guards that coreProvider.UninstallMod replicates the CLI's exact hook
// plumbing (cmd/lmm/uninstall.go's getResolvedHooks/getHookRunner/
// makeHookContext, traced in the task report): a game-level uninstall hook
// actually resolves and runs, mirroring
// internal/core/flows_test.go's TestService_UninstallMod_HookOrder
// arrangement, one level up.
func TestCoreProviderActions_UninstallMod_RunsUninstallHooksMatchingCLIConfig(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	scriptsDir := t.TempDir()
	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeEachScript := createActionsTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "before_each:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	game.Hooks.Uninstall.BeforeEach = beforeEachScript

	seedActionMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "1", "1.0")

	_, err := actions.UninstallMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Test Mod"})
	require.NoError(t, err)

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "before_each:1\n", string(logContent), "coreProvider must resolve and run the game's configured uninstall hooks, same as the CLI")
}

// TestCoreProviderActions_UninstallMod_WarningsIncludeResultWarningsThenNotes
// guards the ActionOutcome.Warnings = flow Warnings + Notes contract (in
// that order) documented on ActionOutcome: an unconditionally-non-fatal
// after_each hook failure produces a flow Warning, and the mod not being
// listed in the profile (mirroring
// internal/core/flows_test.go's TestService_UninstallMod_ProfileDesyncWarnsAndContinues)
// produces a flow Note - both must appear, Warnings first.
func TestCoreProviderActions_UninstallMod_WarningsIncludeResultWarningsThenNotes(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	scriptsDir := t.TempDir()
	failScript := createActionsTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	game.Hooks.Uninstall.AfterEach = failScript

	// No profile seeded with the mod -> profile-removal Note.
	seedActionMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})

	outcome, err := actions.UninstallMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Test Mod"})
	require.NoError(t, err)
	require.Len(t, outcome.Warnings, 2)
	assert.Contains(t, outcome.Warnings[0], "uninstall.after_each hook failed", "flow Warnings must come first")
	assert.True(t, strings.HasPrefix(outcome.Warnings[1], "Note: "), "flow Notes must follow, keeping their historical prefix: %q", outcome.Warnings[1])
}

func TestCoreProviderActions_DeployProfile_DeploysAndReturnsMessage(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "1", "Mod One", "1.0", true, map[string][]byte{"one.esp": []byte("1")})
	seedActionMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "1", "1.0")
	seedActionProfileMod(t, svc, game.ID, "default", "src", "2", "1.0")

	outcome, err := actions.DeployProfile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Deployed 2 mod(s)", outcome.Message)
	assert.Empty(t, outcome.Warnings)

	_, err = os.Lstat(filepath.Join(game.ModPath, "one.esp"))
	assert.NoError(t, err)
	_, err = os.Lstat(filepath.Join(game.ModPath, "two.esp"))
	assert.NoError(t, err)
}

// installActionsBlockingTrigger mirrors internal/core/flows_test.go's
// installBlockingTrigger (unexported there, duplicated here): it opens a
// second connection to dbPath and installs a trigger that makes any UPDATE
// touching installed_mods.link_method or installed_mods.deployed fail,
// deterministically forcing DeployProfile's SetModLinkMethod/SetModDeployed
// calls to error (and therefore record a Note) without relying on
// filesystem permissions. Must be called after the owning *core.Service has
// already run its migrations (so the installed_mods table exists).
func installActionsBlockingTrigger(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	_, err = conn.Exec(`
		CREATE TRIGGER block_link_method_and_deployed_updates_actions
		BEFORE UPDATE OF link_method, deployed ON installed_mods
		BEGIN
			SELECT RAISE(ABORT, 'blocked for test');
		END;
	`)
	require.NoError(t, err)
}

// TestCoreProviderActions_DeployProfile_NotesAppearInWarnings guards the
// ActionOutcome.Warnings = flow Warnings + Notes contract for DeployProfile,
// using the SQLite-blocking-trigger technique from
// internal/core/flows_test.go to force a Note deterministically.
func TestCoreProviderActions_DeployProfile_NotesAppearInWarnings(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	seedActionMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "1", "1.0")

	dbPath := filepath.Join(dataDir, "lmm.db")
	installActionsBlockingTrigger(t, dbPath)

	actions := tui.NewCoreActions(svc, game, "default")
	outcome, err := actions.DeployProfile(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, outcome.Warnings, "the blocked SetModLinkMethod/SetModDeployed updates must surface as Notes in Outcome.Warnings")
	for _, w := range outcome.Warnings {
		assert.Contains(t, w, "Warning: ", "DeployProfile's Notes carry a historical prefix")
	}
}

// TestCoreProviderActions_DeployProfile_ReportsFailedCount guards the
// ", %d failed" branch of coreProvider.DeployProfile's Message composition
// (service_core.go), which had zero coverage: one mod deploys normally, the
// other's cache entry is deleted so the deploy loop's redownload path runs
// and fails - no source is registered for "src", so the GetMod fetch
// mirrors internal/core/flows_test.go's
// TestService_DeployProfile_MissingCacheAndFetchFailure_SkipsMod arrangement,
// one level up - proving the mixed deployed/failed count renders correctly.
func TestCoreProviderActions_DeployProfile_ReportsFailedCount(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "1", "Mod One", "1.0", true, map[string][]byte{"one.esp": []byte("1")})
	seedActionMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "1", "1.0")
	seedActionProfileMod(t, svc, game.ID, "default", "src", "2", "1.0")

	require.NoError(t, svc.GetGameCache(game).Delete(game.ID, "src", "2", "1.0"),
		"deleting mod 2's cache entry forces the redownload path, which fails since no source named \"src\" is registered")

	outcome, err := actions.DeployProfile(context.Background())
	require.NoError(t, err, "a per-mod fetch failure must not fail the whole deploy")
	assert.Equal(t, "Deployed 1 mod(s), 1 failed", outcome.Message)
	assert.Empty(t, outcome.Warnings, "a redownload fetch failure is recorded in DeployResult.Skipped, not Warnings/Notes, so it does not surface in Outcome.Warnings")

	_, err = os.Lstat(filepath.Join(game.ModPath, "one.esp"))
	assert.NoError(t, err, "the mod with an intact cache entry should still deploy")
	_, err = os.Lstat(filepath.Join(game.ModPath, "two.esp"))
	assert.True(t, os.IsNotExist(err), "the mod whose redownload failed must not be deployed")
}

func TestCoreProviderActions_PlanProfileSwitch_MapsBucketsToDisplayStrings(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)

	// modC: enabled under "default", absent from "target" -> Disable.
	seedActionMod(t, svc, game, "src", "modC", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "modC", "1.0")

	// modB: installed (under default) but disabled, cached, referenced by target -> Enable.
	seedActionMod(t, svc, game, "src", "modB", "Mod B", "1.0", false, map[string][]byte{"b.esp": []byte("b")})
	seedActionProfileMod(t, svc, game.ID, "target", "src", "modB", "1.0")

	// modD: referenced by target only, no DB row -> NeedsDownloads.
	seedActionProfileMod(t, svc, game.ID, "target", "src", "modD", "2.0")

	view, err := actions.PlanProfileSwitch(context.Background(), "target")
	require.NoError(t, err)
	assert.Equal(t, "default", view.From)
	assert.Equal(t, "target", view.To)
	assert.False(t, view.NoChanges)
	assert.False(t, view.AlreadyActive)
	assert.Equal(t, []string{"Mod C"}, view.Disable)
	assert.Equal(t, []string{"Mod B"}, view.Enable)
	require.Len(t, view.NeedsDownloads, 1)
	assert.Equal(t, "src:modD v2.0", view.NeedsDownloads[0])
}

func TestCoreProviderActions_PlanProfileSwitch_AlreadyActive(t *testing.T) {
	actions, _, _ := newCoreActionsFixture(t)

	view, err := actions.PlanProfileSwitch(context.Background(), "default")
	require.NoError(t, err)
	assert.True(t, view.AlreadyActive)
	assert.Equal(t, "default", view.From)
	assert.Equal(t, "default", view.To)
}

func TestCoreProviderActions_PlanProfileSwitch_UnknownProfileErrors(t *testing.T) {
	actions, _, _ := newCoreActionsFixture(t)

	_, err := actions.PlanProfileSwitch(context.Background(), "does-not-exist")
	require.Error(t, err)
}

// TestCoreProviderActions_ApplyProfileSwitch_RefusesWhenNeedsDownloads
// guards the 5a scope cut: ApplyProfileSwitch must refuse, with the exact
// error text specified by the task brief, and must not mutate anything -
// the default profile stays active and the target profile's YAML is
// untouched - when the freshly-computed plan has NeedsDownloads entries.
func TestCoreProviderActions_ApplyProfileSwitch_RefusesWhenNeedsDownloads(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)
	seedActionProfileMod(t, svc, game.ID, "target", "src", "modD", "2.0")

	outcome, err := actions.ApplyProfileSwitch(context.Background(), "target")
	require.Error(t, err)
	assert.EqualError(t, err, "profile needs downloads — use 'lmm profile switch' until TUI install ships")
	assert.Equal(t, tui.ActionOutcome{}, outcome)

	def, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "default", def.Name, "the refusal must not switch the active profile")

	target, err := pm.Get(game.ID, "target")
	require.NoError(t, err)
	assert.Len(t, target.Mods, 1, "the target profile YAML must be untouched by the refused apply")
}

func TestCoreProviderActions_ApplyProfileSwitch_AppliesAndReturnsMessage(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)

	seedActionMod(t, svc, game, "src", "modC", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "modC", "1.0")

	outcome, err := actions.ApplyProfileSwitch(context.Background(), "target")
	require.NoError(t, err)
	assert.Equal(t, `Switched to "target"`, outcome.Message)

	def, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "target", def.Name)

	mod, err := svc.GetInstalledMod("src", "modC", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "modC must have been disabled by the switch")
}

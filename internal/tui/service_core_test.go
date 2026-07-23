package tui_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// blockingSource is a source.ModSource test double whose Search blocks until
// release is closed, after signaling entered - letting a test hold a
// coreProvider.Search call genuinely in-flight (past the network call, about
// to read p.profile in installedModKeys) while a concurrent goroutine calls
// SetProfile, for Task 6 item b's race-guard test.
type blockingSource struct {
	id      string
	entered chan struct{}
	release chan struct{}
}

func (s *blockingSource) ID() string      { return s.id }
func (s *blockingSource) Name() string    { return "Blocking Source" }
func (s *blockingSource) AuthURL() string { return "" }
func (s *blockingSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, errors.New("not implemented")
}
func (s *blockingSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	close(s.entered)
	<-s.release
	return source.SearchResult{}, nil
}
func (s *blockingSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, errors.New("not implemented")
}
func (s *blockingSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, errors.New("not implemented")
}
func (s *blockingSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, errors.New("not implemented")
}
func (s *blockingSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", errors.New("not implemented")
}
func (s *blockingSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, errors.New("not implemented")
}

// TestCoreProviderProfileFieldRaceGuard is Task 6 item b's race test: a
// Search call blocked genuinely in-flight (past the network call, about to
// read p.profile in installedModKeys - see blockingSource) overlaps a
// concurrent SetProfile call on the SAME coreProvider instance (mirroring
// production: cmd/lmm/tui.go wires m.provider's SetProfile via
// app.go's rebindProfile, called from the SAME instance Search runs
// against). Passes functionally either way (SetProfile's happens-before
// relationship to the read is irrelevant to the observable result here);
// the point is `go test -race` must NOT report a data race on p.profile.
// RED (this test, run under -race, against the pre-item-b code) is proven
// in the task report rather than left failing in the tree.
func TestCoreProviderProfileFieldRaceGuard(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"blocking": "testgame"}
	src := &blockingSource{id: "blocking", entered: make(chan struct{}), release: make(chan struct{})}
	svc.RegisterSource(src)

	rebinder, ok := provider.(interface{ SetProfile(string) })
	require.True(t, ok, "coreProvider must implement the profileRebinder shape")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = provider.Search(context.Background(), "blocking", "query", 0)
	}()

	<-src.entered // Search is now blocked inside the "network call", about
	// to proceed straight into installedModKeys' p.profile read the instant
	// release is closed below.
	go func() {
		defer wg.Done()
		rebinder.SetProfile("other")
	}()
	close(src.release)

	wg.Wait()
}

// TestCoreProviderGameFieldRaceGuard is Task 8's race test, mirroring
// TestCoreProviderProfileFieldRaceGuard immediately above in full (gameMu's
// own doc comment guards the identical race for p.game that profileMu
// guards for p.profile): a Search call blocked genuinely in-flight (past
// the network call, about to proceed into installedModKeys' own
// p.currentGame().ID read - the same spot p.currentProfile() is read right
// after unblocking) overlaps a concurrent SetGame call on the SAME
// coreProvider instance. Passes functionally either way; the point is
// `go test -race` must NOT report a data race on p.game.
func TestCoreProviderGameFieldRaceGuard(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"blocking": "testgame"}
	src := &blockingSource{id: "blocking", entered: make(chan struct{}), release: make(chan struct{})}
	svc.RegisterSource(src)

	second := &domain.Game{
		ID:          "second-game",
		Name:        "Second Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
	}
	require.NoError(t, svc.AddGame(second))

	rebinder, ok := provider.(interface{ SetGame(string) error })
	require.True(t, ok, "coreProvider must implement the gameRebinder shape")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = provider.Search(context.Background(), "blocking", "query", 0)
	}()

	<-src.entered // Search is now blocked inside the "network call", about
	// to proceed straight into installedModKeys' p.currentGame().ID read the
	// instant release is closed below.
	go func() {
		defer wg.Done()
		_ = rebinder.SetGame(second.ID)
	}()
	close(src.release)

	wg.Wait()
}

// netSource is a fuller source.ModSource test double than stubSource above:
// it serves real download content over an httptest.Server, so coreProvider's
// network-touching ActionProvider methods (PlanInstall/ApplyInstall/
// CheckUpdates/ApplyUpdate, and ApplyProfileSwitch's install loop) can be
// exercised end-to-end - real download, extract, deploy, DB write.
// Mirrors internal/core's own mockSource/mockSourceWithDownloads test
// doubles (service_test.go there), which are unexported and therefore
// unreachable from this package (a different package cannot import
// another's _test.go-only types) - this is a deliberate, minimal
// duplication of that same pattern for this package's own tests.
type netSource struct {
	id   string
	mods map[string]*domain.Mod // "gameID/modID" -> mod

	// files, if set for a mod ID, overrides the single-primary-file default
	// GetModFiles otherwise synthesizes from whatever's registered under
	// downloads[modID].
	files map[string][]domain.DownloadableFile
	// deps, if set for a mod ID, is returned verbatim by GetDependencies.
	deps map[string][]domain.ModReference

	downloads map[string][]byte // fileID -> zip content
	server    *httptest.Server

	updates []domain.Update

	getModErr, getModFilesErr, checkUpdatesErr error
}

func newNetSource(t *testing.T, id string) *netSource {
	t.Helper()
	s := &netSource{
		id:        id,
		mods:      map[string]*domain.Mod{},
		files:     map[string][]domain.DownloadableFile{},
		deps:      map[string][]domain.ModReference{},
		downloads: map[string][]byte{},
	}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileID := filepath.Base(r.URL.Path)
		content, ok := s.downloads[fileID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(content)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *netSource) ID() string      { return s.id }
func (s *netSource) Name() string    { return "Net Source" }
func (s *netSource) AuthURL() string { return "" }
func (s *netSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, errors.New("not implemented")
}
func (s *netSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}

func (s *netSource) addMod(gameID string, mod *domain.Mod) {
	s.mods[gameID+"/"+mod.ID] = mod
}

func (s *netSource) GetMod(_ context.Context, gameID, modID string) (*domain.Mod, error) {
	if s.getModErr != nil {
		return nil, s.getModErr
	}
	if mod, ok := s.mods[gameID+"/"+modID]; ok {
		return mod, nil
	}
	return nil, fmt.Errorf("mod not found: %s", modID)
}

func (s *netSource) GetDependencies(_ context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return s.deps[mod.ID], nil
}

func (s *netSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if s.getModFilesErr != nil {
		return nil, s.getModFilesErr
	}
	if files, ok := s.files[mod.ID]; ok {
		return files, nil
	}
	return []domain.DownloadableFile{{
		ID: mod.ID, Name: mod.Name, FileName: mod.ID + ".zip", IsPrimary: true,
		Size: int64(len(s.downloads[mod.ID])),
	}}, nil
}

func (s *netSource) GetDownloadURL(_ context.Context, _ *domain.Mod, fileID string) (string, error) {
	return s.server.URL + "/" + fileID, nil
}

func (s *netSource) CheckUpdates(_ context.Context, _ []domain.InstalledMod) ([]domain.Update, error) {
	if s.checkUpdatesErr != nil {
		return nil, s.checkUpdatesErr
	}
	return s.updates, nil
}

func (s *netSource) addDownload(fileID string, zipContent []byte) {
	s.downloads[fileID] = zipContent
}

// netSourceTestZip builds a one-file zip archive (relativePath -> content)
// and returns its bytes, mirroring internal/core/extractor_test.go's
// createTestZip (unexported there, duplicated here for the same reason
// netSource itself is).
func netSourceTestZip(t *testing.T, relativePath, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, err := w.Create(relativePath)
	require.NoError(t, err)
	_, err = fw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
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

// --- coreProvider: DeployedFiles ---

// TestCoreProviderDeployedFiles guards the read-only files-overlay data path
// (Task 4): DeployedFiles must return the exact relative paths a real
// Installer.Install run recorded via SaveDeployedFile, sorted (the
// underlying query already ORDER BYs relative_path - see
// internal/storage/db/files.go's GetDeployedFilesForMod - so this also pins
// that coreProvider does not need to re-sort defensively).
func TestCoreProviderDeployedFiles(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)

	gameCache := svc.GetGameCache(game)
	files := map[string][]byte{
		"textures/mod-a/main.dds": []byte("data"),
		"mod-a.esp":               []byte("data"),
	}
	for path, content := range files {
		require.NoError(t, gameCache.Store(game.ID, "nexusmods", "mod-a", "1.0", path, content))
	}
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "mod-a",
			SourceID: "nexusmods",
			GameID:   game.ID,
			Name:     "Mod A",
			Version:  "1.0",
		},
		ProfileName: "default",
		Enabled:     true,
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod-a", SourceID: "nexusmods", Version: "1.0", GameID: game.ID}, "default"))

	got, err := provider.DeployedFiles("nexusmods", "mod-a")
	require.NoError(t, err)
	require.Equal(t, []string{"mod-a.esp", "textures/mod-a/main.dds"}, got)
}

// TestCoreProviderDeployedFilesEmpty covers a mod with no deployed_files
// rows at all - the fixture's mod "101" carries Deployed:true on its
// InstalledMod DB row, but that flag is separate bookkeeping from the
// deployed_files tracking table (only Installer.Install populates it via
// SaveDeployedFile - see this file's other DeployedFiles test), so no
// Install call means no tracked files.
func TestCoreProviderDeployedFilesEmpty(t *testing.T) {
	provider, _, _ := newCoreProviderFixture(t)

	got, err := provider.DeployedFiles("nexusmods", "101")
	require.NoError(t, err)
	require.Empty(t, got)
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

// TestCoreProviderActions_DisableMod_UndeployFailureSurfacesAsWarning guards
// Task 6 item a's TUI refit: coreProvider.DisableMod folds
// core.DisableResult.Notes into ActionOutcome.Warnings (the established
// mergeDiagnostics pattern - see UninstallMod/DeployProfile), so an
// undeploy failure that used to be silently swallowed (`_ =` in flows.go
// pre-Task-6) now reaches the TUI status line instead of vanishing.
func TestCoreProviderActions_DisableMod_UndeployFailureSurfacesAsWarning(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	// Corrupt the deployed file into a plain file (not a symlink) so the
	// symlink linker's Undeploy fails deterministically ("not a symlink") -
	// mirrors internal/core/flows_test.go's
	// TestService_DisableMod_UndeployFailureIsNonFatal.
	deployedPath := filepath.Join(game.ModPath, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	outcome, err := actions.DisableMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Test Mod"})
	require.NoError(t, err, "undeploy failures must not fail DisableMod")
	assert.Equal(t, `Disabled "Test Mod"`, outcome.Message)
	require.Len(t, outcome.Warnings, 1)
	assert.Contains(t, outcome.Warnings[0], "Warning: failed to undeploy some files: ")

	mod, err := svc.GetInstalledMod("src", "1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "DB should still flip to disabled even when undeploy is best-effort")
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

// appendProfileHooksYAML appends a "hooks:" section to profileName's
// already-written YAML file (games/<gameID>/profiles/<profileName>.yaml),
// pointing uninstall.before_all at scriptPath. config.SaveProfile (used
// internally by ProfileManager.Create/AddMod, which seedActionProfileMod
// calls) does not itself round-trip Hooks/HooksExplicit - see
// internal/storage/config/profiles.go's SaveProfile, whose ProfileConfig
// literal omits Hooks entirely - so a profile-level hook override has to be
// layered on afterward, directly, for these tests. Appending a new
// top-level YAML key is safe: none of ProfileConfig's other fields
// (name/game_id/mods/link_method/is_default/overrides) collide with
// "hooks".
func appendProfileHooksYAML(t *testing.T, configDir, gameID, profileName, scriptPath string) {
	t.Helper()
	path := filepath.Join(configDir, "games", gameID, "profiles", profileName+".yaml")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	defer f.Close()
	_, err = fmt.Fprintf(f, "hooks:\n  uninstall:\n    before_all: %q\n", scriptPath)
	require.NoError(t, err)
}

// TestCoreProviderActions_ResolvedHooksCachedAcrossActions guards Task 6
// item c: resolvedHooks/hookRunner used to re-read+parse game/profile hook
// config from disk on EVERY action (5a review Minor, "now hot with 5b's
// frequent actions"). Caching is only observable through the public
// ActionProvider surface by mutating game.Hooks - the SAME *domain.Game
// pointer coreProvider holds - AFTER the first action call: a fresh,
// uncached resolvedHooks call would immediately see the mutation and skip
// the (now unconfigured) hook, while a cached one keeps using the first
// call's already-resolved hook.
func TestCoreProviderActions_ResolvedHooksCachedAcrossActions(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	scriptsDir := t.TempDir()
	callLog := filepath.Join(scriptsDir, "calls.log")
	script := createActionsTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "before_all" >> `+callLog+`
exit 0`)
	game.Hooks.Uninstall.BeforeAll = script

	seedActionMod(t, svc, game, "src", "1", "Mod One", "1.0", true, map[string][]byte{"one.esp": []byte("d")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "1", "1.0")
	seedActionMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("d")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "2", "1.0")

	_, err := actions.UninstallMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Mod One"})
	require.NoError(t, err)
	logAfterFirst, err := os.ReadFile(callLog)
	require.NoError(t, err)
	require.Equal(t, "before_all\n", string(logAfterFirst), "the first action must resolve and run the configured hook")

	// Mutate the SAME *domain.Game coreProvider holds - a fresh, uncached
	// resolvedHooks call would see this and skip the hook entirely.
	game.Hooks.Uninstall.BeforeAll = ""

	_, err = actions.UninstallMod(context.Background(), tui.ModItem{ID: "2", Source: "src", Name: "Mod Two"})
	require.NoError(t, err)
	logAfterSecond, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "before_all\nbefore_all\n", string(logAfterSecond),
		"resolvedHooks must be cached: the second action must still run the FIRST call's resolved hook, not re-read the now-mutated game.Hooks")
}

// TestCoreProviderActions_SetProfile_InvalidatesHooksCache guards Task 6
// item c's SetProfile invalidation half: a profile switch can change which
// profile's hooks.yaml override applies (see resolvedHooks), so the cached
// ResolvedHooks must be dropped on rebind, not carried over to the new
// profile - a stale cache here would silently keep running the OLD
// profile's hooks (or none) against the NEW one.
func TestCoreProviderActions_SetProfile_InvalidatesHooksCache(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	scriptsDir := t.TempDir()
	defaultLog := filepath.Join(scriptsDir, "default.log")
	otherLog := filepath.Join(scriptsDir, "other.log")
	defaultScript := createActionsTestScript(t, scriptsDir, "default_before_all.sh", `#!/bin/bash
echo hit >> `+defaultLog+`
exit 0`)
	otherScript := createActionsTestScript(t, scriptsDir, "other_before_all.sh", `#!/bin/bash
echo hit >> `+otherLog+`
exit 0`)

	seedActionMod(t, svc, game, "src", "1", "Mod One", "1.0", true, map[string][]byte{"one.esp": []byte("d")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "1", "1.0")
	appendProfileHooksYAML(t, svc.ConfigDir(), game.ID, "default", defaultScript)

	// seedActionMod always saves under the "default" profile (see its own
	// doc comment) - mod 2 needs a real DB row under "other", so it's saved
	// directly here instead.
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "2", SourceID: "src", Name: "Mod Two", Version: "1.0", GameID: game.ID},
		ProfileName:  "other",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	seedActionProfileMod(t, svc, game.ID, "other", "src", "2", "1.0")
	appendProfileHooksYAML(t, svc.ConfigDir(), game.ID, "other", otherScript)

	_, err := actions.UninstallMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Mod One"})
	require.NoError(t, err)
	defaultLogContent, err := os.ReadFile(defaultLog)
	require.NoError(t, err)
	assert.Equal(t, "hit\n", string(defaultLogContent), "must resolve and run the DEFAULT profile's own hook")
	_, err = os.ReadFile(otherLog)
	assert.True(t, os.IsNotExist(err), "the OTHER profile's hook must not have run yet")

	rebinder, ok := actions.(interface{ SetProfile(string) })
	require.True(t, ok, "coreProvider must implement the profileRebinder shape")
	rebinder.SetProfile("other")

	_, err = actions.UninstallMod(context.Background(), tui.ModItem{ID: "2", Source: "src", Name: "Mod Two"})
	require.NoError(t, err)
	otherLogContent, err := os.ReadFile(otherLog)
	require.NoError(t, err, "the cache must have been invalidated by SetProfile - a stale DEFAULT-profile cache would never resolve/run this hook")
	assert.Equal(t, "hit\n", string(otherLogContent))
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
// one level up - proving the mixed deployed/failed count renders correctly,
// and (Task 7 addition) that the skip reason itself surfaces in
// Outcome.Warnings so the TUI status line can explain WHY a mod failed
// rather than just how many did.
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
	require.Len(t, outcome.Warnings, 1, "the skip reason (DeployResult.Skipped) must be appended to Outcome.Warnings")
	assert.Contains(t, outcome.Warnings[0], "Mod Two")
	assert.Contains(t, outcome.Warnings[0], "failed to fetch")

	_, err = os.Lstat(filepath.Join(game.ModPath, "one.esp"))
	assert.NoError(t, err, "the mod with an intact cache entry should still deploy")
	_, err = os.Lstat(filepath.Join(game.ModPath, "two.esp"))
	assert.True(t, os.IsNotExist(err), "the mod whose redownload failed must not be deployed")
}

// --- coreProvider: PurgeProfile (Task 7) ---

// seedDeployedActionMod stores files in game's cache, saves an InstalledMod
// DB row with Deployed already true, and actually deploys the files via
// Installer.Install - the seed+install recipe TestCoreProviderDeployedFiles
// (Task 3, above) established, extended with an explicit Deployed:true seed
// (mirroring newCoreProviderFixture's own SkyUI row) so a purge test can
// observe it flip to false.
func seedDeployedActionMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version string, files map[string][]byte) {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, path, content))
	}
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: name, Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: modID, SourceID: sourceID, Version: version, GameID: game.ID}, "default"))
}

// TestCoreProviderActions_PurgeProfile drives PurgeProfile against a real
// Service: two mods are seeded pre-deployed and actually installed
// (seedDeployedActionMod), then purged - proving the files are undeployed
// for real, both DB rows flip Deployed=false (not deleted - Uninstall is
// always false, see coreProvider.PurgeProfile's own doc comment), the
// outcome message counts them, and the progress adapter streams the
// DeployPurging header plus a "✓"-line per mod (purgeProgressLine's
// composed lines, observed here directly - unlike the TUI-model-level pump
// tests in purge_test.go, this calls the progress callback synchronously
// per event with no single-slot-channel coalescing in between).
func TestCoreProviderActions_PurgeProfile(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedDeployedActionMod(t, svc, game, "src", "1", "Mod One", "1.0", map[string][]byte{"one.esp": []byte("1")})
	seedDeployedActionMod(t, svc, game, "src", "2", "Mod Two", "1.0", map[string][]byte{"two.esp": []byte("2")})

	var ticks []tui.ActionProgress
	outcome, err := actions.PurgeProfile(context.Background(), func(p tui.ActionProgress) { ticks = append(ticks, p) })
	require.NoError(t, err)
	assert.Equal(t, "Purged 2 mod(s)", outcome.Message)
	assert.Empty(t, outcome.Warnings)

	_, err = os.Lstat(filepath.Join(game.ModPath, "one.esp"))
	assert.True(t, os.IsNotExist(err), "purge must undeploy mod 1's files")
	_, err = os.Lstat(filepath.Join(game.ModPath, "two.esp"))
	assert.True(t, os.IsNotExist(err), "purge must undeploy mod 2's files")

	mod1, err := svc.GetInstalledMod("src", "1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod1.Deployed, "purge must flip Deployed to false, not delete the record")
	mod2, err := svc.GetInstalledMod("src", "2", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod2.Deployed)

	var lines []string
	for _, tick := range ticks {
		lines = append(lines, tick.Line)
	}
	assert.Contains(t, lines, "purging 2 mod(s)…")
	checkmarks := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "✓ ") {
			checkmarks++
		}
	}
	assert.Equal(t, 2, checkmarks, "must observe a ✓-line per purged mod")
}

// TestCoreProviderActions_PurgeProfile_SkipAndWarningsSurface guards the
// Skipped -> Warnings prefixing coreProvider.PurgeProfile documents
// (service_core.go's prefixSkipped): a game-level uninstall.before_each
// hook that always fails skips the mod entirely (core.PurgeResult.Skipped,
// not Purged), and that skip must surface in ActionOutcome.Warnings with
// its own "Skipped " lead-in. Uses the SAME direct
// game.Hooks.Uninstall.BeforeEach technique
// TestCoreProviderActions_UninstallMod_RunsUninstallHooksMatchingCLIConfig
// (above) already established in this file - simpler than round-tripping a
// hooks.yaml fixture, and already proven to resolve through
// coreProvider.resolvedHooks, so this test reuses it rather than
// introducing a second hook-wiring mechanism.
func TestCoreProviderActions_PurgeProfile_SkipAndWarningsSurface(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	scriptsDir := t.TempDir()
	failScript := createActionsTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	game.Hooks.Uninstall.BeforeEach = failScript

	seedDeployedActionMod(t, svc, game, "src", "1", "Test Mod", "1.0", map[string][]byte{"plugin.esp": []byte("data")})

	outcome, err := actions.PurgeProfile(context.Background(), nil)
	require.NoError(t, err, "a before_each skip must not fail the whole purge")
	assert.Equal(t, "Purged 0 mod(s)", outcome.Message)
	require.Len(t, outcome.Warnings, 1)
	assert.Contains(t, outcome.Warnings[0], "Skipped Test Mod: uninstall.before_each hook failed")

	_, err = os.Lstat(filepath.Join(game.ModPath, "plugin.esp"))
	assert.NoError(t, err, "a skipped mod's files must remain deployed - purge never touched it")

	mod, err := svc.GetInstalledMod("src", "1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.Deployed, "a skipped mod's Deployed flag must be untouched")
}

// TestCoreProviderActions_PurgeProfile_Empty guards the empty-mods
// short-circuit (coreProvider.PurgeProfile's own doc comment): no installed
// mods means no core.PurgeProfile call at all, just a plain "no mods
// installed" outcome with no error and no warnings.
func TestCoreProviderActions_PurgeProfile_Empty(t *testing.T) {
	actions, _, _ := newCoreActionsFixture(t)

	outcome, err := actions.PurgeProfile(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "no mods installed", outcome.Message)
	assert.Empty(t, outcome.Warnings)
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

// TestCoreProviderActions_ApplyProfileSwitch_DownloadsAndAppliesNeedsDownloads
// is the RED test proving the Phase 5b Task 4 switch-refusal LIFT: this
// EXACT scenario (a target profile referencing a mod with no installed row
// at all) previously made ApplyProfileSwitch refuse outright
// (errProfileNeedsDownloads - see this test's prior form, formerly named
// TestCoreProviderActions_ApplyProfileSwitch_RefusesWhenNeedsDownloads, in
// this file's git history). With a real fetchable/downloadable source
// registered, it must now proceed: download and install the missing mod,
// stream progress for it, and complete the switch (SetDefault) exactly like
// any other plan. Against the still-refusing implementation (this task's
// first commit) this fails with the refusal error instead of succeeding.
func TestCoreProviderActions_ApplyProfileSwitch_DownloadsAndAppliesNeedsDownloads(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)
	seedActionProfileMod(t, svc, game.ID, "target", "src", "modD", "2.0")

	netSrc := newNetSource(t, "src")
	svc.RegisterSource(netSrc)
	netSrc.addMod(game.ID, &domain.Mod{ID: "modD", SourceID: "src", Name: "Mod D", Version: "2.0", GameID: game.ID})
	netSrc.addDownload("modD", netSourceTestZip(t, "modd.esp", "modd-payload"))

	var ticks []tui.ActionProgress
	outcome, err := actions.ApplyProfileSwitch(context.Background(), "target",
		func(p tui.ActionProgress) { ticks = append(ticks, p) })
	require.NoError(t, err, "a plan needing downloads must now proceed instead of being refused")
	assert.Equal(t, `Switched to "target"`, outcome.Message)
	assert.NotEmpty(t, ticks, "downloading the missing mod must stream progress")

	def, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "target", def.Name, "the switch must actually apply now")

	installed, err := svc.GetInstalledMod("src", "modD", game.ID, "target")
	require.NoError(t, err)
	assert.True(t, installed.Enabled)

	_, err = os.Lstat(filepath.Join(game.ModPath, "modd.esp"))
	assert.NoError(t, err, "the downloaded mod must actually be deployed")
}

// TestCoreProviderActions_ApplyProfileSwitch_SurfacesInstallFailureAsWarning
// is the RED test for the Fix wave 2 review finding: a NeedsDownloads switch
// whose per-mod install fails (here, the fake source errors fetching the
// mod - one of the install loop's mod-fatal-only failure reasons, see
// SwitchInstallError's doc comment in flows.go) still completes the switch
// per core's own semantics (the install loop's fail() closure just
// `continue`s to the next ToInstall entry; SetDefault always runs after the
// loop) - but core.SwitchResult never records the failure anywhere
// (SwitchInstallError's doc comment: "these are NOT accumulated into any
// SwitchResult slice"), and coreProvider.ApplyProfileSwitch's own
// result.Notes-only Warnings mapping never sees it either. Against the
// pre-fix implementation this asserts require.NotEmpty on an Outcome.Warnings
// that is actually empty - the silent "Switched to X" with zero warnings the
// finding describes.
func TestCoreProviderActions_ApplyProfileSwitch_SurfacesInstallFailureAsWarning(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)
	seedActionProfileMod(t, svc, game.ID, "target", "src", "modZ", "1.0")

	netSrc := newNetSource(t, "src")
	netSrc.getModErr = errors.New("connection refused")
	svc.RegisterSource(netSrc)

	outcome, err := actions.ApplyProfileSwitch(context.Background(), "target", nil)
	require.NoError(t, err, "core reports a per-mod install failure via a progress event, not a fatal error - the switch itself must still complete")
	assert.Equal(t, `Switched to "target"`, outcome.Message)
	require.NotEmpty(t, outcome.Warnings, "a failed per-mod install during a NeedsDownloads switch must surface as a warning, not vanish silently")
	assert.Contains(t, outcome.Warnings[0], "modZ", "the warning must identify WHICH mod failed")
	assert.Contains(t, outcome.Warnings[0], "connection refused", "the warning must carry the underlying failure reason")

	def, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "target", def.Name, "the switch must still complete despite the failed install")

	_, err = svc.GetInstalledMod("src", "modZ", game.ID, "target")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "the failed mod must not have been installed")
}

// --- C1: profile rebind after a TUI-driven switch ---

// TestCoreProviderSetProfile_RebindsWhichProfileProfilesMarksActive guards
// finding C1's DataProvider half: coreProvider.profile is fixed at
// NewCoreProvider construction time and, absent a rebind hook, never
// reflects a later profile switch even though core.Service.
// ApplyProfileSwitch persists the new default profile via
// ProfileManager.SetDefault (see ApplyProfileSwitch's own doc comment in
// internal/core/flows.go). SetProfile is the optional profileRebinder hook
// app.go's actionDoneMsg handler calls after a successful switch; this test
// drives it directly against a real coreProvider/temp sandbox, independent
// of the Model-level wiring covered by mutations_test.go's fakes.
func TestCoreProviderSetProfile_RebindsWhichProfileProfilesMarksActive(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "hardcore")
	require.NoError(t, err)

	rebinder, ok := provider.(interface{ SetProfile(string) })
	require.True(t, ok, "coreProvider must implement the profileRebinder hook (SetProfile(string))")
	rebinder.SetProfile("hardcore")

	profiles, err := provider.Profiles(context.Background())
	require.NoError(t, err)
	byName := map[string]tui.ProfileItem{}
	for _, p := range profiles {
		byName[p.Name] = p
	}
	require.True(t, byName["hardcore"].Active, "SetProfile must rebind which profile Profiles() marks Active")
	require.False(t, byName["default"].Active, "the profile bound at construction must no longer read as active")
}

// TestCoreProviderActions_SetProfile_RebindsSubsequentMutationsToNewProfile
// guards finding C1's ActionProvider half: after SetProfile retargets the
// session, EnableMod (and by extension every other ActionProvider method
// keyed on p.profile) must address the NEW profile's DB row, not the
// profile bound at NewCoreActions construction time. recordingActions can't
// catch this class of bug - it never touches a real DB - so this drives the
// real coreProvider against a temp sandbox, mirroring this file's other
// ActionProvider tests.
func TestCoreProviderActions_SetProfile_RebindsSubsequentMutationsToNewProfile(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)

	// Installed under "target" only - NewCoreActions (via
	// newCoreActionsFixture) was bound to "default".
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "1", "1.0", "plugin.esp", []byte("data")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "1", SourceID: "src", Name: "Test Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "target",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      false,
	}))
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "src", ModID: "1", Version: "1.0"}))

	rebinder, ok := actions.(interface{ SetProfile(string) })
	require.True(t, ok, "coreProvider must implement the profileRebinder hook (SetProfile(string))")
	rebinder.SetProfile("target")

	_, err = actions.EnableMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Test Mod"})
	require.NoError(t, err)

	targetMod, err := svc.GetInstalledMod("src", "1", game.ID, "target")
	require.NoError(t, err)
	assert.True(t, targetMod.Enabled, "EnableMod after SetProfile must enable the mod under the TARGET profile")

	_, err = svc.GetInstalledMod("src", "1", game.ID, "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "EnableMod must not fall back to the profile bound at construction time")
}

func TestCoreProviderActions_ApplyProfileSwitch_AppliesAndReturnsMessage(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)

	seedActionMod(t, svc, game, "src", "modC", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "modC", "1.0")

	outcome, err := actions.ApplyProfileSwitch(context.Background(), "target", nil)
	require.NoError(t, err)
	assert.Equal(t, `Switched to "target"`, outcome.Message)

	def, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "target", def.Name)

	mod, err := svc.GetInstalledMod("src", "modC", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "modC must have been disabled by the switch")
}

// --- coreProvider: PlanInstall/ApplyInstall/CheckUpdates/ApplyUpdate
// (Phase 5b Task 4) ---

// TestCoreProviderActions_PlanInstall_MapsFilesDepsConflictsAndSize guards
// coreProvider.PlanInstall's mapping to InstallPlanView against a real
// core.Service sandbox: files, a resolved dependency, a conflict against an
// already-deployed OTHER mod's file, and a size label all round-trip.
func TestCoreProviderActions_PlanInstall_MapsFilesDepsConflictsAndSize(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)

	// modA is already installed, cached, and deployed under "shared.esp" -
	// installing modB (which also declares "shared.esp") must report a
	// conflict against it.
	seedActionMod(t, svc, game, "src", "modA", "Mod A", "1.0", true, map[string][]byte{"shared.esp": []byte("a")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "modA", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	netSrc := newNetSource(t, "src")
	svc.RegisterSource(netSrc)

	netSrc.addMod(game.ID, &domain.Mod{ID: "depX", SourceID: "src", Name: "Dep X", Version: "1.0", GameID: game.ID})
	netSrc.addMod(game.ID, &domain.Mod{ID: "modB", SourceID: "src", Name: "Mod B", Version: "1.0", GameID: game.ID})
	netSrc.deps["modB"] = []domain.ModReference{{SourceID: "src", ModID: "depX", Version: "1.0"}}
	netSrc.files["modB"] = []domain.DownloadableFile{{ID: "fileB", Name: "Mod B Archive", FileName: "modB.zip", IsPrimary: true, Size: 4096}}

	// PlanInstall never downloads (see InstallPlan.Conflicts' doc comment),
	// so GetConflicts only finds something if modB is ALREADY cached at
	// this exact version - pre-cache its conflicting file directly.
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "modB", "1.0", "shared.esp", []byte("b")))

	view, err := actions.PlanInstall(context.Background(), tui.ModItem{ID: "modB", Source: "src", Name: "Mod B"})
	require.NoError(t, err)

	assert.Equal(t, "Mod B", view.Name)
	assert.Equal(t, "1.0", view.Version)
	assert.Equal(t, "src", view.Source)
	require.Len(t, view.Files, 1)
	assert.Contains(t, view.Files[0], "Mod B Archive")
	require.Len(t, view.Dependencies, 1)
	assert.Contains(t, view.Dependencies[0], "Dep X")
	require.Len(t, view.Conflicts, 1)
	assert.Contains(t, view.Conflicts[0], "shared.esp")
	assert.Contains(t, view.Conflicts[0], "modA", "the conflict must name the OTHER mod that owns the file")
	assert.Equal(t, "4.0 KiB", view.SizeLabel)
	assert.False(t, view.Reinstall, "modB is not yet installed")
}

// TestCoreProviderActions_PlanInstall_SizeUnknownWhenFileSizeUnreported
// guards the "size unknown" branch (InstallPlanView.SizeLabel's doc
// comment).
func TestCoreProviderActions_PlanInstall_SizeUnknownWhenFileSizeUnreported(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)

	netSrc := newNetSource(t, "src")
	svc.RegisterSource(netSrc)
	netSrc.addMod(game.ID, &domain.Mod{ID: "modI", SourceID: "src", Name: "Mod I", Version: "1.0", GameID: game.ID})
	netSrc.files["modI"] = []domain.DownloadableFile{{ID: "fileI", Name: "Mod I Archive", FileName: "modI.zip", IsPrimary: true, Size: 0}}

	view, err := actions.PlanInstall(context.Background(), tui.ModItem{ID: "modI", Source: "src", Name: "Mod I"})
	require.NoError(t, err)
	assert.Equal(t, "size unknown", view.SizeLabel)
}

// TestCoreProviderActions_PlanInstall_ReinstallMarksExistingRow guards
// InstallPlanView.Reinstall: a mod already installed for this profile must
// report Reinstall true.
func TestCoreProviderActions_PlanInstall_ReinstallMarksExistingRow(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "modJ", "Mod J", "1.0", true, map[string][]byte{"j.esp": []byte("j")})

	netSrc := newNetSource(t, "src")
	svc.RegisterSource(netSrc)
	netSrc.addMod(game.ID, &domain.Mod{ID: "modJ", SourceID: "src", Name: "Mod J", Version: "1.0", GameID: game.ID})
	netSrc.files["modJ"] = []domain.DownloadableFile{{ID: "fileJ", Name: "Mod J Archive", FileName: "modJ.zip", IsPrimary: true, Size: 100}}

	view, err := actions.PlanInstall(context.Background(), tui.ModItem{ID: "modJ", Source: "src", Name: "Mod J"})
	require.NoError(t, err)
	assert.True(t, view.Reinstall)
}

// TestCoreProviderActions_PlanInstall_MapsAuthRequiredError and its
// not-supported sibling below guard the §7/auth error-mapping contract
// (task brief: "all four methods"). Every one of PlanInstall/ApplyInstall/
// CheckUpdates/ApplyUpdate shares the same mapNetworkError helper, so
// PlanInstall's coverage here (the cheapest call to trigger: a bare GetMod
// failure) is representative of the shared mapping logic; ApplyUpdate's own
// dedicated auth test below additionally proves it end-to-end through a
// DIFFERENT call site (CheckUpdates, not GetMod).
func TestCoreProviderActions_PlanInstall_MapsAuthRequiredError(t *testing.T) {
	actions, svc, _ := newCoreActionsFixture(t)
	netSrc := newNetSource(t, "src")
	netSrc.getModErr = domain.ErrAuthRequired
	svc.RegisterSource(netSrc)

	_, err := actions.PlanInstall(context.Background(), tui.ModItem{ID: "modK", Source: "src", Name: "Mod K"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Authentication required for src")
	assert.Contains(t, err.Error(), "lmm auth login src")
}

// TestCoreProviderActions_PlanInstall_MapsNotSupportedError additionally
// pins the install-path CLI fallback wording (mapInstallNetworkError): the
// notice must name installing and point at 'lmm install --source ... --id
// ...', the correct fallback for THIS action - see
// TestCoreProviderActions_ApplyUpdate_MapsNotSupportedError below for the
// sibling update-path proof that this wording must NOT leak into an
// updates-capability gap.
func TestCoreProviderActions_PlanInstall_MapsNotSupportedError(t *testing.T) {
	actions, svc, _ := newCoreActionsFixture(t)
	netSrc := newNetSource(t, "src")
	netSrc.getModErr = fmt.Errorf("fetching: %w", source.ErrNotSupported)
	svc.RegisterSource(netSrc)

	_, err := actions.PlanInstall(context.Background(), tui.ModItem{ID: "modK", Source: "src", Name: "Mod K"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `source "src" does not support installing`)
	assert.Contains(t, err.Error(), "lmm install --source src --id <mod-id>")
}

// TestCoreProviderActions_ApplyInstall_InstallsForRealWithHookParityAndProgress
// guards coreProvider.ApplyInstall against a real core.Service sandbox: the
// mod is genuinely downloaded/extracted/deployed/saved, the CLI's exact
// install hook configuration runs (a spy before_each hook), and at least
// one progress line is observed.
func TestCoreProviderActions_ApplyInstall_InstallsForRealWithHookParityAndProgress(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	scriptsDir := t.TempDir()
	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeEachScript := createActionsTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "install.before_each:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	game.Hooks.Install.BeforeEach = beforeEachScript

	netSrc := newNetSource(t, "src")
	svc.RegisterSource(netSrc)
	netSrc.addMod(game.ID, &domain.Mod{ID: "modE", SourceID: "src", Name: "Mod E", Version: "1.0", GameID: game.ID})
	netSrc.addDownload("modE", netSourceTestZip(t, "mode.esp", "payload"))

	item := tui.ModItem{ID: "modE", Source: "src", Name: "Mod E"}
	var ticks []tui.ActionProgress
	outcome, err := actions.ApplyInstall(context.Background(), item, func(p tui.ActionProgress) { ticks = append(ticks, p) })
	require.NoError(t, err)
	assert.Equal(t, `Installed "Mod E"`, outcome.Message)
	assert.NotEmpty(t, ticks, "must observe at least one progress line")

	installed, err := svc.GetInstalledMod("src", "modE", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, installed.Enabled)

	_, err = os.Lstat(filepath.Join(game.ModPath, "mode.esp"))
	assert.NoError(t, err, "the mod must actually be deployed")

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "install.before_each:modE\n", string(logContent),
		"ApplyInstall must run the CLI's exact install hook configuration")
}

// TestCoreProviderActions_ApplyInstall_ConflictAutoProceedsAndRecordsWarning
// guards the C1 review finding's TUI-side divergence: unlike the CLI, the
// TUI has no blocking conflict prompt (it can't - there's no stdin to read
// mid-action), so coreProvider.ApplyInstall's ConfirmConflicts callback must
// auto-proceed (never abort) while still surfacing the overwrite as a
// Warning instead of silently hiding it - "other" already owns shared.esp;
// installing a second mod whose own archive also contains shared.esp must
// overwrite it AND report the overwrite.
func TestCoreProviderActions_ApplyInstall_ConflictAutoProceedsAndRecordsWarning(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)

	seedActionMod(t, svc, game, "src", "other", "Other Mod", "1.0", true, map[string][]byte{"shared.esp": []byte("other-content")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "other", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	netSrc := newNetSource(t, "src")
	svc.RegisterSource(netSrc)
	netSrc.addMod(game.ID, &domain.Mod{ID: "modX", SourceID: "src", Name: "Mod X", Version: "1.0", GameID: game.ID})
	netSrc.addDownload("modX", netSourceTestZip(t, "shared.esp", "new-content"))

	item := tui.ModItem{ID: "modX", Source: "src", Name: "Mod X"}
	outcome, err := actions.ApplyInstall(context.Background(), item, nil)
	require.NoError(t, err, "the TUI must auto-proceed past a conflict, never blocking on stdin it doesn't have")
	assert.Equal(t, `Installed "Mod X"`, outcome.Message)
	require.NotEmpty(t, outcome.Warnings)
	assert.Contains(t, outcome.Warnings, "overwrote: shared.esp (owned by other)")

	content, err := os.ReadFile(filepath.Join(game.ModPath, "shared.esp"))
	require.NoError(t, err)
	assert.Equal(t, "new-content", string(content), "the TUI must actually overwrite, not silently skip")
}

// TestCoreProviderActions_ApplyInstall_BatchPrimaryFailureReflectsFailureNotFalseSuccess
// guards the I1 review finding: the BATCH path (plan.Dependencies non-empty)
// never fails ApplyInstall on a primary's own failure - the primary is
// recorded in result.Failed/Skipped instead (see InstallResult's doc
// comment) - so coreProvider.ApplyInstall's OLD unconditional "Installed %q"
// Message was a false success whenever the PRIMARY (not just a dependency)
// was the one that failed: a dependency installs, the primary 404s, and the
// status line still claimed "Installed" while search would report the mod
// as still available.
func TestCoreProviderActions_ApplyInstall_BatchPrimaryFailureReflectsFailureNotFalseSuccess(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)

	netSrc := newNetSource(t, "src")
	svc.RegisterSource(netSrc)
	dep := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: game.ID}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root Mod", Version: "1.0", GameID: game.ID}
	netSrc.addMod(game.ID, dep)
	netSrc.addMod(game.ID, root)
	netSrc.deps["root"] = []domain.ModReference{{SourceID: "src", ModID: "dep1"}}
	netSrc.addDownload("dep1", netSourceTestZip(t, "dep1.esp", "dep-payload"))
	// Deliberately no addDownload for "root" - its download 404s.

	item := tui.ModItem{ID: "root", Source: "src", Name: "Root Mod"}
	outcome, err := actions.ApplyInstall(context.Background(), item, nil)
	require.NoError(t, err, "a BATCH-path primary failure is not a hard error - it lands in Failed/Skipped instead")
	assert.NotEqual(t, `Installed "Root Mod"`, outcome.Message, "must not falsely claim the primary installed when it actually failed")
	assert.Equal(t, "Installed 1 of 2 mod(s)", outcome.Message)
	require.NotEmpty(t, outcome.Warnings, "the failure reason must be present, not just the bare name")
	assert.Contains(t, outcome.Warnings[0], "Root Mod")
	assert.Contains(t, outcome.Warnings[0], "download failed")

	_, dbErr := svc.GetInstalledMod("src", "dep1", game.ID, "default")
	assert.NoError(t, dbErr, "the dependency must still have installed")
	_, dbErr = svc.GetInstalledMod("src", "root", game.ID, "default")
	assert.Error(t, dbErr, "the primary must NOT be recorded as installed")
}

// TestCoreProviderActions_CheckUpdates_OneUpdateAndOneErroringSourceSurfacesWarning
// guards coreProvider.CheckUpdates against a real core.Service sandbox with
// two sources: one reports a real update, the other fails outright - the
// good source's update must still surface, with the failure as a Warning
// naming the failing source (Updater.CheckUpdates' own partial-results
// contract).
func TestCoreProviderActions_CheckUpdates_OneUpdateAndOneErroringSourceSurfacesWarning(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)

	seedActionMod(t, svc, game, "good", "modF", "Mod F", "1.0", true, nil)
	goodSrc := newNetSource(t, "good")
	svc.RegisterSource(goodSrc)
	goodSrc.updates = []domain.Update{{
		InstalledMod: domain.InstalledMod{Mod: domain.Mod{ID: "modF", SourceID: "good", Name: "Mod F", Version: "1.0"}},
		NewVersion:   "1.1",
	}}

	seedActionMod(t, svc, game, "flaky", "modG", "Mod G", "1.0", true, nil)
	flakySrc := newNetSource(t, "flaky")
	flakySrc.checkUpdatesErr = errors.New("connection refused")
	svc.RegisterSource(flakySrc)

	view, err := actions.CheckUpdates(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Updates, 1)
	assert.Equal(t, "modF", view.Updates[0].ID)
	assert.Equal(t, "1.0", view.Updates[0].FromVersion)
	assert.Equal(t, "1.1", view.Updates[0].ToVersion)
	require.NotEmpty(t, view.Warnings)
	assert.Contains(t, view.Warnings[0], "flaky")
}

// TestCoreProviderActions_CheckUpdates_MapsAuthRequiredError guards the
// §7/auth mapping for CheckUpdates specifically: unlike the other three
// methods, CheckUpdates' failure is a JOINED multi-source error (Updater.
// CheckUpdates' own contract), so the auth-hint wording here can't name one
// specific source the way PlanInstall/ApplyInstall/ApplyUpdate's mapping
// does - see coreProvider.CheckUpdates' doc comment for why.
func TestCoreProviderActions_CheckUpdates_MapsAuthRequiredError(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "modL", "Mod L", "1.0", true, nil)

	netSrc := newNetSource(t, "src")
	netSrc.checkUpdatesErr = domain.ErrAuthRequired
	svc.RegisterSource(netSrc)

	view, err := actions.CheckUpdates(context.Background())
	require.NoError(t, err, "a source failure is a Warning, never a hard error")
	require.NotEmpty(t, view.Warnings)
	assert.Contains(t, view.Warnings[0], "Authentication required")
	assert.Contains(t, view.Warnings[0], "lmm auth login")
}

// TestCoreProviderActions_ApplyUpdate_AppliesForRealAndPreservesRollbackPrecondition
// guards coreProvider.ApplyUpdate against a real core.Service sandbox,
// reusing Task 3's rollback-precondition arrangement
// (TestService_ApplyUpdate_RollbackPreconditionPreserved in
// internal/core/flows_update_test.go) one level up: the update genuinely
// applies (download, deploy, DB/profile writes), progress is observed, and
// 'lmm update rollback's precondition still holds afterward (the OLD
// version's cache entry survives, and PreviousVersion is recorded).
func TestCoreProviderActions_ApplyUpdate_AppliesForRealAndPreservesRollbackPrecondition(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)

	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "modH", "1.0", "modh-old.esp", []byte("old-content")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "modH", SourceID: "src", Name: "Mod H", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"old-1"},
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "modH", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	netSrc := newNetSource(t, "src")
	svc.RegisterSource(netSrc)
	netSrc.addMod(game.ID, &domain.Mod{ID: "modH", SourceID: "src", Name: "Mod H", Version: "2.0", GameID: game.ID})
	netSrc.files["modH"] = []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "modh-new.esp", IsPrimary: true}}
	netSrc.addDownload("new-1", netSourceTestZip(t, "modh-new.esp", "new-content"))
	netSrc.updates = []domain.Update{{
		InstalledMod: domain.InstalledMod{Mod: domain.Mod{ID: "modH", SourceID: "src", Name: "Mod H", Version: "1.0"}},
		NewVersion:   "2.0",
	}}

	item := tui.UpdateItem{Source: "src", ID: "modH", Name: "Mod H", FromVersion: "1.0", ToVersion: "2.0"}
	var ticks []tui.ActionProgress
	outcome, err := actions.ApplyUpdate(context.Background(), item, func(p tui.ActionProgress) { ticks = append(ticks, p) })
	require.NoError(t, err)
	assert.Contains(t, outcome.Message, "2.0")

	assert.True(t, svc.GetGameCache(game).Exists(game.ID, "src", "modH", "1.0"),
		"the previous version's cache entry must survive an update, for rollback")

	updated, err := svc.GetInstalledMod("src", "modH", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updated.Version)
	assert.Equal(t, "1.0", updated.PreviousVersion, "rollback precondition: PreviousVersion must be set")
	assert.True(t, svc.GetGameCache(game).Exists(game.ID, updated.SourceID, updated.ID, updated.PreviousVersion),
		"rollback precondition: the previous version must still be cached")

	_, err = os.Lstat(filepath.Join(game.ModPath, "modh-new.esp"))
	assert.NoError(t, err, "the new version's file must be deployed")
}

// TestCoreProviderActions_ApplyUpdate_MapsAuthRequiredError guards the
// §7/auth mapping for ApplyUpdate, triggered via its own re-check call
// (mirroring cmd/lmm/update.go's applySingleUpdate - see ApplyUpdate's doc
// comment for why it re-checks rather than reconstructing a domain.Update).
func TestCoreProviderActions_ApplyUpdate_MapsAuthRequiredError(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "modM", "Mod M", "1.0", true, nil)

	netSrc := newNetSource(t, "src")
	netSrc.checkUpdatesErr = domain.ErrAuthRequired
	svc.RegisterSource(netSrc)

	_, err := actions.ApplyUpdate(context.Background(), tui.UpdateItem{Source: "src", ID: "modM", Name: "Mod M", FromVersion: "1.0", ToVersion: "1.1"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Authentication required for src")
	assert.Contains(t, err.Error(), "lmm auth login src")
}

// TestCoreProviderActions_ApplyUpdate_MapsNotSupportedError guards the fix
// for the Task 4 review finding: ApplyUpdate re-checks via
// Updater.CheckUpdates before applying (see ApplyUpdate's doc comment), and
// a source lacking the Updates capability returns a wrapped
// source.ErrNotSupported from THAT re-check - previously mapNetworkError
// unconditionally suggested the install-path fallback ('lmm install
// --source ... --id ...') for every ErrNotSupported, which is actively
// wrong advice for an updates-capability gap. The message must name updates
// and must NOT mention lmm install.
func TestCoreProviderActions_ApplyUpdate_MapsNotSupportedError(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "modN", "Mod N", "1.0", true, nil)

	netSrc := newNetSource(t, "src")
	netSrc.checkUpdatesErr = fmt.Errorf("checking: %w", source.ErrNotSupported)
	svc.RegisterSource(netSrc)

	_, err := actions.ApplyUpdate(context.Background(), tui.UpdateItem{Source: "src", ID: "modN", Name: "Mod N", FromVersion: "1.0", ToVersion: "1.1"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `source "src" does not support checking for updates`)
	assert.Contains(t, err.Error(), "lmm update")
	assert.NotContains(t, err.Error(), "lmm install",
		"an updates-capability gap must never suggest the install-path fallback")
}

// TestCoreProviderActions_SetUpdatePolicy_PersistsAndReadsBack is Task 5's
// TestCoreProviderActions_CreateProfile proves CreateProfile persists a real,
// empty profile via svc.NewProfileManager().Create, readable back with
// pm.Get, and that a duplicate create is rejected (ProfileManager.Create's
// own "profile already exists" guard - internal/core/profile.go).
func TestCoreProviderActions_CreateProfile(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)

	outcome, err := actions.CreateProfile(context.Background(), "extra")
	require.NoError(t, err)
	assert.Equal(t, "Created profile: extra", outcome.Message)

	pm := svc.NewProfileManager()
	profile, err := pm.Get(game.ID, "extra")
	require.NoError(t, err)
	assert.Equal(t, "extra", profile.Name)

	_, err = actions.CreateProfile(context.Background(), "extra")
	assert.Error(t, err, "creating a colliding name a second time must error")
}

// TestCoreProviderActions_DeleteProfile proves DeleteProfile removes a real,
// non-active profile's YAML via svc.NewProfileManager().Delete - a
// subsequent pm.Get for the same name returns domain.ErrProfileNotFound.
func TestCoreProviderActions_DeleteProfile(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "extra")
	require.NoError(t, err)

	outcome, err := actions.DeleteProfile(context.Background(), "extra")
	require.NoError(t, err)
	assert.Equal(t, "Deleted profile: extra", outcome.Message)

	_, err = pm.Get(game.ID, "extra")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound)
}

// TestCoreProviderActions_DeleteActiveProfileRefused proves the
// defense-in-depth guard in coreProvider.DeleteProfile: deleting the
// session's own active profile ("default", per newCoreActionsFixture) errors
// without ever calling ProfileManager.Delete - the profile is still present
// afterward.
func TestCoreProviderActions_DeleteActiveProfileRefused(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)

	_, err := actions.DeleteProfile(context.Background(), "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete the active profile")

	pm := svc.NewProfileManager()
	profile, err := pm.Get(game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "default", profile.Name, "the refused delete must leave the active profile untouched")
}

// coreProvider guard: setting "auto" through the ActionProvider seam
// persists via svc.SetModUpdatePolicy, visible in a direct
// svc.GetInstalledMod read-back as domain.UpdateAuto - a real Service
// fixture, no recording fake, so this proves the actual DB write/mapping
// rather than just the wiring.
func TestCoreProviderActions_SetUpdatePolicy_PersistsAndReadsBack(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "modP", "Mod P", "1.0", true, nil)
	item := tui.ModItem{ID: "modP", Source: "src", Name: "Mod P"}

	outcome, err := actions.SetUpdatePolicy(context.Background(), item, "auto")
	require.NoError(t, err)
	assert.Equal(t, "Mod P update policy: auto", outcome.Message)

	mod, err := svc.GetInstalledMod("src", "modP", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, mod.UpdatePolicy)
}

// TestCoreProviderActions_SetUpdatePolicy_UnknownPolicyErrors guards the
// reject-not-default contract (service_core.go's parseUpdatePolicy): an
// unrecognized policy string errors instead of silently mapping to
// domain.UpdateNotify, and never reaches svc.SetModUpdatePolicy at all - the
// mod's policy in the DB stays untouched.
func TestCoreProviderActions_SetUpdatePolicy_UnknownPolicyErrors(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	seedActionMod(t, svc, game, "src", "modQ", "Mod Q", "1.0", true, nil)
	item := tui.ModItem{ID: "modQ", Source: "src", Name: "Mod Q"}

	_, err := actions.SetUpdatePolicy(context.Background(), item, "bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown policy "bogus"`)

	mod, err := svc.GetInstalledMod("src", "modQ", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateNotify, mod.UpdatePolicy, "an unknown policy must never mutate the stored policy")
}

// TestCoreProviderOverview_MapsUpdatePolicyToWireString extends
// TestCoreProviderOverview's own assertions (per task-5-brief.md: "extend
// the existing Overview test's assertions minimally") to cover the
// ModItem.UpdatePolicy field this task added: seeded UpdateAuto/UpdatePinned
// mods must stringify to "auto"/"pin" (policyToString's documented wire
// strings - NOT the CLI's own "pinned" spelling, see that function's doc
// comment) in the Overview mapping.
func TestCoreProviderOverview_MapsUpdatePolicyToWireString(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "201", SourceID: "nexusmods", GameID: game.ID, Name: "Auto Mod", Version: "1.0"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateAuto,
		Enabled:      true,
	}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "202", SourceID: "nexusmods", GameID: game.ID, Name: "Pinned Mod", Version: "1.0"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdatePinned,
		Enabled:      true,
	}))

	_, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)

	byID := map[string]tui.ModItem{}
	for _, m := range mods {
		byID[m.ID] = m
	}
	require.Contains(t, byID, "101", "pre-existing fixture mod must still be present")
	assert.Equal(t, "notify", byID["101"].UpdatePolicy, "the fixture's default UpdatePolicy (zero value) maps to notify")
	require.Contains(t, byID, "201")
	assert.Equal(t, "auto", byID["201"].UpdatePolicy)
	require.Contains(t, byID, "202")
	assert.Equal(t, "pin", byID["202"].UpdatePolicy)
}

// --- Task 8: in-TUI game switcher ---

// TestCoreProviderListGames guards ListGames' basic contract: every
// configured game is listed, sorted by Name (mirroring SourceInfos' own
// sorting - svc.ListGames ranges over an internal map, so iteration order
// isn't otherwise deterministic), with exactly the fixture's own game -
// the one bound at construction time - marked Active.
func TestCoreProviderListGames(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)

	second := &domain.Game{
		ID:          "second-game",
		Name:        "A Second Game", // sorts before "Test Game"
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
	}
	require.NoError(t, svc.AddGame(second))

	games, err := provider.ListGames()
	require.NoError(t, err)
	require.Len(t, games, 2)

	require.Equal(t, "A Second Game", games[0].Name, "must be sorted by Name")
	require.Equal(t, "Test Game", games[1].Name)

	byID := map[string]tui.GameInfo{}
	for _, g := range games {
		byID[g.ID] = g
	}
	require.True(t, byID[game.ID].Active, "the fixture's game (bound at construction) must be marked Active")
	require.False(t, byID[second.ID].Active)
}

// TestCoreProviderSetGame guards SetGame's rebind contract: after switching,
// Overview/Profiles reflect the NEW game's own data (not the game bound at
// construction); an unknown id is refused, with the current binding left
// completely untouched.
func TestCoreProviderSetGame(t *testing.T) {
	provider, svc, _ := newCoreProviderFixture(t)

	second := &domain.Game{
		ID:          "second-game",
		Name:        "Second Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
	}
	require.NoError(t, svc.AddGame(second))
	pm := svc.NewProfileManager()
	_, err := pm.Create(second.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(second.ID, "default"))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "301", SourceID: "nexusmods", GameID: second.ID, Name: "Second Game Mod", Version: "1.0"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	rebinder, ok := provider.(interface{ SetGame(string) error })
	require.True(t, ok, "coreProvider must implement the gameRebinder hook (SetGame(string) error)")
	require.NoError(t, rebinder.SetGame(second.ID))

	summary, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Second Game", summary.GameName)
	require.Equal(t, "default", summary.ProfileName, "SetGame must resolve the new game's OWN default profile")
	require.Len(t, mods, 1)
	require.Equal(t, "Second Game Mod", mods[0].Name)

	profiles, err := provider.Profiles(context.Background())
	require.NoError(t, err)
	require.Len(t, profiles, 1, "Profiles must be scoped to the NEW game, not the one bound at construction")
	require.Equal(t, "default", profiles[0].Name)
	require.True(t, profiles[0].Active)

	err = rebinder.SetGame("no-such-game")
	require.Error(t, err)
	summary, _, err = provider.Overview(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Second Game", summary.GameName, "an unknown id must leave the current binding untouched")
}

// TestCoreProviderActions_SetGame_InvalidatesHooksCache guards SetGame's
// hooks-cache invalidation, mirroring
// TestCoreProviderActions_SetProfile_InvalidatesHooksCache's own seam
// exactly (see that test's doc comment for the full reasoning) but keyed on
// GAME instead of profile: a stale ResolvedHooks cached from the FIRST
// game's hooks must never leak into an action run against the SECOND.
func TestCoreProviderActions_SetGame_InvalidatesHooksCache(t *testing.T) {
	actions, svc, game := newCoreActionsFixture(t)
	scriptsDir := t.TempDir()
	firstLog := filepath.Join(scriptsDir, "first.log")
	secondLog := filepath.Join(scriptsDir, "second.log")
	firstScript := createActionsTestScript(t, scriptsDir, "first_before_all.sh", `#!/bin/bash
echo hit >> `+firstLog+`
exit 0`)
	secondScript := createActionsTestScript(t, scriptsDir, "second_before_all.sh", `#!/bin/bash
echo hit >> `+secondLog+`
exit 0`)

	game.Hooks.Uninstall.BeforeAll = firstScript
	seedActionMod(t, svc, game, "src", "1", "Mod One", "1.0", true, map[string][]byte{"one.esp": []byte("d")})
	seedActionProfileMod(t, svc, game.ID, "default", "src", "1", "1.0")

	_, err := actions.UninstallMod(context.Background(), tui.ModItem{ID: "1", Source: "src", Name: "Mod One"})
	require.NoError(t, err)
	firstLogContent, err := os.ReadFile(firstLog)
	require.NoError(t, err)
	assert.Equal(t, "hit\n", string(firstLogContent), "must resolve and run the FIRST game's own hook")

	second := &domain.Game{
		ID:          "second-game",
		Name:        "Second Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
	}
	second.Hooks.Uninstall.BeforeAll = secondScript
	require.NoError(t, svc.AddGame(second))
	pm := svc.NewProfileManager()
	_, err = pm.Create(second.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(second.ID, "default"))
	seedActionMod(t, svc, second, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("d")})
	seedActionProfileMod(t, svc, second.ID, "default", "src", "2", "1.0")

	rebinder, ok := actions.(interface{ SetGame(string) error })
	require.True(t, ok, "coreProvider must implement the gameRebinder hook (SetGame(string) error)")
	require.NoError(t, rebinder.SetGame(second.ID))

	_, err = actions.UninstallMod(context.Background(), tui.ModItem{ID: "2", Source: "src", Name: "Mod Two"})
	require.NoError(t, err)
	secondLogContent, err := os.ReadFile(secondLog)
	require.NoError(t, err)
	assert.Equal(t, "hit\n", string(secondLogContent),
		"resolvedHooks must be recomputed after SetGame: the SECOND game's own hook must run, not one cached from the first")
}

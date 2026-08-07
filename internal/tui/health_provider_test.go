package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// newHealthProviderFixture builds a real *core.Service/game/"default"
// profile triple and returns both provider roles over it - mirroring
// service_core_test.go's newCoreProviderFixture/newCoreActionsFixture
// (package tui_test, and so unreachable from here: a different package
// cannot call another's unexported helpers - see this repo's established
// "duplicate the fixture, don't fight the package boundary" precedent, e.g.
// service_core_test.go's own netSource doc comment) - this file needs
// package tui specifically to unit-test the unexported healthView filter
// directly (Step 1's fourth test below).
func newHealthProviderFixture(t *testing.T) (DataProvider, ActionProvider, *core.Service, *domain.Game) {
	t.Helper()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID:          "health-game",
		Name:        "Health Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	return NewCoreProvider(svc, game, "default"), NewCoreActions(svc, game, "default"), svc, game
}

// seedHealthMod installs sourceID/modID as an enabled mod with the given
// fileIDs and, when storeCache is true, stores each file in the game cache
// under version - mirrors internal/core/verify_test.go's seedVerifyMod
// (unexported there, duplicated here for the same package-boundary reason
// documented on newHealthProviderFixture above).
func seedHealthMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version string, fileIDs []string, storeCache bool) {
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

// healthSourceBase implements every source.ModSource method this file's
// tests never exercise, so each variant below only needs to override what
// it actually cares about - mirrors service_core_test.go's stubSource/
// netSource split, one level up (package tui_test there, unreachable here).
type healthSourceBase struct{ id string }

func (s *healthSourceBase) ID() string      { return s.id }
func (s *healthSourceBase) Name() string    { return "Health Test Source" }
func (s *healthSourceBase) AuthURL() string { return "" }
func (s *healthSourceBase) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, errors.New("not implemented")
}
func (s *healthSourceBase) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (s *healthSourceBase) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, errors.New("not implemented")
}
func (s *healthSourceBase) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *healthSourceBase) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", errors.New("not implemented")
}
func (s *healthSourceBase) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// healthTrapSource fails the test outright if GetModFiles is ever called -
// the #224 Task 3 "VerifyLocal never touches the network" contract
// (internal/core/verify_test.go's TestVerify_LocalTier_NeverTouchesNetwork),
// reproduced here for coreProvider.Health.
type healthTrapSource struct {
	*healthSourceBase
	t *testing.T
}

func (s *healthTrapSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	s.t.Fatal("GetModFiles must not be called by coreProvider.Health (Local tier)")
	return nil, nil
}

// healthVersionSource scripts a per-modID GetModFiles outcome for the
// Full-tier version pass - mirrors internal/core/verify_test.go's
// scriptedVersionSource.
type healthVersionSource struct {
	*healthSourceBase
	filesByModID map[string][]domain.DownloadableFile
}

func (s *healthVersionSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return s.filesByModID[mod.ID], nil
}

// healthDownloadSource backs the --fix redownload fixture: GetModFiles
// always reports a single file ID "1" with an EMPTY Version (the version
// pass's own documented "vacuous OK" fallback via
// domain.EffectiveInstalledVersion - see verify.go's versionPass), and
// GetDownloadURL serves real bytes over an httptest.Server keyed by fileID -
// mirrors internal/core/verify_test.go's nonArchiveDownloadSource/
// mockSourceWithDownloads pair, combined into one type since this file only
// needs the one success-path scenario.
type healthDownloadSource struct {
	*healthSourceBase
	downloads map[string][]byte
	server    *httptest.Server
}

func newHealthDownloadSource(t *testing.T, id string) *healthDownloadSource {
	t.Helper()
	s := &healthDownloadSource{healthSourceBase: &healthSourceBase{id: id}, downloads: map[string][]byte{}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileID := filepath.Base(r.URL.Path)
		content, ok := s.downloads[fileID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(content)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *healthDownloadSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{{ID: "1", Name: "Main File", FileName: mod.ID + ".dat", IsPrimary: true}}, nil
}

func (s *healthDownloadSource) GetDownloadURL(_ context.Context, _ *domain.Mod, fileID string) (string, error) {
	return s.server.URL + "/" + fileID, nil
}

// TestHealthProvider_Local_NeverTouchesNetwork guards DataProvider.Health's
// documented contract: it runs core.VerifyLocal only, so a source
// registered with a GetModFiles trap must never have it fire, even though
// the seeded mod carries FileIDs (the version pass's own participation
// gate). A second, unhealthy mod (checksummed but never cached, so its
// per-file walk row is "missing") proves Health still surfaces real local
// findings while the healthy mod's quiet-ok row stays filtered out.
func TestHealthProvider_Local_NeverTouchesNetwork(t *testing.T) {
	provider, _, svc, game := newHealthProviderFixture(t)
	svc.RegisterSource(&healthTrapSource{healthSourceBase: &healthSourceBase{id: "trap-src"}, t: t})

	seedHealthMod(t, svc, game, "trap-src", "mod-ok", "Mod OK", "1.0", []string{"ok-file"}, true)
	require.NoError(t, svc.SaveFileChecksum("trap-src", "mod-ok", game.ID, "default", "ok-file", "checksum-ok"))

	seedHealthMod(t, svc, game, "trap-src", "mod-missing", "Mod Missing", "1.0", []string{"missing-file"}, false)
	require.NoError(t, svc.SaveFileChecksum("trap-src", "mod-missing", game.ID, "default", "missing-file", "checksum-missing"))

	view, err := provider.Health(context.Background())
	require.NoError(t, err)

	assert.False(t, view.Full, "Health always runs the Local tier")
	assert.Equal(t, 1, view.Issues, "only mod-missing's MISSING row counts as an issue")
	require.Equal(t, []HealthFinding{
		{ModID: "mod-missing", ModName: "Mod Missing", FileID: "missing-file", Status: "missing"},
	}, view.Findings, "mod-ok's quiet-ok row must be filtered out")
}

// TestHealthProvider_RunHealthCheck_FullTier_VersionStatusesAndProgress
// guards ActionProvider.RunHealthCheck's full=true path: opts.Tier moves to
// core.VerifyFull, so the version pass actually runs, its version_mismatch
// finding reaches HealthView.Findings, and progress receives both a
// "checking versions N/M: <name>" tick (VerifyEvProgress) and a
// "<status>: <name>" line (VerifyEvFinding) for the mismatch.
func TestHealthProvider_RunHealthCheck_FullTier_VersionStatusesAndProgress(t *testing.T) {
	_, actions, svc, game := newHealthProviderFixture(t)

	src := &healthVersionSource{
		healthSourceBase: &healthSourceBase{id: "vsrc"},
		filesByModID: map[string][]domain.DownloadableFile{
			"reachable-mod": {{ID: "f1", Version: "1.0", IsPrimary: true}},
			"mismatch-mod":  {{ID: "f1", Version: "2.0", IsPrimary: true}},
		},
	}
	svc.RegisterSource(src)

	seedHealthMod(t, svc, game, "vsrc", "reachable-mod", "Reachable", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum("vsrc", "reachable-mod", game.ID, "default", "f1", "cs-reachable"))
	seedHealthMod(t, svc, game, "vsrc", "mismatch-mod", "Mismatch", "1.0", []string{"f1"}, true)
	require.NoError(t, svc.SaveFileChecksum("vsrc", "mismatch-mod", game.ID, "default", "f1", "cs-mismatch"))

	var progress []string
	view, err := actions.RunHealthCheck(context.Background(), true, false, func(p ActionProgress) {
		progress = append(progress, p.Line)
	})
	require.NoError(t, err)

	assert.True(t, view.Full)
	assert.Equal(t, 1, view.Issues, "mismatch-mod's version_mismatch is the only issue")
	require.Len(t, view.Findings, 1)
	assert.Equal(t, "mismatch-mod", view.Findings[0].ModID)
	assert.Equal(t, "version_mismatch", view.Findings[0].Status)

	var sawProgressTick, sawFindingLine bool
	for _, line := range progress {
		if line == "checking versions 1/2: Reachable" || line == "checking versions 2/2: Mismatch" {
			sawProgressTick = true
		}
		if line == "version_mismatch: Mismatch" {
			sawFindingLine = true
		}
	}
	assert.True(t, sawProgressTick, "a VerifyEvProgress tick must reach progress: %v", progress)
	assert.True(t, sawFindingLine, "the version_mismatch VerifyEvFinding must reach progress: %v", progress)
}

// TestHealthProvider_RunHealthCheck_Fix_ResolvesMissingFile guards
// RunHealthCheck(full=true, fix=true) - the Health screen's 'F' binding
// contract ("always full", ActionProvider.RunHealthCheck's own doc comment)
// - actually applies CLI --fix semantics: a missing cached file gets
// re-downloaded and its checksum restored, so the finding resolves to a
// quiet ok and HealthView reports zero issues with no leftover row.
func TestHealthProvider_RunHealthCheck_Fix_ResolvesMissingFile(t *testing.T) {
	_, actions, svc, game := newHealthProviderFixture(t)

	src := newHealthDownloadSource(t, "rsrc")
	src.downloads["1"] = []byte("fresh content")
	svc.RegisterSource(src)

	seedHealthMod(t, svc, game, "rsrc", "mod1", "Mod One", "1.0", []string{"1"}, false)
	require.NoError(t, svc.SaveFileChecksum("rsrc", "mod1", game.ID, "default", "1", "old-checksum"))

	var progress []string
	view, err := actions.RunHealthCheck(context.Background(), true, true, func(p ActionProgress) {
		progress = append(progress, p.Line)
	})
	require.NoError(t, err)

	assert.True(t, view.Full)
	assert.Equal(t, 0, view.Issues, "the redownload repair resolves the missing issue")
	assert.Equal(t, 0, view.Warnings)
	assert.Empty(t, view.Findings, "the repaired row is quiet-ok and filtered out")
	assert.NotEmpty(t, progress, "the repair detail line must stream to progress")
}

// TestHealthProvider_RunHealthCheck_FindingProgressUsesSubjectFallback
// covers a Copilot round-2 finding (item 4): RunHealthCheck's VerifyEvFinding
// progress mapping rendered "<status>: <ModName>" verbatim, which produced a
// bare "stale_deployment: " line for a modless convergence finding (a
// dangling cache-rooted symlink with no owning mod - see
// TestHealthViewRendersSubjectFallbackForModlessFinding in
// health_screen_test.go, the same real case, for the list/detail pane side
// of this). The fix must reuse healthFindingSubject's ModName -> ModID ->
// FileID fallback so the progress line falls back to the FileID instead.
func TestHealthProvider_RunHealthCheck_FindingProgressUsesSubjectFallback(t *testing.T) {
	_, actions, svc, game := newHealthProviderFixture(t)

	cacheRoot := svc.GetGameCachePath(game)
	target := filepath.Join(cacheRoot, game.ID, "src-stray", "1.0", "stray.pak")
	require.NoError(t, os.Symlink(target, filepath.Join(game.ModPath, "stray.pak")))

	var progress []string
	view, err := actions.RunHealthCheck(context.Background(), false, false, func(p ActionProgress) {
		progress = append(progress, p.Line)
	})
	require.NoError(t, err)

	require.Len(t, view.Findings, 1)
	assert.Equal(t, "stale_deployment", view.Findings[0].Status)
	assert.Equal(t, "stray.pak", view.Findings[0].FileID)

	assert.Contains(t, progress, "stale_deployment: stray.pak", "the modless finding's progress line must fall back to the FileID: %v", progress)
	for _, line := range progress {
		assert.NotEqual(t, "stale_deployment: ", line, "must not render a blank ModName subject")
	}
}

// TestHealthView_FiltersQuietOkKeepsLockPending unit-tests healthView
// directly (package tui only, since it's unexported): a quiet-ok row
// (Status "ok", empty Note) is dropped, a lock-pending row (Status "ok",
// non-empty Note) is kept, and an ordinary non-ok row is kept - proving the
// filter is keyed on Status=="ok" && Note=="" exactly, not on Status alone.
func TestHealthView_FiltersQuietOkKeepsLockPending(t *testing.T) {
	res := &core.VerifyResult{
		Issues: 1, Warnings: 2,
		Findings: []core.VerifyFinding{
			{ModID: "a", ModName: "A", FileID: "f1", Status: "ok"},
			{ModID: "b", ModName: "B", Status: "ok", Note: "lock pending convergence (installed v1.0, locked v2.0)"},
			{ModID: "c", ModName: "C", FileID: "f2", Status: "missing"},
		},
	}

	view := healthView(res, true)

	assert.True(t, view.Full)
	assert.Equal(t, 1, view.Issues)
	assert.Equal(t, 2, view.Warnings)
	require.Len(t, view.Findings, 2)
	assert.Equal(t, "b", view.Findings[0].ModID, "lock-pending (ok + note) must be kept")
	assert.Equal(t, "c", view.Findings[1].ModID, "an ordinary non-ok row must be kept")
}

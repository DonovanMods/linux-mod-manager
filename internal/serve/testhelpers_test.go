package serve_test

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/require"
)

// newFixtureService returns a *core.Service backed by fresh temp dirs, with
// one game (ID "g1", name "Fixture Game") registered - the seeded fixture
// every internal/serve page test renders against, matching the construction
// pattern internal/core's own tests use (see core's testhelpers_test.go).
func newFixtureService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:          "g1",
		Name:        "Fixture Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
	}))
	return svc
}

// newFixtureServiceNoGames is newFixtureService without the seeded game -
// the "nothing configured yet" case every page must still render cleanly.
func newFixtureServiceNoGames(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// fakeSourceMod is one mod fakeSource knows about: its catalog entry, the
// files AvailableModVersions/ModFiles resolve against, and (optionally) the
// changelog text Changelog reports - never HTML, matching
// source.ChangelogProvider's plain-text contract.
type fakeSourceMod struct {
	Mod       domain.Mod
	Files     []domain.DownloadableFile
	Changelog string
}

// fakeSource is a minimal source.ModSource (and source.ChangelogProvider)
// double for internal/serve's page tests: a fixed catalog, no network calls,
// no capability gaps beyond what each test explicitly sets up. It exists
// here (rather than reusing internal/core's own test doubles, which live in
// package core_test and aren't importable) because every read page that
// touches a mod's source data - details, search, updates, changelog -
// needs one registered against the fixture Service.
type fakeSource struct {
	id   string
	mods map[string]*fakeSourceMod
}

// newFakeSource builds a fakeSource with id and no mods; add them via
// addMod before registering it on a Service.
func newFakeSource(id string) *fakeSource {
	return &fakeSource{id: id, mods: map[string]*fakeSourceMod{}}
}

// addMod registers mod in the source's catalog, served by every
// fakeSource method keyed off mod.ID.
func (s *fakeSource) addMod(mod fakeSourceMod) *fakeSource {
	s.mods[mod.Mod.ID] = &mod
	return s
}

func (s *fakeSource) ID() string      { return s.id }
func (s *fakeSource) Name() string    { return "Fake Source" }
func (s *fakeSource) AuthURL() string { return "" }

func (s *fakeSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

// Search returns every catalog mod whose name contains query
// (case-insensitive), or every mod when query is empty - enough to exercise
// /search's rendering without a real search index.
func (s *fakeSource) Search(_ context.Context, query source.SearchQuery) (source.SearchResult, error) {
	var mods []domain.Mod
	q := strings.ToLower(query.Query)
	for _, m := range s.mods {
		if q == "" || strings.Contains(strings.ToLower(m.Mod.Name), q) {
			mods = append(mods, m.Mod)
		}
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].ID < mods[j].ID })
	return source.SearchResult{Mods: mods, TotalCount: len(mods)}, nil
}

func (s *fakeSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	m, ok := s.mods[modID]
	if !ok {
		return nil, domain.ErrModNotFound
	}
	mod := m.Mod
	return &mod, nil
}

func (s *fakeSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

func (s *fakeSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	m, ok := s.mods[mod.ID]
	if !ok {
		return nil, domain.ErrModNotFound
	}
	return m.Files, nil
}

func (s *fakeSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", source.ErrNotSupported
}

// CheckUpdates reports a mod as updatable whenever the catalog's version
// differs from the installed one.
func (s *fakeSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var updates []domain.Update
	for _, im := range installed {
		m, ok := s.mods[im.ID]
		if !ok || m.Mod.Version == im.Version {
			continue
		}
		updates = append(updates, domain.Update{InstalledMod: im, NewVersion: m.Mod.Version})
	}
	return updates, nil
}

// Changelog implements source.ChangelogProvider: plain text from the
// catalog entry, or source.ErrNotSupported when the mod has none.
func (s *fakeSource) Changelog(_ context.Context, _, modID, _ string) (string, error) {
	m, ok := s.mods[modID]
	if !ok || m.Changelog == "" {
		return "", source.ErrNotSupported
	}
	return m.Changelog, nil
}

var _ source.ModSource = (*fakeSource)(nil)
var _ source.ChangelogProvider = (*fakeSource)(nil)

// newFixtureServiceWithSource returns a *core.Service seeded with one game
// ("g1", "Fixture Game", set as the configured default game) whose
// SourceIDs map src's ID to itself (so Service.Search's aggregate path
// considers it), a "default" profile, and src registered on the Service.
// Every game/profile-scoped page test builds on this - it is
// newFixtureService plus the registered source and resolvable
// default game/profile those pages' resolveSelection (and its underlying
// core calls: ModDetail, Search, CheckGameUpdates) need.
func newFixtureServiceWithSource(t *testing.T, src *fakeSource) (*core.Service, *domain.Game) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	svc.RegisterSource(src)

	game := &domain.Game{
		ID:          "g1",
		Name:        "Fixture Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
		SourceIDs:   map[string]string{src.ID(): ""},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	_, err = svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	require.NoError(t, svc.SetDefaultGame(context.Background(), game.ID))

	return svc, game
}

// seedInstalledMod stores mod's files in game's cache (skipped when files is
// nil) and saves an InstalledMod DB record for it in the "default" profile,
// mirroring internal/core's own seedInstalledMod test helper (which lives in
// package core_test and isn't importable from here).
func seedInstalledMod(t *testing.T, svc *core.Service, game *domain.Game, mod domain.Mod, enabled bool, files map[string][]byte) {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(game.ID, mod.SourceID, mod.ID, mod.Version, path, content))
	}

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          mod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

package core_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// searchStubSource is a minimal ModSource whose Search returns canned data.
type searchStubSource struct {
	id      string
	caps    *source.Capabilities // nil = no CapabilityReporter (assumed fully capable)
	result  source.SearchResult
	err     error
	gotGame string // records the GameID the source was queried with
}

func (s *searchStubSource) ID() string      { return s.id }
func (s *searchStubSource) Name() string    { return s.id }
func (s *searchStubSource) AuthURL() string { return "" }
func (s *searchStubSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (s *searchStubSource) Search(ctx context.Context, q source.SearchQuery) (source.SearchResult, error) {
	s.gotGame = q.GameID
	if s.err != nil {
		return source.SearchResult{}, s.err
	}
	return s.result, nil
}
func (s *searchStubSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, nil
}
func (s *searchStubSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *searchStubSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (s *searchStubSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (s *searchStubSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// capsStubSource adds a CapabilityReporter to searchStubSource.
type capsStubSource struct{ *searchStubSource }

func (c *capsStubSource) Capabilities() source.Capabilities { return *c.caps }

func newAggregateTestService(t *testing.T, sources map[string]string, srcs ...source.ModSource) (*core.Service, *domain.Game) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	for _, s := range srcs {
		svc.RegisterSource(s)
	}
	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), SourceIDs: sources}
	require.NoError(t, svc.AddGame(game))
	return svc, game
}

func mods(sourceID string, entries ...string) []domain.Mod {
	out := make([]domain.Mod, 0, len(entries))
	for _, e := range entries {
		out = append(out, domain.Mod{ID: e, SourceID: sourceID, Name: e})
	}
	return out
}

func TestSearchAllSourcesMergesAndTags(t *testing.T) {
	a := &searchStubSource{id: "alpha", result: source.SearchResult{
		Mods: []domain.Mod{
			{ID: "cool-a", SourceID: "alpha", Name: "Cool A", Downloads: 5},
			{ID: "other-a", SourceID: "alpha", Name: "Unrelated", Downloads: 100},
		},
		TotalCount: 2,
	}}
	b := &searchStubSource{id: "beta", result: source.SearchResult{
		Mods:       []domain.Mod{{ID: "cool-b", SourceID: "beta", Name: "Cool B", Downloads: 50}},
		TotalCount: 7,
	}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": "", "beta": "mapped-beta"}, a, b)

	res, err := svc.SearchAllSources(context.Background(), game.ID, "cool", "", nil, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings)
	assert.Equal(t, 9, res.TotalCount)

	// Ranking: name matches ("Cool B" 50 > "Cool A" 5 by downloads) before
	// non-matches ("Unrelated"), stable and deterministic.
	require.Len(t, res.Mods, 3)
	assert.Equal(t, "cool-b", res.Mods[0].ID)
	assert.Equal(t, "cool-a", res.Mods[1].ID)
	assert.Equal(t, "other-a", res.Mods[2].ID)

	// Per-source game-ID mapping applied (empty mapping -> lmm game id).
	assert.Equal(t, "testgame", a.gotGame)
	assert.Equal(t, "mapped-beta", b.gotGame)
}

func TestSearchAllSourcesFailureIsWarning(t *testing.T) {
	ok := &searchStubSource{id: "local", result: source.SearchResult{
		Mods: mods("local", "modlet"), TotalCount: 1,
	}}
	flaky := &searchStubSource{id: "remote", err: fmt.Errorf("dial tcp: connection refused")}
	svc, game := newAggregateTestService(t, map[string]string{"local": "", "remote": ""}, ok, flaky)

	res, err := svc.SearchAllSources(context.Background(), game.ID, "mod", "", nil, 0, 10)
	require.NoError(t, err, "one flaky source must not fail the aggregate")
	require.Len(t, res.Mods, 1)
	assert.Equal(t, "local", res.Mods[0].SourceID)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "remote", res.Warnings[0].SourceID)
}

func TestSearchAllSourcesSkipsNonSearching(t *testing.T) {
	searcher := &searchStubSource{id: "manifest", result: source.SearchResult{Mods: mods("manifest", "m1"), TotalCount: 1}}
	caps := source.Capabilities{Search: false, Updates: true}
	idOnly := &capsStubSource{&searchStubSource{id: "id-only-api", caps: &caps,
		err: fmt.Errorf("should never be called")}}
	svc, game := newAggregateTestService(t, map[string]string{"manifest": "", "id-only-api": ""}, searcher, idOnly)

	res, err := svc.SearchAllSources(context.Background(), game.ID, "m", "", nil, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings, "non-searching sources are skipped silently, not warned")
	require.Len(t, res.Mods, 1)
}

func TestSearchAllSourcesUnregisteredMappedSourceWarns(t *testing.T) {
	ok := &searchStubSource{id: "real", result: source.SearchResult{Mods: mods("real", "x"), TotalCount: 1}}
	svc, game := newAggregateTestService(t, map[string]string{"real": "", "ghost": ""}, ok)

	res, err := svc.SearchAllSources(context.Background(), game.ID, "x", "", nil, 0, 10)
	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "ghost", res.Warnings[0].SourceID)
	require.Len(t, res.Mods, 1)
}

func TestSearchAllSourcesAllFailedIsError(t *testing.T) {
	f1 := &searchStubSource{id: "s1", err: errors.New("boom1")}
	f2 := &searchStubSource{id: "s2", err: errors.New("boom2")}
	svc, game := newAggregateTestService(t, map[string]string{"s1": "", "s2": ""}, f1, f2)

	res, err := svc.SearchAllSources(context.Background(), game.ID, "x", "", nil, 0, 10)
	require.Error(t, err)
	assert.Len(t, res.Warnings, 2)
	assert.Empty(t, res.Mods)
}

func TestSearchAllSourcesUnknownGame(t *testing.T) {
	svc, _ := newAggregateTestService(t, map[string]string{})
	_, err := svc.SearchAllSources(context.Background(), "nope", "x", "", nil, 0, 10)
	assert.Error(t, err)
}

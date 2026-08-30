package core_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
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
	require.NoError(t, svc.SaveGame(context.Background(), game))
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

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "cool", "", nil, 0, 10)
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

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil, 0, 10)
	require.NoError(t, err, "one flaky source must not fail the aggregate")
	require.Len(t, res.Mods, 1)
	assert.Equal(t, "local", res.Mods[0].SourceID)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "remote", res.Warnings[0].SourceID)
	assert.Equal(t, res.Warnings[0].Err.Error(), res.Warnings[0].ErrorMessage,
		"ErrorMessage must mirror Err.Error() (newSourceWarning pairs them)")
}

func TestSearchAllSourcesSkipsNonSearching(t *testing.T) {
	searcher := &searchStubSource{id: "manifest", result: source.SearchResult{Mods: mods("manifest", "m1"), TotalCount: 1}}
	caps := source.Capabilities{Search: false, Updates: true}
	idOnly := &capsStubSource{&searchStubSource{id: "id-only-api", caps: &caps,
		err: fmt.Errorf("should never be called")}}
	svc, game := newAggregateTestService(t, map[string]string{"manifest": "", "id-only-api": ""}, searcher, idOnly)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "m", "", nil, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings, "non-searching sources are skipped silently, not warned")
	require.Len(t, res.Mods, 1)
}

func TestSearchAllSourcesUnregisteredMappedSourceWarns(t *testing.T) {
	ok := &searchStubSource{id: "real", result: source.SearchResult{Mods: mods("real", "x"), TotalCount: 1}}
	svc, game := newAggregateTestService(t, map[string]string{"real": "", "ghost": ""}, ok)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "x", "", nil, 0, 10)
	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "ghost", res.Warnings[0].SourceID)
	require.Len(t, res.Mods, 1)
}

func TestSearchAllSourcesAllFailedIsError(t *testing.T) {
	f1 := &searchStubSource{id: "s1", err: errors.New("boom1")}
	f2 := &searchStubSource{id: "s2", err: errors.New("boom2")}
	svc, game := newAggregateTestService(t, map[string]string{"s1": "", "s2": ""}, f1, f2)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "x", "", nil, 0, 10)
	require.Error(t, err)
	assert.Len(t, res.Warnings, 2)
	assert.Empty(t, res.Mods)
}

func TestSearchAllSourcesUnknownGame(t *testing.T) {
	svc, _ := newAggregateTestService(t, map[string]string{})
	_, err := svc.SearchAllSourcesForTest(context.Background(), "nope", "x", "", nil, 0, 10)
	assert.Error(t, err)
}

// --- #58 item 1: aggregate pagination must stop offering pages once every
// contributing source has exhausted its own results, rather than trusting
// the merged TotalCount (summed across sources with INDEPENDENT per-source
// pagination cursors) against a single global PageSize - see
// AggregateSearchResult.Exhausted's doc comment.

// TestSearchAllSourcesExhaustedWhenEveryCatalogFitsOnPageOne reproduces the
// reported overshoot: 3 sources whose ENTIRE catalog (10 mods each,
// TotalCount == pageSize) fits on page 0. Summed TotalCount (30) divided by
// the single requested pageSize (10) suggests 3 pages exist, but every
// source has already reported its whole catalog on page 0 - so Exhausted
// must already be true here, even though this is the FIRST page and every
// individual source's page came back "full" (len(Mods) == pageSize).
func TestSearchAllSourcesExhaustedWhenEveryCatalogFitsOnPageOne(t *testing.T) {
	newTenModSource := func(id string) *searchStubSource {
		return &searchStubSource{id: id, result: source.SearchResult{Mods: mods(id, "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9", "m10"), TotalCount: 10}}
	}
	a, b, c := newTenModSource("alpha"), newTenModSource("beta"), newTenModSource("gamma")
	svc, game := newAggregateTestService(t, map[string]string{"alpha": "", "beta": "", "gamma": ""}, a, b, c)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "m", "", nil, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, 30, res.TotalCount, "summed across sources, per AggregateSearchResult.TotalCount's contract")
	assert.True(t, res.Exhausted, "every source's own TotalCount says page 0 was its last page")
}

// TestSearchAllSourcesNotExhaustedWhenASourceHasMore is the counterpart: one
// source still has more (its TotalCount exceeds what page 0 could return),
// so the merge must NOT be marked exhausted.
func TestSearchAllSourcesNotExhaustedWhenASourceHasMore(t *testing.T) {
	exhausted := &searchStubSource{id: "alpha", result: source.SearchResult{Mods: mods("alpha", "m1"), TotalCount: 1}}
	hasMore := &searchStubSource{id: "beta", result: source.SearchResult{
		Mods:       mods("beta", "b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8", "b9", "b10"),
		TotalCount: 25,
	}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": "", "beta": ""}, exhausted, hasMore)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "m", "", nil, 0, 10)
	require.NoError(t, err)
	assert.False(t, res.Exhausted, "beta has 25 total and only returned page 0's 10, so more remain")
}

// TestSearchAllSourcesExhaustedFallsBackToShortPageWhenTotalUnknown mirrors
// the single-source sourceHasMore fallback for a source that reports no
// TotalCount at all: a short (partial) page is the only available "no more"
// signal.
func TestSearchAllSourcesExhaustedFallsBackToShortPageWhenTotalUnknown(t *testing.T) {
	shortPage := &searchStubSource{id: "alpha", result: source.SearchResult{Mods: mods("alpha", "m1"), TotalCount: 0}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, shortPage)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "m", "", nil, 0, 10)
	require.NoError(t, err)
	assert.True(t, res.Exhausted, "a short page (1 < pageSize 10) with no reported total means no more")
}

// --- #58 item 3: AttemptedCount distinguishes "no source supports search"
// from a genuine zero-result search - see AggregateSearchResult.
// AttemptedCount's doc comment.

// TestSearchAllSourcesAttemptedCountZeroWhenNoCapableSources covers a game
// whose configured sources are all real and registered, but NONE support
// searching (design §5 skips them silently) - the honesty-notice trigger
// case (#58 item 3): callers must be able to tell this apart from a capable
// source that legitimately found nothing.
func TestSearchAllSourcesAttemptedCountZeroWhenNoCapableSources(t *testing.T) {
	caps := source.Capabilities{Search: false, Updates: true}
	idOnly := &capsStubSource{&searchStubSource{id: "id-only-api", caps: &caps, err: fmt.Errorf("should never be called")}}
	svc, game := newAggregateTestService(t, map[string]string{"id-only-api": ""}, idOnly)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "m", "", nil, 0, 10)
	require.NoError(t, err, "zero capable sources is not itself a failure")
	assert.Equal(t, 0, res.AttemptedCount)
	assert.Empty(t, res.Mods)
}

// TestSearchAllSourcesAttemptedCountReflectsRealAttempts is the counterpart:
// a genuinely-searched, genuinely-empty result must NOT look like the
// zero-capable-sources case.
func TestSearchAllSourcesAttemptedCountReflectsRealAttempts(t *testing.T) {
	empty := &searchStubSource{id: "alpha", result: source.SearchResult{}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, empty)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "nothing-matches", "", nil, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, res.AttemptedCount)
	assert.Empty(t, res.Mods)
}

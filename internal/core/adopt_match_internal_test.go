package core

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for matchScannedMod - the source-matching half of PlanAdopt, lifted
// verbatim from cmd/lmm/import.go's tryMatchSources by v2 Phase 2 Unit K
// Task 18 (#291). These tests are cmd/lmm's own TestTryMatchSources_*
// suite, moved here with the engine they pin. The ERROR semantics (an error
// only when EVERY searchable source failed) are unchanged; the acceptance
// rule is not - #27 replaced "first searchable source with a hit wins" with
// scoring every candidate across every source (adopt_score.go), so these
// fixtures now name candidates the scanned archive actually matches instead
// of relying on whatever came back first.

// matchTestSource is a minimal source.ModSource double for the matcher: it
// returns searchMods verbatim from Search regardless of query, or searchErr,
// and reports caps so the "no searchable sources" case can be driven.
type matchTestSource struct {
	id         string
	caps       source.Capabilities
	searchMods []domain.Mod
	searchErr  error
	files      []domain.DownloadableFile
	filesErr   error
}

func newMatchTestSource(id string) *matchTestSource {
	return &matchTestSource{
		id:   id,
		caps: source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true},
	}
}

func (s *matchTestSource) ID() string                        { return s.id }
func (s *matchTestSource) Name() string                      { return s.id }
func (s *matchTestSource) AuthURL() string                   { return "" }
func (s *matchTestSource) Capabilities() source.Capabilities { return s.caps }
func (s *matchTestSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, nil
}

func (s *matchTestSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	if s.searchErr != nil {
		return source.SearchResult{}, s.searchErr
	}
	return source.SearchResult{Mods: s.searchMods, TotalCount: len(s.searchMods)}, nil
}

func (s *matchTestSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return nil, domain.ErrModNotFound
}

func (s *matchTestSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

func (s *matchTestSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if s.filesErr != nil {
		return nil, s.filesErr
	}
	return s.files, nil
}

func (s *matchTestSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", nil
}

func (s *matchTestSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// newMatchTestService builds a Service and registers game with it via
// SaveGame - required because matchScannedMod calls SourcesForGame, which
// resolves gameID against the service's own game registry, not a bare
// struct.
func newMatchTestService(t *testing.T) (*Service, *domain.Game) {
	t.Helper()

	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	return svc, game
}

// TestMatchScannedMod_NonBuiltinSourceMatches proves the generalization: a
// source with an arbitrary (non-CurseForge) ID supplies a match.
func TestMatchScannedMod_NonBuiltinSourceMatches(t *testing.T) {
	svc, game := newMatchTestService(t)
	src := newMatchTestSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "Acme Mod"}}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	matched, score, err := svc.matchScannedMod(context.Background(), game, "Acme Mod", "")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "acme-source", matched.SourceID)
	assert.Equal(t, "42", matched.ID)
	assert.InDelta(t, 1.0, score, 1e-9, "the names are identical once normalised")
}

// TestMatchScannedMod_MultiSourceOrder_CurseforgeBeforeNexusmods pins the
// ID-sorted iteration order design §4.2 calls out explicitly: "curseforge"
// sorts before "nexusmods" alphabetically, so typical two-built-in setups
// keep today's outcome. Registration order is deliberately the opposite of
// alphabetical, to prove the winner is decided by sort order, not
// registration order.
func TestMatchScannedMod_MultiSourceOrder_CurseforgeBeforeNexusmods(t *testing.T) {
	svc, game := newMatchTestService(t)
	cf := newMatchTestSource("curseforge")
	cf.searchMods = []domain.Mod{{ID: "1", SourceID: "curseforge", Name: "Shared Mod"}}
	nx := newMatchTestSource("nexusmods")
	nx.searchMods = []domain.Mod{{ID: "2", SourceID: "nexusmods", Name: "Shared Mod"}}
	svc.RegisterSource(nx)
	svc.RegisterSource(cf)
	game.SourceIDs = map[string]string{"curseforge": "g1", "nexusmods": "g1"}

	matched, _, err := svc.matchScannedMod(context.Background(), game, "Shared Mod", "")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "curseforge", matched.SourceID, "curseforge sorts before nexusmods alphabetically and must win an exact-score tie")
}

// TestMatchScannedMod_NoSearchableSources_CleanNoMatch guards the "no error"
// half of the contract: when the game's only configured source declares no
// search capability, the matcher returns a clean no-match, not an error -
// the loop has nothing to try, which is not a failure.
func TestMatchScannedMod_NoSearchableSources_CleanNoMatch(t *testing.T) {
	svc, game := newMatchTestService(t)
	src := newMatchTestSource("no-search")
	src.caps.Search = false
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"no-search": "g1"}

	matched, _, err := svc.matchScannedMod(context.Background(), game, "Anything", "")

	require.NoError(t, err)
	assert.Nil(t, matched)
}

// TestMatchScannedMod_NoConfiguredSources_CleanNoMatch covers the simplest
// no-searchable-sources case: a game with no sources configured at all.
func TestMatchScannedMod_NoConfiguredSources_CleanNoMatch(t *testing.T) {
	svc, game := newMatchTestService(t)

	matched, _, err := svc.matchScannedMod(context.Background(), game, "Anything", "")

	require.NoError(t, err)
	assert.Nil(t, matched)
}

// TestMatchScannedMod_FirstErrorsSecondEmpty_CleanNoMatchNotError is the
// scenario PR #124's review flagged: source A errors, source B searches
// successfully but finds nothing. The overall result must be a clean
// no-match (nil, nil), not A's stale error.
func TestMatchScannedMod_FirstErrorsSecondEmpty_CleanNoMatchNotError(t *testing.T) {
	svc, game := newMatchTestService(t)
	failing := newMatchTestSource("acme-fail")
	failing.searchErr = errors.New("boom")
	empty := newMatchTestSource("beta-empty") // no searchMods set: succeeds with zero results
	svc.RegisterSource(failing)
	svc.RegisterSource(empty)
	game.SourceIDs = map[string]string{"acme-fail": "g1", "beta-empty": "g1"}

	matched, _, err := svc.matchScannedMod(context.Background(), game, "Anything", "")

	require.NoError(t, err, "a later source's clean empty result must clear an earlier source's error")
	assert.Nil(t, matched)
}

// TestMatchScannedMod_AllSourcesError_ReturnsError guards the other half:
// when every searchable source fails, the round genuinely produced nothing
// usable and an error must still surface (not silently swallowed into a
// no-match).
func TestMatchScannedMod_AllSourcesError_ReturnsError(t *testing.T) {
	svc, game := newMatchTestService(t)
	a := newMatchTestSource("source-a")
	a.searchErr = errors.New("boom a")
	b := newMatchTestSource("source-b")
	b.searchErr = errors.New("boom b")
	svc.RegisterSource(a)
	svc.RegisterSource(b)
	game.SourceIDs = map[string]string{"source-a": "g1", "source-b": "g1"}

	matched, _, err := svc.matchScannedMod(context.Background(), game, "Anything", "")

	require.Error(t, err)
	assert.Nil(t, matched)
}

// TestMatchScannedMod_FirstEmptySecondMatches_ReturnsMatch guards that a
// clean empty result from an earlier source doesn't prevent a later
// source's real match from being found and returned.
func TestMatchScannedMod_FirstEmptySecondMatches_ReturnsMatch(t *testing.T) {
	svc, game := newMatchTestService(t)
	empty := newMatchTestSource("acme-empty")
	matchSrc := newMatchTestSource("beta-match")
	matchSrc.searchMods = []domain.Mod{{ID: "5", SourceID: "beta-match", Name: "Found It"}}
	svc.RegisterSource(empty)
	svc.RegisterSource(matchSrc)
	game.SourceIDs = map[string]string{"acme-empty": "g1", "beta-match": "g1"}

	matched, _, err := svc.matchScannedMod(context.Background(), game, "Found It", "")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "beta-match", matched.SourceID)
}

// --- #27: the acceptance rule itself, driven through matchScannedMod.

// TestMatchScannedMod_RefusesAWeakHit is the case the old first-hit rule
// got wrong: a source's own relevance ranking puts a DIFFERENT mod first
// for the query, and adopting it would track the archive as that mod.
func TestMatchScannedMod_RefusesAWeakHit(t *testing.T) {
	svc, game := newMatchTestService(t)
	src := newMatchTestSource("nexusmods")
	src.searchMods = []domain.Mod{
		{ID: "1", SourceID: "nexusmods", Name: "SkyUI Flashlite"},
		{ID: "2", SourceID: "nexusmods", Name: "SkyUI Weapons Pack"},
	}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"nexusmods": "g1"}

	matched, score, err := svc.matchScannedMod(context.Background(), game, "SkyUI", "")

	require.NoError(t, err, "no confident match is an ordinary outcome, not a failure")
	assert.Nil(t, matched, "the archive stays untracked rather than adopted as the wrong mod")
	assert.Less(t, score, adoptMatchThreshold, "the best near-miss is still reported")
}

// TestMatchScannedMod_PicksTheBestHitNotTheFirst: the right mod is in the
// same source's result list, just not first. The old rule took mods[0].
func TestMatchScannedMod_PicksTheBestHitNotTheFirst(t *testing.T) {
	svc, game := newMatchTestService(t)
	src := newMatchTestSource("nexusmods")
	src.searchMods = []domain.Mod{
		{ID: "1", SourceID: "nexusmods", Name: "SkyUI Flashlite"},
		{ID: "2", SourceID: "nexusmods", Name: "SkyUI"},
	}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"nexusmods": "g1"}

	matched, score, err := svc.matchScannedMod(context.Background(), game, "SkyUI", "")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "2", matched.ID)
	assert.InDelta(t, 1.0, score, 1e-9)
}

// TestMatchScannedMod_ScoresAcrossSources: the best candidate is in the
// SECOND source, while the first source answered with a plausible-looking
// near-miss. Under the old first-source-with-a-hit rule the near-miss won.
func TestMatchScannedMod_ScoresAcrossSources(t *testing.T) {
	svc, game := newMatchTestService(t)
	first := newMatchTestSource("alpha")
	first.searchMods = []domain.Mod{{ID: "1", SourceID: "alpha", Name: "Bigger Backpacks Redux"}}
	second := newMatchTestSource("beta")
	second.searchMods = []domain.Mod{{ID: "2", SourceID: "beta", Name: "Bigger Backpacks"}}
	svc.RegisterSource(first)
	svc.RegisterSource(second)
	game.SourceIDs = map[string]string{"alpha": "g1", "beta": "g1"}

	matched, _, err := svc.matchScannedMod(context.Background(), game, "Bigger Backpacks", "")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "beta", matched.SourceID, "the better-scoring candidate wins even in a later source")
}

// TestMatchScannedMod_ExactHitStopsEarly: an exact match in the first
// source cannot be beaten, so the remaining sources are not searched at
// all - the cost control on scoring across every source.
func TestMatchScannedMod_ExactHitStopsEarly(t *testing.T) {
	svc, game := newMatchTestService(t)
	first := newMatchTestSource("alpha")
	first.searchMods = []domain.Mod{{ID: "1", SourceID: "alpha", Name: "SkyUI"}}
	second := newMatchTestSource("beta")
	second.searchErr = errors.New("this source must never be reached")
	svc.RegisterSource(first)
	svc.RegisterSource(second)
	game.SourceIDs = map[string]string{"alpha": "g1", "beta": "g1"}

	matched, score, err := svc.matchScannedMod(context.Background(), game, "SkyUI", "")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "alpha", matched.SourceID)
	assert.InDelta(t, 1.0, score, 1e-9)
}

// TestMatchScannedMod_VersionLiftsANearMiss threads the parsed archive
// version through: the same name pair is refused without it and accepted
// with it.
func TestMatchScannedMod_VersionLiftsANearMiss(t *testing.T) {
	newSvc := func(t *testing.T) (*Service, *domain.Game) {
		svc, game := newMatchTestService(t)
		src := newMatchTestSource("nexusmods")
		src.searchMods = []domain.Mod{{ID: "1", SourceID: "nexusmods", Name: "SkyUI SE", Version: "5.2"}}
		svc.RegisterSource(src)
		game.SourceIDs = map[string]string{"nexusmods": "g1"}
		return svc, game
	}

	svc, game := newSvc(t)
	matched, _, err := svc.matchScannedMod(context.Background(), game, "SkyUI", "")
	require.NoError(t, err)
	assert.Nil(t, matched, "the name alone is not enough")

	svc, game = newSvc(t)
	matched, score, err := svc.matchScannedMod(context.Background(), game, "SkyUI", "5.2")
	require.NoError(t, err)
	require.NotNil(t, matched, "an agreeing version lifts the same pair over the bar")
	assert.Equal(t, AdoptMatchProbable, adoptMatchClass(score))
}

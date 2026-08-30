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
// Task 18 (#291). These seven tests are cmd/lmm's own TestTryMatchSources_*
// suite, moved here with the engine they pin; the acceptance rule ("first
// searchable source with a hit wins", in SourcesForGame's ID-sorted order)
// and the error semantics (an error only when EVERY searchable source
// failed) are unchanged.

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

	matched, err := svc.matchScannedMod(context.Background(), game, "Acme")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "acme-source", matched.SourceID)
	assert.Equal(t, "42", matched.ID)
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
	cf.searchMods = []domain.Mod{{ID: "1", SourceID: "curseforge", Name: "CF Match"}}
	nx := newMatchTestSource("nexusmods")
	nx.searchMods = []domain.Mod{{ID: "2", SourceID: "nexusmods", Name: "NX Match"}}
	svc.RegisterSource(nx)
	svc.RegisterSource(cf)
	game.SourceIDs = map[string]string{"curseforge": "g1", "nexusmods": "g1"}

	matched, err := svc.matchScannedMod(context.Background(), game, "Match")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "curseforge", matched.SourceID, "curseforge sorts before nexusmods alphabetically and must win")
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

	matched, err := svc.matchScannedMod(context.Background(), game, "Anything")

	require.NoError(t, err)
	assert.Nil(t, matched)
}

// TestMatchScannedMod_NoConfiguredSources_CleanNoMatch covers the simplest
// no-searchable-sources case: a game with no sources configured at all.
func TestMatchScannedMod_NoConfiguredSources_CleanNoMatch(t *testing.T) {
	svc, game := newMatchTestService(t)

	matched, err := svc.matchScannedMod(context.Background(), game, "Anything")

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

	matched, err := svc.matchScannedMod(context.Background(), game, "Anything")

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

	matched, err := svc.matchScannedMod(context.Background(), game, "Anything")

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

	matched, err := svc.matchScannedMod(context.Background(), game, "Anything")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "beta-match", matched.SourceID)
}

package core_test

// The per-source game identifier when it is EMPTY (T1 review #3).
//
// games.yaml's `sources: {<id>: <value>}` maps a game onto whatever each
// source calls it - a NexusMods slug, a CurseForge numeric id, a
// Thunderstore community. source.IgnoresGameIdentifier is the source's own
// answer to "may that be blank"; a source that implements it not at all
// REQUIRES the value. Core used to answer an empty mapping by falling back
// to lmm's own game id, which for Thunderstore meant downloading and
// searching whichever real community happened to share the lmm game's
// name - the exact thing the design's "there is deliberately no community
// auto-detection" was written to prevent.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newEmptyMappingService registers one source that requires the identifier
// and one that ignores it, and maps a game to BOTH with an empty value.
func newEmptyMappingService(t *testing.T) (*core.Service, *identifierSpySource, *domain.Game) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	needs := &identifierSpySource{mockSource: newMockSource("needs-id")}
	svc.RegisterSource(needs)
	svc.RegisterSource(&identifierIgnoringMockSource{mockSource: newMockSource("ignores-id")})

	game := &domain.Game{
		ID: "valheim", Name: "Valheim", ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"needs-id": "", "ignores-id": ""},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	return svc, needs, game
}

// identifierSpySource records the game identifier it was actually handed,
// which is the only way to tell "the mapping reached the source" from
// "lmm's own game id did".
type identifierSpySource struct {
	*mockSource
	lastSearchGameID string
}

func (s *identifierSpySource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	s.lastSearchGameID = query.GameID
	return s.mockSource.Search(ctx, query)
}

// identifierIgnoringMockSource is a directory source's shape: its mapped
// value addresses nothing, so an empty one is a configuration rather than
// a value the user left out.
type identifierIgnoringMockSource struct{ *mockSource }

func (s *identifierIgnoringMockSource) IgnoresGameIdentifier() bool { return true }

// TestSearchMods_EmptyMappingForASourceThatNeedsAnIdentifierIsRefused is
// the headline: lmm must not decide for itself what a source calls this
// game. The refusal is source.ErrGameIdentifierInvalid, and it names the
// command that fixes it, because that sentence is the whole remedy and
// both frontends print the error verbatim.
func TestSearchMods_EmptyMappingForASourceThatNeedsAnIdentifierIsRefused(t *testing.T) {
	svc, _, game := newEmptyMappingService(t)

	_, err := svc.SearchMods(t.Context(), "needs-id", game.ID, "anything", "", nil, 0, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
	assert.Contains(t, err.Error(), "lmm game edit valheim --source needs-id=",
		"the error has to carry the fix: the identifier is not derivable from anything lmm knows")
}

// TestSearchMods_EmptyMappingForASourceThatIgnoresTheIdentifierStillSearches
// is the other half, and the reason the question is asked of the SOURCE: a
// directory source is routinely mapped with nothing at all, and blanking
// its id out - or refusing it - would break every game configured that way.
func TestSearchMods_EmptyMappingForASourceThatIgnoresTheIdentifierStillSearches(t *testing.T) {
	svc, _, game := newEmptyMappingService(t)

	_, err := svc.SearchMods(t.Context(), "ignores-id", game.ID, "anything", "", nil, 0, 0)
	require.NoError(t, err)
}

// TestSearchMods_AMappedIdentifierIsWhatTheSourceSees pins the behaviour
// the refusal sits beside, unchanged: a non-empty mapping is what reaches
// the source, never lmm's own game id.
func TestSearchMods_AMappedIdentifierIsWhatTheSourceSees(t *testing.T) {
	svc, needs, game := newEmptyMappingService(t)
	updated := *game
	updated.SourceIDs = map[string]string{"needs-id": "valheim-community"}
	require.NoError(t, svc.SaveGame(t.Context(), &updated))

	_, err := svc.SearchMods(t.Context(), "needs-id", game.ID, "anything", "", nil, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, "valheim-community", needs.lastSearchGameID)
}

// TestAnAggregateSearchWarnsAboutAnEmptyMapping: the unscoped path must
// not fail the whole search over one misconfigured source - it warns, with
// the same remedy in the sentence, and the other sources still answer.
func TestAnAggregateSearchWarnsAboutAnEmptyMapping(t *testing.T) {
	svc, _, game := newEmptyMappingService(t)

	report, err := svc.Search(t.Context(), game, "", "anything", core.SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, report.Warnings)
	joined := ""
	for _, w := range report.Warnings {
		joined += w.ErrorMessage + "\n"
	}
	assert.Contains(t, joined, "lmm game edit valheim --source needs-id=")
}

// TestSourceIndexStatus_EmptyMappingIsRefused: the index surface asks the
// same question - "what have I got for this game" - and an empty mapping
// makes it unanswerable rather than answerable about a guess.
func TestSourceIndexStatus_EmptyMappingIsRefused(t *testing.T) {
	svc, _, game := newEmptyMappingService(t)
	indexed := newIndexedSource("indexed")
	svc.RegisterSource(indexed)
	updated := *game
	updated.SourceIDs = map[string]string{"indexed": ""}
	require.NoError(t, svc.SaveGame(t.Context(), &updated))

	_, err := svc.SourceIndexStatus(t.Context(), "indexed", game.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)

	_, err = svc.RefreshSourceIndex(t.Context(), "indexed", game.ID, false, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
	assert.Zero(t, indexed.refresh, "nothing may be fetched for a community the user never named")
}

// The WRITE half (T1 re-review Minor 1). Everything above refuses an empty
// mapping when it is READ; these refuse WRITING one. The read-path refusal
// makes the state survivable - a game configured that way fails at first
// use with the fixing command - but the point of the rule is that the state
// should not exist, and the README says it cannot ("`lmm game add` and `lmm
// game edit` both refuse an empty mapping for a source that needs one").
// Three entry points reach the write with a PREFILLED source map, none of
// which passes through the explicit SourceID/Identifier pair the original
// check guarded: `lmm game add --from-detected` and POST /api/v1/games with
// from_steam_app_id (both via GameSpec.Sources), and `lmm init` (via
// GameFromDetected, which never sees a GameSpec at all).

// newDetectableGameService is newEmptyMappingService's sibling for the
// write paths: the same two sources, and NO game - the point is what
// AddGame and ApplyGameDetect will accept.
func newDetectableGameService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&identifierSpySource{mockSource: newMockSource("needs-id")})
	svc.RegisterSource(&identifierIgnoringMockSource{mockSource: newMockSource("ignores-id")})
	return svc
}

// TestAddGame_APrefilledSourcesMapWithAnEmptyIdentifierIsRefused is the
// `game add --from-detected` / POST /api/v1/games path: the pair the switch
// guards is empty, so nothing was ever asked about the map cloned in
// beside it.
func TestAddGame_APrefilledSourcesMapWithAnEmptyIdentifierIsRefused(t *testing.T) {
	svc := newDetectableGameService(t)

	_, err := svc.AddGame(t.Context(), core.GameSpec{
		Name: "Valheim", ID: "valheim", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		Sources: map[string]string{"needs-id": ""},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "sources", specErr.Field, "a form marks the offending row")
	assert.Equal(t, "needs-id", specErr.Value)

	_, err = svc.GetGame("valheim")
	assert.Error(t, err, "the game must not be written at all")
}

// TestAddGame_APrefilledSourcesMapForAnIdentifierIgnoringSourceIsAccepted
// is the other half, and the reason the question is asked of the SOURCE: a
// directory source's mapped value addresses nothing, so blank is its
// ordinary configuration.
func TestAddGame_APrefilledSourcesMapForAnIdentifierIgnoringSourceIsAccepted(t *testing.T) {
	svc := newDetectableGameService(t)

	entry, err := svc.AddGame(t.Context(), core.GameSpec{
		Name: "Valheim", ID: "valheim", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		Sources: map[string]string{"ignores-id": ""},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"ignores-id": ""}, entry.SourceIDs)
}

// TestApplyGameDetect_AKnownGamesEntryWithAnEmptyIdentifierIsRefused is
// `lmm init`'s path: GameFromDetected writes g.Sources verbatim, and its
// one guard (#203's "no sources AND no nexus_id") cannot see an empty
// VALUE inside a non-empty map.
func TestApplyGameDetect_AKnownGamesEntryWithAnEmptyIdentifierIsRefused(t *testing.T) {
	svc := newDetectableGameService(t)

	_, err := svc.ApplyGameDetect(t.Context(), []domain.DetectedGame{{
		Slug: "valheim", Name: "Valheim", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		Sources: map[string]string{"needs-id": ""},
	}})
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "sources", specErr.Field)
	assert.Equal(t, "needs-id", specErr.Value)

	_, err = svc.GetGame("valheim")
	assert.Error(t, err, "the game must not be written at all")
}

// TestAddGame_APrefilledSourcesMapIsTrimmed (#409 review F8). The explicit
// SourceID/Identifier pair has always been trimmed and `lmm game edit`'s
// own map is trimmed by validatedSourceMap; a PREFILLED map was copied
// verbatim. A padded slug is then written, and every later read fails the
// source's own well-formedness gate - which is precisely the state the
// empty-identifier refusal exists to keep off disk.
func TestAddGame_APrefilledSourcesMapIsTrimmed(t *testing.T) {
	svc := newDetectableGameService(t)

	entry, err := svc.AddGame(t.Context(), core.GameSpec{
		Name: "Valheim", ID: "valheim", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		Sources: map[string]string{"needs-id": "  lethal-company	"},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"needs-id": "lethal-company"}, entry.SourceIDs)
}

// TestApplyGameDetect_AKnownGamesEntrysIdentifierIsTrimmed is the same rule
// on the curated path: a user's own ~/.config/lmm/steam-games.yaml is
// hand-written, so a trailing space in a mapped value is an ordinary typo
// rather than an exotic one.
func TestApplyGameDetect_AKnownGamesEntrysIdentifierIsTrimmed(t *testing.T) {
	svc := newDetectableGameService(t)

	_, err := svc.ApplyGameDetect(t.Context(), []domain.DetectedGame{{
		Slug: "valheim", Name: "Valheim", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		Sources: map[string]string{"needs-id": " valheim "},
	}})
	require.NoError(t, err)

	game, err := svc.GetGame("valheim")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"needs-id": "valheim"}, game.SourceIDs)
}

// TestApplyGameDetect_AKnownGamesEntryForAnIdentifierIgnoringSourceIsSaved
// keeps the curated half honest: the refusal is about sources that need the
// value, not about every blank one.
func TestApplyGameDetect_AKnownGamesEntryForAnIdentifierIgnoringSourceIsSaved(t *testing.T) {
	svc := newDetectableGameService(t)

	result, err := svc.ApplyGameDetect(t.Context(), []domain.DetectedGame{{
		Slug: "valheim", Name: "Valheim", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		Sources: map[string]string{"ignores-id": ""},
	}})
	require.NoError(t, err)
	assert.Equal(t, []string{"valheim"}, result.Saved)
}

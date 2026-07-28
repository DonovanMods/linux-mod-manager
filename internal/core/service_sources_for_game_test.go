package core_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSourcesForGameSortedByID proves the intersection of a game's
// SourceIDs with the registry comes back sorted by ID(), not by
// registration order or map-iteration order (which Go randomizes).
func TestSourcesForGameSortedByID(t *testing.T) {
	zeta := &searchStubSource{id: "zeta"}
	alpha := &searchStubSource{id: "alpha"}
	svc, game := newAggregateTestService(t, map[string]string{"zeta": "", "alpha": ""}, zeta, alpha)

	srcs, err := svc.SourcesForGame(game.ID)
	require.NoError(t, err)
	require.Len(t, srcs, 2)
	assert.Equal(t, "alpha", srcs[0].ID())
	assert.Equal(t, "zeta", srcs[1].ID())
}

// TestSourcesForGameUnknownGameErrors proves the only error case is an
// unresolvable game - not a missing/unregistered source.
func TestSourcesForGameUnknownGameErrors(t *testing.T) {
	svc, _ := newAggregateTestService(t, map[string]string{})

	_, err := svc.SourcesForGame("nope")
	assert.Error(t, err)
}

// TestSourcesForGameSkipsUnregisteredKey mirrors SearchAllSources's existing
// tolerance for a SourceIDs key with no matching registration: SourcesForGame
// has no per-item error channel (it returns only resolved ModSource values),
// so an unregistered key is silently absent from the result rather than
// producing an error.
func TestSourcesForGameSkipsUnregisteredKey(t *testing.T) {
	real := &searchStubSource{id: "real"}
	svc, game := newAggregateTestService(t, map[string]string{"real": "", "ghost": ""}, real)

	srcs, err := svc.SourcesForGame(game.ID)
	require.NoError(t, err)
	require.Len(t, srcs, 1)
	assert.Equal(t, "real", srcs[0].ID())
}

// TestSourcesForGameEmptySourceIDsReturnsEmptySlice proves a game configured
// with no sources at all is not an error - it just has nothing to return.
func TestSourcesForGameEmptySourceIDsReturnsEmptySlice(t *testing.T) {
	svc, game := newAggregateTestService(t, map[string]string{})

	srcs, err := svc.SourcesForGame(game.ID)
	require.NoError(t, err)
	assert.Empty(t, srcs)
	// Non-nil matters: a nil slice marshals to JSON null, an empty one to []
	// (the same trap source list --json fixed in v1.18.1); pin it.
	assert.NotNil(t, srcs)
}

// compile-time sanity: the helper's sources implement source.ModSource.
var _ source.ModSource = (*searchStubSource)(nil)

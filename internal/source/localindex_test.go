package source_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staleIndexSource is the contract case LocalIndexSource exists to make
// expressible: a refresh that FAILED over an index that is still usable
// answers with both a Present status and the error, so a caller can serve
// the stale copy and report the failure as a warning rather than as a dead
// source.
type staleIndexSource struct{ err error }

func (s staleIndexSource) IndexStatus(context.Context, string) (source.IndexStatus, error) {
	return source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 3, Stale: true}, nil
}

func (s staleIndexSource) RefreshIndex(context.Context, string, bool, source.IndexProgressFunc) (source.IndexStatus, error) {
	return source.IndexStatus{GameID: "lethal-company", Present: true, Packages: 3, Stale: true}, s.err
}

// TestLocalIndexSourceStaleRefreshContract pins the seam's shape and the one
// rule a caller cannot guess: a non-nil error alongside a Present status is
// a WARNING, not a failure.
func TestLocalIndexSourceStaleRefreshContract(t *testing.T) {
	var src source.LocalIndexSource = staleIndexSource{
		err: fmt.Errorf("fetching the community index: %w", source.ErrIndexUnavailable),
	}

	status, err := src.IndexStatus(t.Context(), "lethal-company")
	require.NoError(t, err)
	assert.True(t, status.Present)
	assert.True(t, status.Stale)
	assert.Equal(t, 3, status.Packages)

	status, err = src.RefreshIndex(t.Context(), "lethal-company", true, func(string, string, int64) {})
	require.Error(t, err)
	assert.True(t, errors.Is(err, source.ErrIndexUnavailable), "a refresh failure classifies as ErrIndexUnavailable")
	assert.True(t, status.Present, "a failed refresh over a usable index still reports it")
}

// TestIndexStatusZeroValueIsAbsent pins that the zero value means "no index
// here", which is what a source with nothing on disk returns.
func TestIndexStatusZeroValueIsAbsent(t *testing.T) {
	var st source.IndexStatus
	assert.False(t, st.Present)
	assert.False(t, st.Stale)
	assert.Zero(t, st.Packages)
	assert.Zero(t, st.Bytes)
	assert.True(t, st.FetchedAt.IsZero())
	assert.Equal(t, time.Time{}, st.FetchedAt)
}

// TestIndexSentinelsAreDistinct pins that the two failures a local-index
// source reports are separable: an index that could not be BUILT is an
// upstream problem, a game whose per-source identifier is missing or
// malformed is the user's configuration.
func TestIndexSentinelsAreDistinct(t *testing.T) {
	assert.False(t, errors.Is(source.ErrIndexUnavailable, source.ErrGameIdentifierInvalid))
	assert.False(t, errors.Is(source.ErrGameIdentifierInvalid, source.ErrIndexUnavailable))

	wrapped := fmt.Errorf("source %q: %w", "thunderstore", source.ErrGameIdentifierInvalid)
	assert.True(t, errors.Is(wrapped, source.ErrGameIdentifierInvalid))
	assert.False(t, errors.Is(wrapped, source.ErrNotSupported))
}

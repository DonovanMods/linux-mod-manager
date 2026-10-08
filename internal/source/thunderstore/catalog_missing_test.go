package thunderstore_test

// #541: a package the community index no longer holds is reported per mod
// as a *source.ModNotFoundError, so core lists it in catalog_missing rather
// than the check reading as "up to date". Only a CURRENT index may say so:
// a stale copy served over a failed refresh, or no index at all, is
// "unknown" - it never marks a package missing.

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckUpdatesReportsAGonePackageAsModNotFound: one package gone from a
// current index among several - the rest are still checked, and the gone
// one is a per-mod ModNotFoundError and nothing else.
func TestCheckUpdatesReportsAGonePackageAsModNotFound(t *testing.T) {
	s := searchable(t)

	updates, err := s.src.CheckUpdates(t.Context(), []domain.InstalledMod{
		installedAt("Nobody-LeftTheIndex", "1.0.0"),
		installedAt("notnotnotswipez-MoreCompany", "1.8.0"),
		installedAt("Somebody-AlsoGone", "2.0.0"),
	})
	require.Len(t, updates, 1, "a gone package must not blind the rest of the batch")
	assert.Equal(t, "notnotnotswipez-MoreCompany", updates[0].InstalledMod.ID)

	missing, rest := source.SplitModNotFound(err)
	require.Len(t, missing, 2)
	assert.Equal(t, "Nobody-LeftTheIndex", missing[0].ModID)
	assert.Equal(t, "Somebody-AlsoGone", missing[1].ModID)
	assert.NoError(t, rest, "the gone packages were all that went wrong")
}

// TestCheckUpdatesOverAStaleIndexMarksNothingMissing: the refresh failed,
// so the copy on disk is past its TTL. It still answers the packages it
// holds, but a package it does not hold may simply be newer than it - that
// is unknown, not gone.
func TestCheckUpdatesOverAStaleIndexMarksNothingMissing(t *testing.T) {
	s := searchable(t)
	s.srv.failWith(http.StatusForbidden)
	s.clock.advance(7 * time.Hour)

	status, err := s.src.IndexStatus(t.Context(), testCommunity)
	require.NoError(t, err)
	require.True(t, status.Stale, "precondition: the index on disk is stale")

	updates, err := s.src.CheckUpdates(t.Context(), []domain.InstalledMod{
		installedAt("Nobody-LeftTheIndex", "1.0.0"),
		installedAt("notnotnotswipez-MoreCompany", "1.8.0"),
	})
	require.Len(t, updates, 1, "the stale index still answers what it holds")
	assert.Equal(t, "notnotnotswipez-MoreCompany", updates[0].InstalledMod.ID)

	var nf *source.ModNotFoundError
	assert.False(t, errors.As(err, &nf), "a stale index says nothing about a package it does not hold: %v", err)
	assert.False(t, errors.Is(err, domain.ErrModNotFound), "%v", err)
}

// TestCheckUpdatesWithNoReadableIndexMarksNothingMissing: no index could be
// built at all - a failed check, never every mod reported gone.
func TestCheckUpdatesWithNoReadableIndexMarksNothingMissing(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	srv.failWith(http.StatusForbidden)
	src, _, _ := newSource(t, srv)

	updates, err := src.CheckUpdates(t.Context(), []domain.InstalledMod{
		installedAt("Nobody-LeftTheIndex", "1.0.0"),
		installedAt("notnotnotswipez-MoreCompany", "1.8.0"),
	})
	require.Error(t, err, "no index is a failed check")
	assert.Empty(t, updates)
	var nf *source.ModNotFoundError
	assert.False(t, errors.As(err, &nf), "an unreadable index says nothing about any package: %v", err)
	assert.False(t, errors.Is(err, domain.ErrModNotFound), "%v", err)
}

package main

// #673: `lmm update` reports a collection profile's collection as the
// update unit - its latest revision against the installed one - in one
// line, from one request.

import (
	"context"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revisionUpdateSource is the update tests' source that also answers a
// collection's latest revision.
type revisionUpdateSource struct {
	*fakeUpdateSource
	latest int
	calls  int
}

func (s *revisionUpdateSource) LatestCollectionRevision(context.Context, string, string) (int, error) {
	s.calls++
	return s.latest, nil
}

// withCollectionRevision makes the skip tests' profile a collection
// ("Night City", installed revision 3, origin given) with "a" locked by it,
// over a source whose latest revision is latest.
func withCollectionRevision(t *testing.T, svc *core.Service, game *domain.Game, origin *domain.CollectionOrigin, latest int) *revisionUpdateSource {
	t.Helper()
	got, err := svc.GetSource("test-src")
	require.NoError(t, err)
	rs := &revisionUpdateSource{fakeUpdateSource: got.(*fakeUpdateSource), latest: latest}
	svc.RegisterSource(rs)
	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	prof.Collection = &domain.CollectionInfo{ID: "night-city", Version: 3, Title: "Night City", Origin: origin}
	prof.FindRef("test-src", "a").Locked = true
	prof.FindRef("test-src", "a").FromCollection = true
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), prof))
	return rs
}

func TestDoUpdate_CollectionRevisionAvailable(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	rs := withCollectionRevision(t, svc, game, &domain.CollectionOrigin{SourceID: "test-src", Slug: "abc123", Revision: 3}, 4)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Equal(t, 1, rs.calls)
	assert.Contains(t, out, "Collection Night City: revision 4 available (installed 3) — 'lmm profile sync default --collection' updates it.")
	assert.Contains(t, out, "1 mod locked by collection Night City (not checked)")
	assert.NotContains(t, out, "Mod A", "the collection's mod is still not checked on its own")
}

func TestDoUpdate_CollectionRevisionLines(t *testing.T) {
	tests := []struct {
		name   string
		origin *domain.CollectionOrigin
		latest int
		want   string
	}{
		{"up to date", &domain.CollectionOrigin{SourceID: "test-src", Slug: "abc123", Revision: 3}, 3,
			"Collection Night City: up to date (revision 3)."},
		{"an address lmm does not fetch", &domain.CollectionOrigin{ImportURL: "http://example.com/night.lmmc"}, 4,
			"Collection Night City: its address isn't an https one, so lmm can't check it for a new version."},
		{"a file", nil, 4,
			"Collection Night City: imported from a file, so lmm can't check for a new version — import the new version with 'lmm profile import <file> --force'."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, game := setupSkipUpdateTest(t)
			withCollectionRevision(t, svc, game, tc.origin, tc.latest)
			out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
			require.NoError(t, err)
			assert.Contains(t, out, tc.want)
		})
	}
}

func TestJSONGolden_UpdateBulkCollectionRevision(t *testing.T) {
	withJSONOutput(t)
	svc, game := setupSkipUpdateTest(t)
	withCollectionRevision(t, svc, game, &domain.CollectionOrigin{SourceID: "test-src", Slug: "abc123", Revision: 3}, 4)
	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	assertJSONCLIGolden(t, "update_bulk_collection_revision", out)
}

// heldRevisionSource refuses the revision request with #671's hold.
type heldRevisionSource struct {
	*fakeUpdateSource
	until time.Time
}

func (s *heldRevisionSource) LatestCollectionRevision(context.Context, string, string) (int, error) {
	return 0, &source.RetryLaterError{Source: "NexusMods", Until: s.until, Reason: "rate limited"}
}

func TestDoUpdate_AHeldSourceSaysUntilWhen(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	withCollectionRevision(t, svc, game, &domain.CollectionOrigin{SourceID: "test-src", Slug: "abc123", Revision: 3}, 0)
	got, err := svc.GetSource("test-src")
	require.NoError(t, err)
	until := time.Date(2026, 11, 22, 14, 3, 0, 0, time.Local)
	svc.RegisterSource(&heldRevisionSource{fakeUpdateSource: got.(*revisionUpdateSource).fakeUpdateSource, until: until})

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "Collection Night City: NexusMods is holding requests until 14:03; revision not checked.")
}

func TestDoUpdate_NoInstalledRevisionIsNotCompared(t *testing.T) {
	svc, game := setupSkipUpdateTest(t)
	withCollectionRevision(t, svc, game, &domain.CollectionOrigin{SourceID: "test-src", Slug: "abc123"}, 4)
	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	prof.Collection.Version = 0
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), prof))

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "Collection Night City: latest revision 4 (the installed revision isn't recorded)")
	assert.NotContains(t, out, "installed 0")
}

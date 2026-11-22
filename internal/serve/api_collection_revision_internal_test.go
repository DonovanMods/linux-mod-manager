package serve

// Issue 673 through GET /api/v1/updates: a collection profile's report
// carries its collection, checked as the update unit, from one request.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revisionUpdatesSource is the updates fixture's source that also answers
// a collection's latest revision.
type revisionUpdatesSource struct {
	*updatesSource
	latest int
	calls  int
}

func (s *revisionUpdatesSource) LatestCollectionRevision(context.Context, string, string) (int, error) {
	s.calls++
	return s.latest, nil
}

func TestAPIUpdates_ReportsTheCollectionsRevision(t *testing.T) {
	src := newUpdatesSource(t)
	s, svc, game := newUpdatesFixtureServerWith(t, src)
	rs := &revisionUpdatesSource{updatesSource: src, latest: 8}
	svc.RegisterSource(rs)
	game.SourceIDs[fixtureSourceID] = "fixturegame"
	require.NoError(t, svc.SaveGame(t.Context(), game))

	prof, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	prof.Collection = &domain.CollectionInfo{ID: "night-city", Version: 7, Title: "Night City",
		Origin: &domain.CollectionOrigin{SourceID: fixtureSourceID, Slug: "abc123", Revision: 7}}
	prof.FindRef(fixtureSourceID, "u1").Locked = true
	prof.FindRef(fixtureSourceID, "u1").FromCollection = true
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), prof))

	report := getUpdatesReport(t, s, game)
	assert.Equal(t, 1, rs.calls)
	assert.NotContains(t, updateIDs(report.Updates), "u1")
	assert.Equal(t, &core.CollectionRevisionStatus{
		Name: "Night City", Origin: core.CollectionOriginSource, SourceID: fixtureSourceID,
		Installed: 7, Latest: 8, Available: true, Checked: true, Sync: true,
	}, report.CollectionRevision)
}

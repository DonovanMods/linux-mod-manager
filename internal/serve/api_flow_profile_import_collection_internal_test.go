package serve

// The Steam Workshop COLLECTION half of the profile-import flow (#269 W2):
// POST /api/v1/plans/profile_import with a workshop_collection reference
// instead of a document, then POST /api/v1/jobs - with the assertions on
// END STATE this flow's rules are actually about: the profile exists and
// lists the collection's items in the author's order, and NOTHING was
// downloaded, cached or written under mod_path, because lmm cannot fetch a
// Workshop item the user is not subscribed to until Tier 3.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectionFixtureSource adds source.CollectionResolver to the workshop
// fake. It records the reference it was handed, so the test can pin that
// the server forwards what the caller typed rather than re-parsing it.
type collectionFixtureSource struct {
	workshopFixtureSource
	collection source.Collection
	collErr    error
	gotRef     string
}

func (s *collectionFixtureSource) ResolveCollection(_ context.Context, ref string) (source.Collection, error) {
	s.gotRef = ref
	if s.collErr != nil {
		return source.Collection{}, s.collErr
	}
	return s.collection, nil
}

// seedCollectionFixture registers a workshop source that resolves one
// collection of two items, neither of them subscribed.
func seedCollectionFixture(t *testing.T, svc *core.Service, game *domain.Game, collErr error) *collectionFixtureSource {
	t.Helper()
	src := &collectionFixtureSource{
		collection: source.Collection{
			ID: "2500900001", Name: "Cargo Ships",
			URL:     "https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001",
			ItemIDs: []string{"3617086610", "3512001122"},
		},
		collErr: collErr,
	}
	src.describe = []source.ModDescription{
		{ModID: "3617086610", Mod: domain.Mod{ID: "3617086610", SourceID: workshopFixtureSourceID, Name: "Sample Workshop Item"}},
		{ModID: "3512001122", Mod: domain.Mod{ID: "3512001122", SourceID: workshopFixtureSourceID, Name: "Second Workshop Item"}},
	}
	svc.RegisterSource(src)
	game.SourceIDs[workshopFixtureSourceID] = "1133870"
	require.NoError(t, svc.SaveGame(t.Context(), game))
	return src
}

func TestAPIFlow_ProfileImportCollection_PlansTheCollectionAndImportsNothing(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	src := seedCollectionFixture(t, svc, game, nil)

	_, raw := planFlow(t, s, game, "profile_import",
		`{"workshop_collection":"https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001"}`)

	assert.Equal(t, "https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001", src.gotRef,
		"the reference is forwarded verbatim: the SOURCE owns what it recognises")

	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.NotNil(t, resp.Plan.Profile)
	assert.Equal(t, "cargo-ships", resp.Plan.Profile.Name)

	c := resp.Plan.WorkshopCollection
	require.NotNil(t, c, "the plan document carries the collection")
	assert.Equal(t, "2500900001", c.CollectionID)
	assert.Equal(t, "Cargo Ships", c.Name)
	assert.Equal(t, 0, c.Tracked)
	assert.Equal(t, 2, c.NotSubscribed)
	require.Len(t, c.Items, 2)
	assert.Equal(t, "Sample Workshop Item", c.Items[0].Name)
	assert.Contains(t, c.Items[0].Note, "subscribe in Steam")

	_, err := svc.NewProfileManager().Get(t.Context(), game.ID, "cargo-ships")
	require.ErrorIs(t, err, domain.ErrProfileNotFound, "a plan must import nothing")
}

func TestAPIFlow_ProfileImportCollection_SavesTheProfileAndDownloadsNothing(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedCollectionFixture(t, svc, game, nil)
	modPathBefore := dirEntryNames(t, game.ModPath)

	planID, _ := planFlow(t, s, game, "profile_import", `{"workshop_collection":"2500900001"}`)

	// install:true is deliberately requested: core forces NoInstall for a
	// collection, and the point of this assertion is that the frontend
	// cannot override that rule from the wire.
	j := startFlowJob(t, s, planID, `{"install":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	result, ok := j.status().Result.(*core.ProfileImportResult)
	require.True(t, ok)
	assert.Equal(t, "cargo-ships", result.ProfileName)
	assert.Zero(t, result.Installed, "Tier 3 owns the download path")
	assert.Equal(t, 2, result.Skipped)
	assert.NotEmpty(t, result.Notes)

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "cargo-ships")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 2)
	assert.Equal(t, "3617086610", profile.Mods[0].ModID, "the author's own order is the load order")
	assert.Equal(t, "3512001122", profile.Mods[1].ModID)

	installed, err := svc.GetInstalledMods(t.Context(), game.ID, "cargo-ships")
	require.NoError(t, err)
	assert.Empty(t, installed, "nothing was fetched, so nothing is installed")
	assert.Equal(t, modPathBefore, dirEntryNames(t, game.ModPath),
		"nothing may be written under the game's mod directory")
}

func TestAPIFlow_ProfileImportCollection_ProfileNameOverridesTheCollectionTitle(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedCollectionFixture(t, svc, game, nil)

	planID, _ := planFlow(t, s, game, "profile_import",
		`{"workshop_collection":"2500900001","profile_name":"my-ships"}`)
	j := startFlowJob(t, s, planID, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	_, err := svc.NewProfileManager().Get(t.Context(), game.ID, "my-ships")
	require.NoError(t, err)
}

// TestAPIFlow_ProfileImportCollection_BadRequests pins that every way the
// caller's input can be wrong answers 400 - a reference the source refuses,
// a game with no Workshop mapping, both inputs at once, and neither.
func TestAPIFlow_ProfileImportCollection_BadRequests(t *testing.T) {
	tests := map[string]struct {
		body   string
		seed   bool
		refErr error
	}{
		"a reference the source does not recognise": {
			body: `{"workshop_collection":"nonsense"}`, seed: true,
			refErr: source.ErrInvalidReference,
		},
		"a game with no Steam Workshop mapping": {
			body: `{"workshop_collection":"2500900001"}`, seed: false,
		},
		"both inputs at once": {
			body: `{"data":"name: x\ngame_id: g1\n","workshop_collection":"2500900001"}`, seed: true,
		},
		"neither input": {
			body: `{}`, seed: true,
		},
		"a profile name on a document import": {
			body: `{"data":"name: x\ngame_id: g1\n","profile_name":"x"}`, seed: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s, svc, game := newFlowFixtureServer(t)
			if tt.seed {
				seedCollectionFixture(t, svc, game, tt.refErr)
			}
			rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/profile_import", game), tt.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

// TestAPISearch_AnUnkeyedSourceIs401 is the non-browser half of the same
// rule: when the ONLY source needs a credential the user has not supplied,
// core returns the failure unwrapped and this endpoint must say "get a key"
// rather than "something broke". Before #346 it answered 500, which is the
// ordinary state of a Workshop-only game whose owner has not run `lmm auth
// login steamworkshop`.
func TestAPISearch_AnUnkeyedSourceIs401(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	svc.RegisterSource(&unkeyedSearchSource{workshopFixtureSource: workshopFixtureSource{}})
	game.SourceIDs = map[string]string{"needs-key": "1133870"}
	require.NoError(t, svc.SaveGame(t.Context(), game))

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/search?q=cargo&source=needs-key", game), "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "authentication required")
}

// unkeyedSearchSource is a source whose Search always reports that a
// credential is required - Valve's answer to a keyless QueryFiles, mapped.
type unkeyedSearchSource struct {
	workshopFixtureSource
}

func (s *unkeyedSearchSource) ID() string   { return "needs-key" }
func (s *unkeyedSearchSource) Name() string { return "Needs Key" }

func (s *unkeyedSearchSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, domain.ErrAuthRequired
}

func (s *unkeyedSearchSource) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Auth: true}
}

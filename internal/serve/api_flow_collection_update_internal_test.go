package serve

// #613: a collection profile updated from its origin over /api/v1 - the
// profiles modal's "Update collection…" posts the collection_update plan
// kind with the profile, and the job with the CLI's choices as options.
// The entries are switched off in both versions, so nothing downloads.

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const updateDocV1 = `lmm_collection: 1
id: coll-1
version: 1
name: shared
game_id: g1
mods:
  - {source_id: nexusmods, mod_id: "1", version: "1.0", disabled: true}
  - {source_id: nexusmods, mod_id: "2", version: "1.0", disabled: true}
  - {source_id: nexusmods, mod_id: "4", version: "1.0", disabled: true}
`

const updateDocV2 = `lmm_collection: 1
id: coll-1
version: 2
name: shared
game_id: g1
mods:
  - {source_id: nexusmods, mod_id: "1", version: "1.1", disabled: true}
  - {source_id: nexusmods, mod_id: "3", version: "1.0", disabled: true}
  - {source_id: nexusmods, mod_id: "4", version: "2.0", disabled: true}
`

// collectionUpdateServer is a Server with profile "shared" imported from an
// address serving updateDocV1, and a setter for what it serves next.
func collectionUpdateServer(t *testing.T) (*Server, *core.Service, *domain.Game, func(string)) {
	t.Helper()
	var body atomic.Pointer[string]
	v1 := updateDocV1
	body.Store(&v1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(*body.Load()))
	}))
	t.Cleanup(srv.Close)
	s, svc, game := newProfilesFixtureServerWith(t, srv.Client())
	j := runFlow(t, s, game, "profile_import", urlPlanBody(t, srv.URL+"/shared.yaml", "shared"), "")
	require.Equal(t, jobSucceeded, j.status().State, "import failed: %+v", j.status().Error)
	return s, svc, game, func(doc string) { body.Store(&doc) }
}

func TestFlowCollectionUpdate_PlanAndApplyWithChoices(t *testing.T) {
	s, svc, game, serve := collectionUpdateServer(t)

	// The profiles list names where the profile updates from.
	listing, err := svc.ListProfiles(t.Context(), game.ID)
	require.NoError(t, err)
	var origin core.CollectionInput
	for _, p := range listing.Profiles {
		if p.Name == "shared" {
			origin = p.CollectionOrigin
		}
	}
	assert.Equal(t, core.CollectionFromURL, origin)

	// The user locks mod 4 (by hand: it is not installed), so the new
	// version's change to it conflicts.
	p, err := config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
	require.NoError(t, err)
	p.FindRef("nexusmods", "4").Locked = true
	p.FindRef("nexusmods", "4").FromCollection = false
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))
	serve(updateDocV2)

	_, raw := planFlow(t, s, game, "collection_update", `{"profile":"shared"}`)
	var resp struct {
		Plan core.CollectionUpdatePlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	plan := resp.Plan
	assert.Equal(t, 2, plan.NewVersion)
	require.Len(t, plan.Added, 1)
	assert.Equal(t, "nexusmods:3", plan.Added[0].Key())
	require.Len(t, plan.Removed, 1)
	assert.Equal(t, "nexusmods:2", plan.Removed[0].Key())
	require.Len(t, plan.Conflicts, 1)
	assert.Equal(t, "nexusmods:4", plan.Conflicts[0].Key())

	j := runFlow(t, s, game, "collection_update", `{"profile":"shared"}`,
		`{"remove":["nexusmods:2"],"override":["nexusmods:4"]}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.CollectionUpdateResult)
	require.True(t, ok)
	assert.Equal(t, []string{"nexusmods:2"}, result.Removed)
	assert.Equal(t, 1, result.Overridden)

	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
	require.NoError(t, err)
	assert.Equal(t, 2, saved.Collection.Version)
	assert.Nil(t, saved.FindRef("nexusmods", "2"), "removed as asked")
	assert.Equal(t, "2.0", saved.FindRef("nexusmods", "4").Version, "taken the collection's way")
	assert.True(t, saved.FindRef("nexusmods", "4").FromCollection)
	assert.NotNil(t, saved.FindRef("nexusmods", "3"))
}

func TestFlowCollectionUpdate_UpToDateAndRefusals(t *testing.T) {
	s, svc, game, serve := collectionUpdateServer(t)

	_, raw := planFlow(t, s, game, "collection_update", `{"profile":"shared"}`)
	var resp struct {
		Plan core.CollectionUpdatePlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	assert.True(t, resp.Plan.UpToDate)

	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{Name: "plain", GameID: game.ID}))
	serve("lmm_collection: 1\nid: other\nversion: 3\nname: shared\ngame_id: g1\nmods: []\n")
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"no profile":         {`{}`, http.StatusBadRequest},
		"no origin":          {`{"profile":"plain"}`, http.StatusConflict},
		"another collection": {`{"profile":"shared"}`, http.StatusConflict},
		"unknown option":     {`{"profile":"shared","remove":["x"]}`, http.StatusBadRequest},
	} {
		rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/collection_update", game), tc.body)
		assert.Equal(t, tc.want, rec.Code, name+": "+rec.Body.String())
	}

	// A choice naming an entry the plan does not offer fails the job.
	serve(updateDocV2)
	j := runFlow(t, s, game, "collection_update", `{"profile":"shared"}`, `{"override":["nexusmods:1"]}`)
	assert.Equal(t, jobFailed, j.status().State)
}

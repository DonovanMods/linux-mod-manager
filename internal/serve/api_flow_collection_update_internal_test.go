package serve

// #613: a collection profile updated from its origin over /api/v1 - the
// profiles modal's "Update collection…" posts the collection_update plan
// kind with the profile, and the job with the CLI's choices as options.
// The entries are switched off in both versions, so nothing downloads.

import (
	"encoding/json/v2"
	"log/slog"
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

// TestFlowCollectionUpdate_AnInstallersQuestionRoundTrips: version 2 adds
// a FOMOD mod whose installer the document records no answers for. The job
// fails with the question, nothing written; the re-posted plan and job
// with the answers in "fomod" land the update.
func TestFlowCollectionUpdate_AnInstallersQuestionRoundTrips(t *testing.T) {
	sandboxEnv(t)
	var body atomic.Pointer[string]
	v1 := "lmm_collection: 1\nid: coll-1\nversion: 1\nname: shared\ngame_id: g1\nmods: []\n"
	body.Store(&v1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(*body.Load()))
	}))
	t.Cleanup(srv.Close)
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler), DownloadClient: srv.Client()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	src := newInstallSource(t)
	svc.RegisterSource(src)
	addFomodMod(t, src)
	game := &domain.Game{ID: "g1", Name: "Fixture Game", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{fixtureSourceID: ""}}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err = svc.NewProfileManager().Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))
	s := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr})

	j := runFlow(t, s, game, "profile_import", urlPlanBody(t, srv.URL+"/c.yaml", "shared"), "")
	require.Equal(t, jobSucceeded, j.status().State, "import failed: %+v", j.status().Error)
	v2 := "lmm_collection: 1\nid: coll-1\nversion: 2\nname: shared\ngame_id: g1\nmods:\n  - {source_id: " + fixtureSourceID + ", mod_id: " + fomodModID + ", version: \"1.0\", file_ids: [w1]}\n"
	body.Store(&v2)

	j = runFlow(t, s, game, "collection_update", `{"profile":"shared"}`, "")
	st := j.status()
	require.Equal(t, jobFailed, st.State)
	require.NotNil(t, st.Error)
	raw, err := json.Marshal(st.Error.Details)
	require.NoError(t, err)
	// The import result's question, as a profile import's job reports it
	// (the web UI's fomodQuestionFor reads it there).
	var details struct {
		Question core.FomodChoicesRequiredError `json:"fomod_choices_required"`
	}
	require.NoError(t, json.Unmarshal(raw, &details))
	require.NotEmpty(t, details.Question.Mods, "details: %s", raw)
	assert.Equal(t, fomodModID, details.Question.Mods[0].ModID)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
	require.NoError(t, err)
	assert.Equal(t, 1, saved.Collection.Version, "nothing written")

	j = runFlow(t, s, game, "collection_update", `{"profile":"shared"}`, fomodAnswer(t, sizeStep("2K", 0)))
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	saved, err = config.LoadProfile(svc.ConfigDir(), game.ID, "shared")
	require.NoError(t, err)
	assert.Equal(t, 2, saved.Collection.Version)
	ref := saved.FindRef(fixtureSourceID, fomodModID)
	require.NotNil(t, ref)
	assert.Equal(t, "2K", ref.Fomod.Steps[0].Groups[0].Choices[0].Name)
}

// TestFlowCollectionUpdate_OptionalSelectionDecodes: the plan view's
// toggles post the whole selection; an empty one is a selection too.
func TestFlowCollectionUpdate_OptionalSelectionDecodes(t *testing.T) {
	opts, err := decodeKindOptions[collectionUpdatePlanRequest]([]byte(`{"profile":"p","optional":[]}`))
	require.NoError(t, err)
	assert.NotNil(t, opts.(collectionUpdatePlanRequest).Optional, "present and empty is a choice")
	opts, err = decodeKindOptions[collectionUpdatePlanRequest]([]byte(`{"profile":"p"}`))
	require.NoError(t, err)
	assert.Nil(t, opts.(collectionUpdatePlanRequest).Optional, "absent is no choice")
}

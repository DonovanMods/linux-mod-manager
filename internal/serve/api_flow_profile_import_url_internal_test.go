package serve

// An lmm collection imported from an https address over /api/v1 (#622):
// the profiles modal's URL field posts {"url": …} through the ordinary
// profile_import plan kind. Core fetches it; its hooks are never applied,
// even with with_hooks; the address is recorded as the profile's origin.

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedCollectionDoc is a version-1 document with a hook, for game g1.
const sharedCollectionDoc = `lmm_collection: 1
name: shared
author: friend
game_id: g1
hooks:
  install:
    after_all: /usr/bin/true
mods: []
`

// collectionURLServer serves body over TLS, and a Server whose downloads
// trust it.
func collectionURLServer(t *testing.T, body string) (*httptest.Server, *Server, *core.Service, *domain.Game) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	s, svc, game := newProfilesFixtureServerWith(t, srv.Client())
	return srv, s, svc, game
}

func urlPlanBody(t *testing.T, link, profileName string) string {
	t.Helper()
	encoded, err := json.Marshal(struct {
		URL         string `json:"url"`
		ProfileName string `json:"profile_name,omitzero"`
	}{link, profileName})
	require.NoError(t, err)
	return string(encoded)
}

func TestFlowCollectionURL_ImportsWithoutHooksAndRecordsTheAddress(t *testing.T) {
	srv, s, svc, game := collectionURLServer(t, sharedCollectionDoc)
	link := srv.URL + "/shared.yaml?token=s3cret"

	_, raw := planFlow(t, s, game, "profile_import", urlPlanBody(t, link, ""))
	assert.NotContains(t, string(raw), "s3cret", "the plan never shows the query")
	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	c := resp.Plan.Collection
	require.NotNil(t, c)
	assert.Equal(t, core.CollectionFromURL, c.Input)
	assert.Equal(t, game.ID, c.GameID, "imported into the selected game")
	assert.False(t, c.HooksImportable)
	require.Len(t, c.Hooks, 1)
	assert.Equal(t, srv.URL+"/shared.yaml?…", c.Origin.ImportURL)

	// with_hooks is ignored for a URL: the job succeeds and withholds them.
	j := runFlow(t, s, game, "profile_import", urlPlanBody(t, link, "mine"), `{"with_hooks":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.ProfileImportResult)
	require.True(t, ok)
	assert.Equal(t, 1, result.HooksWithheld)

	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "mine")
	require.NoError(t, err)
	assert.False(t, saved.HooksExplicit.Install.AfterAll, "a URL's hooks are never saved")
	assert.Equal(t, link, saved.Collection.Origin.ImportURL)
}

func TestFlowCollectionURL_Refusals(t *testing.T) {
	srv, s, _, game := collectionURLServer(t, "<!doctype html><p>a page</p>")
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"plain http":            {urlPlanBody(t, strings.Replace(srv.URL, "https://", "http://", 1)+"/c.lmmc", ""), http.StatusBadRequest},
		"not a collection":      {urlPlanBody(t, srv.URL+"/page", ""), http.StatusBadRequest},
		"url and data together": {`{"url":"https://example.invalid/c","data":"name: x"}`, http.StatusBadRequest},
		"url and a collection":  {`{"url":"https://example.invalid/c","workshop_collection":"1"}`, http.StatusBadRequest},
		"unreachable":           {urlPlanBody(t, "https://127.0.0.1:1/c.lmmc?token=s3cret", ""), http.StatusBadGateway},
	} {
		rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/profile_import", game), tc.body)
		assert.Equal(t, tc.want, rec.Code, name+": "+rec.Body.String())
		assert.NotContains(t, rec.Body.String(), "s3cret", name)
	}
}

// TestFlowCollectionURL_JSONOutputsRedactTheAddress is #622 review S3:
// the profile file keeps a URL import's whole address, and every JSON
// answer - a ProfileResult route, the collection identity route - shows
// it with its query redacted.
func TestFlowCollectionURL_JSONOutputsRedactTheAddress(t *testing.T) {
	srv, s, svc, game := collectionURLServer(t, sharedCollectionDoc)
	link := srv.URL + "/shared.yaml?token=s3cret"
	j := runFlow(t, s, game, "profile_import", urlPlanBody(t, link, "mine"), "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/profiles/mine/rename", game), `{"name":"ours"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "s3cret")
	var result core.ProfileResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, srv.URL+"/shared.yaml?…", result.Profile.Collection.Origin.ImportURL)

	rec = doAPI(s, http.MethodPost, scoped("/api/v1/profiles/ours/collection", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "s3cret")
	var info domain.CollectionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Equal(t, srv.URL+"/shared.yaml?…", info.Origin.ImportURL)
	assert.NotEmpty(t, info.ID, "the identity was recorded")

	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "ours")
	require.NoError(t, err)
	assert.Equal(t, link, saved.Collection.Origin.ImportURL, "only the profile file keeps it whole")
	assert.Equal(t, info.ID, saved.Collection.ID)
}

package serve

// The lmm collection over /api/v1 (#621): the profiles modal's Export
// downloads a .lmmc (GET .../export?format=lmmc), and its Import posts one
// back (base64, since it is binary) through the ordinary profile_import
// plan kind - which plans it as a collection and applies its hooks only
// with with_hooks.

import (
	"encoding/base64"
	"encoding/json/v2"
	"net/http"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exportLMMC downloads the active profile as a .lmmc, after giving it a
// hook so the import has one to withhold or apply.
func exportLMMC(t *testing.T, s *Server, svc *core.Service, game *domain.Game) []byte {
	t.Helper()
	profile, err := config.LoadProfile(svc.ConfigDir(), game.ID, activeProfile)
	require.NoError(t, err)
	profile.Hooks.Install.AfterAll = "/usr/bin/true"
	profile.HooksExplicit.Install.AfterAll = true
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/profiles/"+activeProfile+"/collection", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = doAPI(s, http.MethodGet, scoped("/api/v1/profiles/"+activeProfile+"/export?format=lmmc", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, `attachment; filename="`+activeProfile+`.lmmc"`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "application/zip", rec.Header().Get("Content-Type"))
	require.True(t, core.IsCollectionContainer(rec.Body.Bytes()))

	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, activeProfile)
	require.NoError(t, err)
	require.NotEmpty(t, saved.Collection.ID, "the first export records the collection id")
	return rec.Body.Bytes()
}

func lmmcPlanBody(t *testing.T, data []byte, profileName string) string {
	t.Helper()
	encoded, err := json.Marshal(struct {
		DataBase64  string `json:"data_base64"`
		ProfileName string `json:"profile_name,omitzero"`
	}{base64.StdEncoding.EncodeToString(data), profileName})
	require.NoError(t, err)
	return string(encoded)
}

func TestFlowLMMCollection_ExportThenImportWithoutHooks(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	data := exportLMMC(t, s, svc, game)

	_, raw := planFlow(t, s, game, "profile_import", lmmcPlanBody(t, data, "copy"))
	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.NotNil(t, resp.Plan.Collection, "a .lmmc plans as a collection")
	assert.Equal(t, "copy", resp.Plan.Collection.ProfileName)
	assert.Equal(t, game.ID, resp.Plan.Collection.GameID)
	assert.True(t, resp.Plan.Collection.HooksImportable)
	require.Len(t, resp.Plan.Collection.Hooks, 1)

	j := runFlow(t, s, game, "profile_import", lmmcPlanBody(t, data, "copy"), "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.ProfileImportResult)
	require.True(t, ok)
	assert.Equal(t, 1, result.HooksWithheld)

	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "copy")
	require.NoError(t, err)
	assert.False(t, saved.HooksExplicit.Install.AfterAll)
}

func TestFlowLMMCollection_WithHooks(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	data := exportLMMC(t, s, svc, game)

	j := runFlow(t, s, game, "profile_import", lmmcPlanBody(t, data, "copy"), `{"with_hooks":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "copy")
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/true", saved.Hooks.Install.AfterAll)
}

func TestFlowLMMCollection_BadRequests(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)
	for name, body := range map[string]string{
		"not base64":         `{"data_base64":"***"}`,
		"both inputs":        `{"data":"name: x","data_base64":"eA=="}`,
		"a newer format":     `{"data":"lmm_collection: 9\nname: x\n"}`,
		"a broken container": lmmcPlanBody(t, []byte("PK\x03\x04garbage"), ""),
	} {
		rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/profile_import", game), body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name+": "+rec.Body.String())
	}
	rec := doAPI(s, http.MethodGet, scoped("/api/v1/profiles/"+activeProfile+"/export?format=zip", game), "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestFlowLMMCollection_TheExportGETNeverWrites is #621 review S8: the
// GET is not CSRF-guarded, so it must not record anything. A profile with
// no collection identity yet answers 409 naming the POST; the POST
// (CSRF-guarded) records it; the GET then serves it, still writing nothing.
func TestFlowLMMCollection_TheExportGETNeverWrites(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	path, err := config.ProfilePath(svc.ConfigDir(), game.ID, activeProfile)
	require.NoError(t, err)
	read := func() []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return data
	}
	exportURL := scoped("/api/v1/profiles/"+activeProfile+"/export?format=lmmc", game)

	before := read()
	rec := doAPI(s, http.MethodGet, exportURL, "")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "/collection")
	require.Equal(t, before, read(), "the refused GET wrote nothing")

	collectionURL := scoped("/api/v1/profiles/"+activeProfile+"/collection", game)
	rec = doAPIWithoutCSRF(s, http.MethodPost, collectionURL, "")
	require.NotEqual(t, http.StatusOK, rec.Code, "the write is CSRF-guarded")
	require.Equal(t, before, read())

	rec = doAPI(s, http.MethodPost, collectionURL, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var info domain.CollectionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	require.NotEmpty(t, info.ID)

	recorded := read()
	rec = doAPI(s, http.MethodGet, exportURL, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, recorded, read(), "a served GET writes nothing either")
}

// gatedImportDocument is a version-0 export with a hook and a .dll
// override, the two things #621's import gates hold back.
const gatedImportDocument = `name: gated
game_id: g1
mods: []
hooks:
  install:
    after_all: /usr/bin/true
overrides:
  Config/Game.ini: "[A]\n"
  bin/version.dll: "MZ"
`

// TestFlowProfileImport_GatesHoldBackHooksAndNonConfigOverrides is the
// owner's S5/S6 ruling over /api/v1, for a version-0 document: the plan
// lists both, the default apply saves neither, and the apply options
// include them.
func TestFlowProfileImport_GatesHoldBackHooksAndNonConfigOverrides(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	_, raw := planFlow(t, s, game, "profile_import", importPlanBody(t, gatedImportDocument))
	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.Len(t, resp.Plan.Hooks, 1)
	require.Len(t, resp.Plan.RestrictedOverrides, 1)
	assert.Equal(t, "bin/version.dll", resp.Plan.RestrictedOverrides[0].Path)

	j := runFlow(t, s, game, "profile_import", importPlanBody(t, gatedImportDocument), "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result := j.status().Result.(*core.ProfileImportResult)
	assert.Equal(t, 1, result.HooksWithheld)
	require.Len(t, result.OverridesSkipped, 1)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "gated")
	require.NoError(t, err)
	assert.False(t, saved.HooksExplicit.Install.AfterAll)
	assert.NotContains(t, saved.Overrides, "bin/version.dll")
	assert.Contains(t, saved.Overrides, "Config/Game.ini")

	j = runFlow(t, s, game, "profile_import", importPlanBody(t, gatedImportDocument), `{"with_hooks":true,"allow_any_overrides":true,"force":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	saved, err = config.LoadProfile(svc.ConfigDir(), game.ID, "gated")
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/true", saved.Hooks.Install.AfterAll)
	assert.Contains(t, saved.Overrides, "bin/version.dll")
}

func TestFlowProfileImport_AnEscapingOverrideIsABadRequest(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)
	doc := "name: gated\ngame_id: g1\nmods: []\noverrides:\n  ../../../.bashrc: x\n"
	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/profile_import", game), importPlanBody(t, doc))
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

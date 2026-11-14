package serve

// #623 over /api/v1: a .lmmc carrying its author's own files imports
// through the ordinary profile_import kind - the plan lists each bundled
// item, an item holding code is held back unless allow_any_overrides - and
// the export bundles a mod imported from a file (bundle=…), refusing one
// from a source, with GET .../bundle-candidates listing what it accepts.

import (
	"archive/zip"
	"bytes"
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bundledLMMC is a .lmmc naming two bundled items: patch (a text file) and
// tool (a DLL, held back by default).
func bundledLMMC(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, m := range []struct{ name, content string }{
		{"collection.yaml", "lmm_collection: 1\nid: c0ffee\nname: bundled\ngame: {name: G}\nrequires: [bundled]\nmods:\n" +
			"  - {source_id: lmm-collection, mod_id: patch, name: My Patch}\n" +
			"  - {source_id: lmm-collection, mod_id: tool, name: My Tool}\n"},
		{"files/patch/patch.esp", "PATCH"},
		{"files/tool/helper.dll", "MZ\x90\x00"},
	} {
		hdr := &zip.FileHeader{Name: m.name, Method: zip.Deflate}
		hdr.SetMode(0o644)
		f, err := w.CreateHeader(hdr)
		require.NoError(t, err)
		_, err = f.Write([]byte(m.content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestFlowBundledCollection_PlanListsTheBundledItems(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)
	_, raw := planFlow(t, s, game, "profile_import", lmmcPlanBody(t, bundledLMMC(t), ""))
	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	c := resp.Plan.Collection
	require.NotNil(t, c)
	require.Empty(t, c.Unsupported)
	require.Len(t, c.Bundled, 2)
	assert.Equal(t, "c0ffee.patch", c.Bundled[0].ModID)
	assert.Empty(t, c.Bundled[0].Restricted)
	assert.Equal(t, "My Tool", c.Bundled[1].Name)
	require.Len(t, c.Bundled[1].Restricted, 1)
	assert.Equal(t, "helper.dll", c.Bundled[1].Restricted[0].Path)
	assert.True(t, c.Items[0].Bundled)
}

func TestFlowBundledCollection_CodeIsHeldBackUnlessAllowed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		apply   string
		skipped []string
		mods    int
	}{
		{"held back", `{"install":true}`, []string{"tool"}, 1},
		{"allowed", `{"install":true,"allow_any_overrides":true}`, nil, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, svc, game := newProfilesFixtureServer(t)
			j := runFlow(t, s, game, "profile_import", lmmcPlanBody(t, bundledLMMC(t), ""), tc.apply)
			require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
			result, ok := j.status().Result.(*core.ProfileImportResult)
			require.True(t, ok)
			assert.Equal(t, tc.skipped, result.BundledSkipped)

			saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "bundled")
			require.NoError(t, err)
			require.Len(t, saved.Mods, tc.mods)
			assert.Equal(t, domain.SourceCollection, saved.Mods[0].SourceID)
			assert.Equal(t, "c0ffee.patch", saved.Mods[0].ModID)
			row, err := svc.GetInstalledMod(t.Context(), domain.SourceCollection, "c0ffee.patch", game.ID, "bundled")
			require.NoError(t, err, "the bundled item has its row")
			assert.True(t, svc.GetGameCache(game).Exists(cache.Key{GameID: game.ID, SourceID: row.SourceID, ModID: row.ID, Version: row.Version}),
				"and its cache entry, built from the container")
		})
	}
}

// seedLocalMod gives the active profile a mod imported from a file.
func seedLocalMod(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	key := cache.Key{GameID: game.ID, SourceID: domain.SourceLocal, ModID: "u-1", Version: "1.0"}
	require.NoError(t, svc.GetGameCache(game).Store(key, "tweaks/a.ini", []byte("[T]\n")))
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "u-1", SourceID: domain.SourceLocal, Name: "My Tweaks", Version: "1.0", GameID: game.ID},
		ProfileName: activeProfile, Enabled: true,
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, activeProfile,
		domain.ModReference{SourceID: domain.SourceLocal, ModID: "u-1", Version: "1.0"}))
}

func TestFlowBundledCollection_ExportBundlesTheAuthorsOwnFiles(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	seedLocalMod(t, svc, game)

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/profiles/"+activeProfile+"/bundle-candidates", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var candidates []core.BundleCandidate
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &candidates))
	assert.Equal(t, []core.BundleCandidate{{SourceID: domain.SourceLocal, ModID: "u-1", Name: "My Tweaks"}}, candidates,
		"only the mod imported from a file")

	rec = doAPI(s, http.MethodPost, scoped("/api/v1/profiles/"+activeProfile+"/collection", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = doAPI(s, http.MethodGet, scoped("/api/v1/profiles/"+activeProfile+"/export?format=lmmc&bundle=local:u-1", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	require.NoError(t, err)
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	assert.Equal(t, []string{"collection.yaml", "files/my-tweaks/tweaks/a.ini"}, names)

	// The same bundle, as an import plan, round-trips to its digest.
	_, raw := planFlow(t, s, game, "profile_import", lmmcPlanBody(t, rec.Body.Bytes(), "copy"))
	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.Len(t, resp.Plan.Collection.Bundled, 1)
	assert.Equal(t, "My Tweaks", resp.Plan.Collection.Bundled[0].Name)

	// A source's mod, and a name matching nothing, are the caller's 400.
	for _, bundle := range []string{fixtureSourceID + ":p1", "nothing-here"} {
		rec = doAPI(s, http.MethodGet, scoped("/api/v1/profiles/"+activeProfile+"/export?format=lmmc&bundle="+bundle, game), "")
		assert.Equal(t, http.StatusBadRequest, rec.Code, bundle+": "+rec.Body.String())
	}
}

// TestFlowBundledCollection_TheRecordChecksTheBundle is the web export's
// cheap check (#623 review): the POST that records the identity also
// checks the bundle choice, without building the container.
func TestFlowBundledCollection_TheRecordChecksTheBundle(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	seedLocalMod(t, svc, game)
	url := scoped("/api/v1/profiles/"+activeProfile+"/collection", game)

	rec := doAPI(s, http.MethodPost, url, `{"bundle":["local:u-1"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = doAPI(s, http.MethodPost, url, `{"bundle":["`+fixtureSourceID+`:p1"]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "never bundles")
	rec = doAPI(s, http.MethodPost, url, `{"bundle":["nothing-here"]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	rec = doAPI(s, http.MethodPost, url, `{"bundles":[]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "an unknown field is refused")
}

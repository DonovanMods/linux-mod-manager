package serve_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// categoryFixture seeds two mods into the default profile - "1" with no
// category at all and "2" whose source names its category "Weapons" - and
// returns a server over them plus the profile file the user category lands
// in (#592).
func categoryFixture(t *testing.T) (*serve.Server, *core.Service, string) {
	t.Helper()
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	pm := svc.NewProfileManager()
	for _, mod := range []domain.Mod{
		{ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID},
		{ID: "2", SourceID: "fake", Name: "Big Sword", Version: "1.0", GameID: game.ID, CategoryName: "Weapons"},
	} {
		seedInstalledMod(t, svc, game, mod, true, nil)
		require.NoError(t, pm.AddMod(t.Context(), game.ID, "default",
			domain.ModReference{SourceID: "fake", ModID: mod.ID, Version: "1.0"}))
	}
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	profilePath := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles", "default.yaml")
	return srv, svc, profilePath
}

func readProfile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(raw)
}

// TestServer_APIModCategory_SetsAndClears proves POST .../category writes
// the user category to the profile (the end state a reload reads) and
// answers with the ModSettingResult that names the effective category and
// where it came from; an empty category clears it again, falling back to
// the source's name.
func TestServer_APIModCategory_SetsAndClears(t *testing.T) {
	srv, _, profilePath := categoryFixture(t)

	rec := postAPI(t, srv, "/api/v1/mods/fake/2/category", `{"category":"  Combat  "}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var result core.ModSettingResult
	decodeStrict(t, rec.Body.Bytes(), &result)
	assert.Equal(t, "Combat", result.EffectiveCategory, "the name is trimmed")
	assert.Equal(t, core.CategorySourceUser, result.CategorySource)
	assert.Contains(t, readProfile(t, profilePath), "category: Combat")

	rec = postAPI(t, srv, "/api/v1/mods/fake/2/category", `{"category":""}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	result = core.ModSettingResult{}
	decodeStrict(t, rec.Body.Bytes(), &result)
	assert.Equal(t, "Weapons", result.EffectiveCategory, "a clear falls back to the source's category")
	assert.Equal(t, core.CategorySourceSource, result.CategorySource)
	assert.NotContains(t, readProfile(t, profilePath), "category:")
}

// TestServer_APIModCategory_Invalid_Renders400 proves a category core
// refuses (ErrInvalidCategory) is the caller's bad input, not a server
// failure, and writes nothing.
func TestServer_APIModCategory_Invalid_Renders400(t *testing.T) {
	srv, _, profilePath := categoryFixture(t)
	before := readProfile(t, profilePath)

	rec := postAPI(t, srv, "/api/v1/mods/fake/1/category", `{"category":"`+strings.Repeat("x", core.MaxCategoryLength+1)+`"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, "invalid category")
	assert.Equal(t, before, readProfile(t, profilePath))

	rec = postAPI(t, srv, "/api/v1/mods/fake/1/category", `{"name":"x"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "an unknown member is refused: %s", rec.Body.String())
}

// TestServer_APIModCategory_NotFound_Renders404 mirrors the lock route's
// not-found treatment.
func TestServer_APIModCategory_NotFound_Renders404(t *testing.T) {
	srv, _, _ := categoryFixture(t)
	rec := postAPI(t, srv, "/api/v1/mods/fake/nope/category", `{"category":"Combat"}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestServer_APIModCategory_WithoutCSRF_Refuses proves the write goes
// through the same CSRF gate every other state-changing POST does, and
// writes nothing.
func TestServer_APIModCategory_WithoutCSRF_Refuses(t *testing.T) {
	srv, _, profilePath := categoryFixture(t)
	before := readProfile(t, profilePath)

	req := httptest.NewRequest(http.MethodPost, "http://"+testAddr+"/api/v1/mods/fake/1/category",
		strings.NewReader(`{"category":"Combat"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, before, readProfile(t, profilePath))
}

// TestServer_APICategories_ListsEffectiveCategories proves GET
// /api/v1/categories answers the distinct effective categories in group
// order (Service.ListCategories), user and source names alike.
func TestServer_APICategories_ListsEffectiveCategories(t *testing.T) {
	srv, _, _ := categoryFixture(t)

	get := func() []string {
		req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/categories", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var doc struct {
			Categories []string `json:"categories"`
		}
		decodeStrict(t, rec.Body.Bytes(), &doc)
		require.NotNil(t, doc.Categories, "an empty list is [], never null")
		return doc.Categories
	}

	assert.Equal(t, []string{"Weapons"}, get())
	require.Equal(t, http.StatusOK, postAPI(t, srv, "/api/v1/mods/fake/1/category", `{"category":"armour"}`).Code)
	assert.Equal(t, []string{"armour", "Weapons"}, get(), "sorted case-insensitively")
}

package core_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFakeRESTServer serves a minimal authenticated mod API plus the payload.
func newFakeRESTServer(t *testing.T, requireKey bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if requireKey && r.Header.Get("X-API-Key") != "e2e-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}

	mux.HandleFunc("/mods", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results": [{"id": 77, "name": "Cool Mod", "latest_version": "1.2.0"}], "total": 1}`)
	}))
	mux.HandleFunc("/mods/77", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id": 77, "name": "Cool Mod", "latest_version": "1.2.0"}`)
	}))
	mux.HandleFunc("/mods/77/files", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"files": [{"id": 900, "file_name": "cool-1.2.0.zip", "version": "1.2.0", "size_bytes": 11}]}`)
	}))
	mux.HandleFunc("/files/900/download", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"url": %q}`, srv.URL+"/dl/cool-1.2.0.zip")
	}))
	mux.HandleFunc("/dl/cool-1.2.0.zip", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("mod payload"))
	}))

	return srv
}

func apiSourceDef(baseURL string, withSearch bool) custom.SourceDefinition {
	endpoints := custom.APIEndpoints{
		GetMod:      &custom.EndpointConfig{Path: "/mods/{mod_id}"},
		ModFiles:    &custom.EndpointConfig{Path: "/mods/{mod_id}/files", List: "files"},
		DownloadURL: &custom.EndpointConfig{Path: "/files/{file_id}/download", Field: "url"},
	}
	if withSearch {
		endpoints.Search = &custom.EndpointConfig{Path: "/mods?q={query}&page={page}", List: "results", Total: "total"}
	}
	return custom.SourceDefinition{
		ID: "e2e-api", Name: "E2E API", Type: custom.TypeAPI, AllowHTTP: true,
		API: &custom.APIConfig{
			BaseURL:   baseURL,
			Auth:      &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}},
			Endpoints: endpoints,
			Mappings: custom.APIMappings{
				Mod:  map[string]string{"id": "id", "name": "name", "version": "latest_version"},
				File: map[string]string{"id": "id", "filename": "file_name", "version": "version", "size": "size_bytes"},
			},
		},
	}
}

func TestAPISourceEndToEnd(t *testing.T) {
	srv := newFakeRESTServer(t, true)

	src, err := custom.New(apiSourceDef(srv.URL, true))
	require.NoError(t, err)
	src.(interface{ SetAPIKey(string) }).SetAPIKey("e2e-key")

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.AddGame(game))
	ctx := context.Background()

	res, err := src.Search(ctx, source.SearchQuery{Query: "cool", GameID: "testgame", PageSize: 20})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	mod := res.Mods[0]
	assert.Equal(t, "e2e-api", mod.SourceID)
	assert.Equal(t, "testgame", mod.GameID)

	files, err := src.GetModFiles(ctx, &mod)
	require.NoError(t, err)
	require.Len(t, files, 1)

	result, err := svc.DownloadMod(ctx, "e2e-api", game, &mod, &files[0], nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)
	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version))

	installed := []domain.InstalledMod{{Mod: domain.Mod{ID: "77", SourceID: "e2e-api", Version: "1.0.0", GameID: "testgame"}}}
	updates, err := src.CheckUpdates(ctx, installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "1.2.0", updates[0].NewVersion)
}

// TestAPISourceInstallByIDOnly pins #49 acceptance criterion 1: a definition
// with no search endpoint works for install-by-ID, and search reports
// unsupported cleanly.
func TestAPISourceInstallByIDOnly(t *testing.T) {
	srv := newFakeRESTServer(t, false)

	src, err := custom.New(apiSourceDef(srv.URL, false))
	require.NoError(t, err)

	ctx := context.Background()
	_, err = src.Search(ctx, source.SearchQuery{Query: "cool"})
	assert.True(t, errors.Is(err, source.ErrNotSupported), "search must be a clean capability gap")
	assert.False(t, source.CapabilitiesOf(src).Search)

	mod, err := src.GetMod(ctx, "testgame", "77")
	require.NoError(t, err)
	files, err := src.GetModFiles(ctx, mod)
	require.NoError(t, err)
	u, err := src.GetDownloadURL(ctx, mod, files[0].ID)
	require.NoError(t, err)
	assert.Contains(t, u, "/dl/cool-1.2.0.zip")
}

// TestAPISourceKeyNeverInErrors pins #49 acceptance criterion 2's "never
// logged" half for the error path.
func TestAPISourceKeyNeverInErrors(t *testing.T) {
	def := apiSourceDef("http://127.0.0.1:1", true)
	def.API.Auth = &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "query", Name: "api_key"}}
	src, err := custom.New(def)
	require.NoError(t, err)
	src.(interface{ SetAPIKey(string) }).SetAPIKey("SUPERSECRET")

	_, err = src.Search(context.Background(), source.SearchQuery{Query: "x"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SUPERSECRET")
}

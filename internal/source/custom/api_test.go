package custom

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func apiDef(baseURL string) SourceDefinition {
	def := validAPIDef()
	def.API.BaseURL = baseURL
	def.AllowHTTP = true // httptest serves plain http
	return def
}

func TestNewAPIIsPure(t *testing.T) {
	a, err := NewAPI(apiDef("https://unreachable.invalid"))
	require.NoError(t, err)
	assert.Equal(t, "my-api", a.ID())
	assert.Equal(t, "My API", a.Name())
	assert.Equal(t, apiRequestTimeout, a.httpClient.Timeout)
	assert.Equal(t, 1, a.pageStart) // default when page_start omitted
}

func TestNewAPIExplicitPageStartZero(t *testing.T) {
	def := apiDef("https://x.test")
	zero := 0
	def.API.PageStart = &zero
	a, err := NewAPI(def)
	require.NoError(t, err)
	assert.Equal(t, 0, a.pageStart)
}

func TestAPIIdentityAndCapabilities(t *testing.T) {
	a, err := NewAPI(apiDef("https://x.test"))
	require.NoError(t, err)
	assert.Equal(t, source.Capabilities{Search: true, Dependencies: false, Updates: true, Auth: false}, a.Capabilities())
	assert.Empty(t, a.AuthURL())

	_, err = a.ExchangeToken(context.Background(), "code")
	assert.True(t, errors.Is(err, source.ErrNotSupported))
	_, err = a.GetDependencies(context.Background(), &domain.Mod{ID: "x"})
	assert.True(t, errors.Is(err, source.ErrNotSupported))

	def := apiDef("https://x.test")
	def.API.Endpoints.Search = nil
	def.API.Endpoints.GetMod = nil
	limited, err := NewAPI(def)
	require.NoError(t, err)
	assert.Equal(t, source.Capabilities{Search: false, Dependencies: false, Updates: false, Auth: false}, limited.Capabilities())
}

func TestBuildEndpointURL(t *testing.T) {
	got := buildEndpointURL("/mods?q={query}&page={page}&x={unknown}", map[string]string{
		"query": "cool mod & more", "page": "2",
	})
	assert.Equal(t, "/mods?q=cool+mod+%26+more&page=2&x={unknown}", got)
}

func TestGetJSONAuthAndErrors(t *testing.T) {
	t.Run("header auth attached and 401 maps to ErrAuthRequired", func(t *testing.T) {
		var gotKey string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotKey = r.Header.Get("X-API-Key")
			if gotKey == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"ok": true}`))
		}))
		defer srv.Close()

		def := apiDef(srv.URL)
		def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
		a, err := NewAPI(def)
		require.NoError(t, err)

		_, err = a.getJSON(context.Background(), srv.URL+"/mods/1")
		assert.True(t, errors.Is(err, domain.ErrAuthRequired))

		a.SetAPIKey("sekrit")
		doc, err := a.getJSON(context.Background(), srv.URL+"/mods/1")
		require.NoError(t, err)
		assert.Equal(t, "sekrit", gotKey)
		assert.Equal(t, map[string]any{"ok": true}, doc)
	})

	t.Run("query auth attached", func(t *testing.T) {
		var gotQuery string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query().Get("api_key")
			_, _ = w.Write([]byte(`{}`))
		}))
		defer srv.Close()

		def := apiDef(srv.URL)
		def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
		a, err := NewAPI(def)
		require.NoError(t, err)
		a.SetAPIKey("sekrit")

		_, err = a.getJSON(context.Background(), srv.URL+"/mods/1")
		require.NoError(t, err)
		assert.Equal(t, "sekrit", gotQuery)
	})

	t.Run("network error does not leak query key", func(t *testing.T) {
		def := apiDef("http://127.0.0.1:1")
		def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
		a, err := NewAPI(def)
		require.NoError(t, err)
		a.SetAPIKey("LEAKME")

		_, err = a.getJSON(context.Background(), "http://127.0.0.1:1/mods/1")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "LEAKME")
		assert.Contains(t, err.Error(), "my-api")
	})

	t.Run("non-200 surfaces status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()
		a, err := NewAPI(apiDef(srv.URL))
		require.NoError(t, err)
		_, err = a.getJSON(context.Background(), srv.URL+"/x")
		assert.ErrorContains(t, err, "HTTP 500")
	})
}

func TestAPIDownloadHeaders(t *testing.T) {
	def := apiDef("https://api.x.test")
	def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
	a, err := NewAPI(def)
	require.NoError(t, err)
	a.SetAPIKey("sekrit")

	assert.Equal(t, map[string]string{"X-API-Key": "sekrit"}, a.DownloadHeaders("https://api.x.test/dl/1.zip"))
	assert.Nil(t, a.DownloadHeaders("https://cdn.elsewhere.test/dl/1.zip"), "cross-origin downloads must not receive the key")
}

const apiSearchResponse = `{
	"results": [
		{"id": 1, "name": "Alpha Mod", "latest_version": "1.0.0"},
		{"id": 2, "name": "Beta Mod", "latest_version": "2.0.0"}
	],
	"pagination": {"total": 41}
}`

func TestAPISearch(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		_, _ = w.Write([]byte(apiSearchResponse))
	}))
	defer srv.Close()

	def := apiDef(srv.URL)
	def.API.Endpoints.Search = &EndpointConfig{
		Path:  "/mods?game={game_id}&q={query}&page={page}&limit={page_size}&skip={offset}",
		List:  "results",
		Total: "pagination.total",
	}
	a, err := NewAPI(def)
	require.NoError(t, err)

	res, err := a.Search(context.Background(), source.SearchQuery{
		GameID: "skyrim", Query: "cool mod", Page: 2, PageSize: 10,
	})
	require.NoError(t, err)

	// {page} = 0-based page + page_start(1) = 3; {offset} = 2*10 = 20.
	assert.Equal(t, "/mods?game=skyrim&q=cool+mod&page=3&limit=10&skip=20", gotPath)
	require.Len(t, res.Mods, 2)
	assert.Equal(t, "1", res.Mods[0].ID)
	assert.Equal(t, "Alpha Mod", res.Mods[0].Name)
	assert.Equal(t, "my-api", res.Mods[0].SourceID)
	assert.Equal(t, "skyrim", res.Mods[0].GameID)
	assert.Equal(t, 41, res.TotalCount)
	assert.Equal(t, 2, res.Page)
	assert.Equal(t, 10, res.PageSize)
}

func TestAPISearchNoEndpoint(t *testing.T) {
	def := apiDef("https://x.test")
	def.API.Endpoints.Search = nil
	a, err := NewAPI(def)
	require.NoError(t, err)

	_, err = a.Search(context.Background(), source.SearchQuery{Query: "x"})
	assert.True(t, errors.Is(err, source.ErrNotSupported))
}

func TestAPISearchMissingListPathFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"unexpected": {}}`))
	}))
	defer srv.Close()

	a, err := NewAPI(apiDef(srv.URL))
	require.NoError(t, err)
	_, err = a.Search(context.Background(), source.SearchQuery{Query: "x"})
	assert.ErrorContains(t, err, "results")
}

func TestAPISearchTotalAbsentIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results": []}`))
	}))
	defer srv.Close()

	a, err := NewAPI(apiDef(srv.URL))
	require.NoError(t, err)
	res, err := a.Search(context.Background(), source.SearchQuery{Query: "x"})
	require.NoError(t, err)
	assert.Zero(t, res.TotalCount)
	assert.Empty(t, res.Mods)
}

// newTestAPIServer wires a minimal fake REST API for the read ops.
func newTestAPIServer(t *testing.T) (*httptest.Server, *API) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/mods/77", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id": 77, "name": "Cool Mod", "latest_version": "1.2.0"}`))
	})
	mux.HandleFunc("/mods/77/files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files": [{"id": 900, "file_name": "cool-1.2.0.zip", "version": "1.2.0", "size_bytes": 4}]}`))
	})
	mux.HandleFunc("/files/900/download", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url": "` + srv.URL + `/dl/cool-1.2.0.zip"}`))
	})

	a, err := NewAPI(apiDef(srv.URL))
	require.NoError(t, err)
	return srv, a
}

func TestAPIGetMod(t *testing.T) {
	_, a := newTestAPIServer(t)

	mod, err := a.GetMod(context.Background(), "skyrim", "77")
	require.NoError(t, err)
	assert.Equal(t, "77", mod.ID)
	assert.Equal(t, "Cool Mod", mod.Name)
	assert.Equal(t, "1.2.0", mod.Version)
	assert.Equal(t, "skyrim", mod.GameID)
	assert.Equal(t, "my-api", mod.SourceID)
}

func TestAPIGetModFiles(t *testing.T) {
	_, a := newTestAPIServer(t)

	files, err := a.GetModFiles(context.Background(), &domain.Mod{ID: "77", GameID: "skyrim"})
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "900", files[0].ID)
	assert.Equal(t, "cool-1.2.0.zip", files[0].FileName)
	assert.Equal(t, int64(4), files[0].Size)
}

func TestAPIGetDownloadURL(t *testing.T) {
	srv, a := newTestAPIServer(t)

	u, err := a.GetDownloadURL(context.Background(), &domain.Mod{ID: "77", GameID: "skyrim"}, "900")
	require.NoError(t, err)
	assert.Equal(t, srv.URL+"/dl/cool-1.2.0.zip", u)
}

func TestAPIGetDownloadURLQueryAuthSameOriginOnly(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sameOrigin := srv.URL + "/dl/a.zip"
	mux.HandleFunc("/files/1/download", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url": "` + sameOrigin + `"}`))
	})
	mux.HandleFunc("/files/2/download", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url": "https://cdn.elsewhere.test/b.zip"}`))
	})

	def := apiDef(srv.URL)
	def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
	a, err := NewAPI(def)
	require.NoError(t, err)
	a.SetAPIKey("sekrit")

	u, err := a.GetDownloadURL(context.Background(), &domain.Mod{ID: "x"}, "1")
	require.NoError(t, err)
	assert.Contains(t, u, "api_key=sekrit", "same-origin download URL gets the query key")

	u, err = a.GetDownloadURL(context.Background(), &domain.Mod{ID: "x"}, "2")
	require.NoError(t, err)
	assert.NotContains(t, u, "sekrit", "cross-origin download URL must not carry the key")
}

func TestAPIReadOpsMissingEndpoints(t *testing.T) {
	def := apiDef("https://x.test")
	def.API.Endpoints = APIEndpoints{GetMod: &EndpointConfig{Path: "/mods/{mod_id}"}}
	a, err := NewAPI(def)
	require.NoError(t, err)

	_, err = a.GetModFiles(context.Background(), &domain.Mod{ID: "1"})
	assert.True(t, errors.Is(err, source.ErrNotSupported))
	_, err = a.GetDownloadURL(context.Background(), &domain.Mod{ID: "1"}, "f")
	assert.True(t, errors.Is(err, source.ErrNotSupported))
}

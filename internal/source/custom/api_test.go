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

package custom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- #122: api-type custom sources hard-disabled dependency resolution.
// A declarative endpoints.dependencies + mappings.dependency pair turns it
// on, using the same path/list/dot-path vocabulary the other endpoints use.

// depsDef builds an api definition pointed at baseURL with an optional
// dependencies endpoint and mapping.
func depsDef(baseURL string, ep *EndpointConfig, mapping map[string]string) SourceDefinition {
	return SourceDefinition{
		ID: "demo-api", Name: "Demo API", Type: TypeAPI,
		AllowHTTP: true,
		API: &APIConfig{
			BaseURL: baseURL,
			Endpoints: APIEndpoints{
				GetMod:       &EndpointConfig{Path: "/mods/{mod_id}"},
				Dependencies: ep,
			},
			Mappings: APIMappings{
				Mod:        map[string]string{"id": "id", "name": "name"},
				Dependency: mapping,
			},
		},
	}
}

func TestAPICapabilitiesDependencies(t *testing.T) {
	t.Run("no endpoint declared: still disabled", func(t *testing.T) {
		api, err := NewAPI(depsDef("https://api.x.test", nil, nil))
		require.NoError(t, err)
		assert.False(t, api.Capabilities().Dependencies)
	})

	t.Run("endpoint declared: enabled", func(t *testing.T) {
		api, err := NewAPI(depsDef("https://api.x.test",
			&EndpointConfig{Path: "/mods/{mod_id}/deps", List: "dependencies"},
			map[string]string{"mod_id": "id"}))
		require.NoError(t, err)
		assert.True(t, api.Capabilities().Dependencies)
	})
}

func TestAPIGetDependencies(t *testing.T) {
	fetch := func(t *testing.T, handler http.HandlerFunc, mapping map[string]string) ([]domain.ModReference, error) {
		t.Helper()
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		api, err := NewAPI(depsDef(server.URL,
			&EndpointConfig{Path: "/mods/{mod_id}/deps", List: "dependencies"}, mapping))
		require.NoError(t, err)
		return api.GetDependencies(context.Background(),
			&domain.Mod{ID: "42", SourceID: "demo-api", GameID: "g1"})
	}

	t.Run("maps ids, source refs and versions", func(t *testing.T) {
		var gotPath string
		refs, err := fetch(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_, _ = w.Write([]byte(`{"dependencies":[
				{"id":"7","source":"other-source","min_version":"2.0"},
				{"id":"9"}
			]}`))
		}, map[string]string{"mod_id": "id", "source_id": "source", "version": "min_version"})
		require.NoError(t, err)
		assert.Equal(t, "/mods/42/deps", gotPath)
		require.Len(t, refs, 2)
		assert.Equal(t, domain.ModReference{SourceID: "other-source", ModID: "7", Version: "2.0"}, refs[0])
		assert.Equal(t, domain.ModReference{SourceID: "demo-api", ModID: "9"}, refs[1],
			"an unmapped or absent source falls back to this source's own ID")
	})

	t.Run("an unmapped source_id always means this source", func(t *testing.T) {
		refs, err := fetch(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"dependencies":[{"id":"7","source":"ignored"}]}`))
		}, map[string]string{"mod_id": "id"})
		require.NoError(t, err)
		require.Len(t, refs, 1)
		assert.Equal(t, "demo-api", refs[0].SourceID)
	})

	t.Run("an empty list is no dependencies, not an error", func(t *testing.T) {
		refs, err := fetch(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"dependencies":[]}`))
		}, map[string]string{"mod_id": "id"})
		require.NoError(t, err)
		assert.Empty(t, refs)
	})

	t.Run("a null list is an empty list", func(t *testing.T) {
		refs, err := fetch(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"dependencies":null}`))
		}, map[string]string{"mod_id": "id"})
		require.NoError(t, err)
		assert.Empty(t, refs)
	})

	t.Run("a missing list is an error, not a silent empty", func(t *testing.T) {
		_, err := fetch(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"other":[]}`))
		}, map[string]string{"mod_id": "id"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `no "dependencies" array`)
	})

	t.Run("a list that is not an array is an error", func(t *testing.T) {
		_, err := fetch(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"dependencies":{"id":"7"}}`))
		}, map[string]string{"mod_id": "id"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not an array")
	})

	t.Run("a mapping that resolves to nothing names the entry and the path", func(t *testing.T) {
		_, err := fetch(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"dependencies":[{"id":"7"},{"nope":"8"}]}`))
		}, map[string]string{"mod_id": "id"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dependencies[1]")
		assert.Contains(t, err.Error(), `"mod_id"`)
	})

	t.Run("no endpoint declared stays ErrNotSupported", func(t *testing.T) {
		api, err := NewAPI(depsDef("https://api.x.test", nil, nil))
		require.NoError(t, err)
		_, derr := api.GetDependencies(context.Background(), &domain.Mod{ID: "42"})
		require.Error(t, derr)
		assert.ErrorIs(t, derr, source.ErrNotSupported)
	})

	t.Run("an upstream failure is returned", func(t *testing.T) {
		_, err := fetch(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}, map[string]string{"mod_id": "id"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})
}

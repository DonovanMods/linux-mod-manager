package custom

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- #121: custom api sources validate their key live at `lmm auth login`
// through a declarative auth.validate probe, instead of storing it
// unvalidated and finding out on first use.

// probeDef builds a minimal api definition pointed at baseURL, with an
// auth block carrying probe (nil for "no probe declared").
func probeDef(baseURL string, key *APIKeyConfig, probe *AuthValidateConfig) SourceDefinition {
	return SourceDefinition{
		ID: "demo-api", Name: "Demo API", Type: TypeAPI,
		AllowHTTP: true,
		API: &APIConfig{
			BaseURL:   baseURL,
			Auth:      &AuthConfig{APIKey: key, Validate: probe},
			Endpoints: APIEndpoints{GetMod: &EndpointConfig{Path: "/mods/{mod_id}"}},
			Mappings:  APIMappings{Mod: map[string]string{"id": "id", "name": "name"}},
		},
	}
}

func headerKey() *APIKeyConfig { return &APIKeyConfig{In: "header", Name: "X-API-Key"} }

// TestNewAPISource_KeyValidatorOnlyWhenDeclared pins the wiring #121 turns
// on: app.HasKeyValidator asks the TYPE whether a live check will happen,
// so a definition without a probe must not advertise one.
func TestNewAPISource_KeyValidatorOnlyWhenDeclared(t *testing.T) {
	t.Run("no probe declared: not a KeyValidator", func(t *testing.T) {
		src, err := NewAPISource(probeDef("https://api.x.test", headerKey(), nil))
		require.NoError(t, err)
		_, ok := src.(source.KeyValidator)
		assert.False(t, ok, "without a probe the key is still stored unvalidated")
	})

	t.Run("probe declared: a KeyValidator", func(t *testing.T) {
		src, err := NewAPISource(probeDef("https://api.x.test", headerKey(),
			&AuthValidateConfig{Path: "/me"}))
		require.NoError(t, err)
		_, ok := src.(source.KeyValidator)
		assert.True(t, ok)
	})

	t.Run("no auth at all: not a KeyValidator", func(t *testing.T) {
		def := probeDef("https://api.x.test", headerKey(), nil)
		def.API.Auth = nil
		src, err := NewAPISource(def)
		require.NoError(t, err)
		_, ok := src.(source.KeyValidator)
		assert.False(t, ok)
	})

	t.Run("the wrapper still forwards the source's own methods", func(t *testing.T) {
		src, err := NewAPISource(probeDef("https://api.x.test", headerKey(),
			&AuthValidateConfig{Path: "/me"}))
		require.NoError(t, err)
		assert.Equal(t, "demo-api", src.ID())
		assert.Equal(t, "api", source.TypeLabelOf(src))
		assert.True(t, source.CapabilitiesOf(src).Auth)

		setter, ok := src.(interface{ SetAPIKey(string) })
		require.True(t, ok, "the app layer finds SetAPIKey by duck typing")
		setter.SetAPIKey("k")
		authed, ok := src.(interface{ IsAuthenticated() bool })
		require.True(t, ok)
		assert.True(t, authed.IsAuthenticated())
	})
}

func TestAPIValidateKey(t *testing.T) {
	validate := func(t *testing.T, handler http.HandlerFunc, key *APIKeyConfig, probe *AuthValidateConfig) error {
		t.Helper()
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		src, err := NewAPISource(probeDef(server.URL, key, probe))
		require.NoError(t, err)
		validator, ok := src.(source.KeyValidator)
		require.True(t, ok)
		return validator.ValidateKey(context.Background(), "candidate-key")
	}

	t.Run("200 with the key in a header is valid", func(t *testing.T) {
		var gotPath, gotKey, gotMethod string
		err := validate(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotKey, gotMethod = r.URL.Path, r.Header.Get("X-API-Key"), r.Method
			_, _ = w.Write([]byte(`{"user":{"id":7}}`))
		}, headerKey(), &AuthValidateConfig{Path: "/me"})
		require.NoError(t, err)
		assert.Equal(t, "/me", gotPath)
		assert.Equal(t, "candidate-key", gotKey)
		assert.Equal(t, http.MethodGet, gotMethod, "GET is the default method")
	})

	t.Run("a query-mode key rides the query string", func(t *testing.T) {
		var gotKey string
		err := validate(t, func(w http.ResponseWriter, r *http.Request) {
			gotKey = r.URL.Query().Get("api_key")
			_, _ = w.Write([]byte(`{}`))
		}, &APIKeyConfig{In: "query", Name: "api_key"}, &AuthValidateConfig{Path: "/me"})
		require.NoError(t, err)
		assert.Equal(t, "candidate-key", gotKey)
	})

	t.Run("the declared method is used", func(t *testing.T) {
		var gotMethod string
		err := validate(t, func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			_, _ = w.Write([]byte(`{}`))
		}, headerKey(), &AuthValidateConfig{Path: "/session", Method: "post"})
		require.NoError(t, err)
		assert.Equal(t, http.MethodPost, gotMethod, "the method is normalised to upper case")
	})

	t.Run("401 is an invalid key, named with the source", func(t *testing.T) {
		err := validate(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad token candidate-key"}`))
		}, headerKey(), &AuthValidateConfig{Path: "/me"})
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrAuthRequired)
		assert.Contains(t, err.Error(), `source "demo-api"`)
		assert.Contains(t, err.Error(), "rejected this key")
		assert.NotContains(t, err.Error(), "candidate-key",
			"a 401 body is never quoted back: the one thing it might echo is the key")
	})

	t.Run("403 is an invalid key too", func(t *testing.T) {
		err := validate(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}, headerKey(), &AuthValidateConfig{Path: "/me"})
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrAuthRequired)
	})

	t.Run("a 5xx is a service failure, not a bad key", func(t *testing.T) {
		err := validate(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}, headerKey(), &AuthValidateConfig{Path: "/me"})
		require.Error(t, err)
		assert.NotErrorIs(t, err, domain.ErrAuthRequired)
		assert.Contains(t, err.Error(), "502")
	})

	t.Run("an explicit expected status is exact", func(t *testing.T) {
		ok := validate(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}, headerKey(), &AuthValidateConfig{Path: "/me", Status: 204})
		require.NoError(t, ok)

		mismatch := validate(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}, headerKey(), &AuthValidateConfig{Path: "/me", Status: 204})
		require.Error(t, mismatch)
		assert.Contains(t, mismatch.Error(), "expected 204")
	})

	t.Run("a declared field must be present", func(t *testing.T) {
		ok := validate(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"user":{"id":7,"name":"me"}}`))
		}, headerKey(), &AuthValidateConfig{Path: "/me", Field: "user.id"})
		require.NoError(t, ok)

		missing := validate(t, func(w http.ResponseWriter, _ *http.Request) {
			// An API that answers 200 with an anonymous document instead of
			// refusing outright - the case Field exists for.
			_, _ = w.Write([]byte(`{"user":null,"anonymous":true}`))
		}, headerKey(), &AuthValidateConfig{Path: "/me", Field: "user.id"})
		require.Error(t, missing)
		assert.ErrorIs(t, missing, domain.ErrAuthRequired)
		assert.Contains(t, missing.Error(), "user.id")
	})

	t.Run("an unparseable response with a declared field fails clearly", func(t *testing.T) {
		err := validate(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{not-json`))
		}, headerKey(), &AuthValidateConfig{Path: "/me", Field: "user.id"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing key-validation response")
	})

	t.Run("a network failure is a clear failure, not a verdict", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		base := server.URL
		server.Close() // nothing is listening now

		src, err := NewAPISource(probeDef(base, &APIKeyConfig{In: "query", Name: "api_key"},
			&AuthValidateConfig{Path: "/me"}))
		require.NoError(t, err)
		validator, ok := src.(source.KeyValidator)
		require.True(t, ok)

		verr := validator.ValidateKey(context.Background(), "candidate-key")
		require.Error(t, verr)
		assert.NotErrorIs(t, verr, domain.ErrAuthRequired, "unreachable is not rejected")
		assert.Contains(t, verr.Error(), "could not reach")
		assert.NotContains(t, verr.Error(), "candidate-key",
			"a query-mode key must never appear in an error message")
	})

	t.Run("validating never disturbs the source's own key", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		src, err := NewAPISource(probeDef(server.URL, headerKey(), &AuthValidateConfig{Path: "/me"}))
		require.NoError(t, err)
		src.(interface{ SetAPIKey(string) }).SetAPIKey("the-live-key")

		verr := src.(source.KeyValidator).ValidateKey(context.Background(), "a-bad-candidate")
		require.Error(t, verr)
		assert.True(t, src.(interface{ IsAuthenticated() bool }).IsAuthenticated(),
			"a failed check must leave the running source exactly as it was")
	})
}

// TestAPIValidateKeyWithoutAProbeIsUnsupported covers the one path the
// wrapper cannot reach: validateKey called on a bare *API with no probe.
func TestAPIValidateKeyWithoutAProbeIsUnsupported(t *testing.T) {
	api, err := NewAPI(probeDef("https://api.x.test", headerKey(), nil))
	require.NoError(t, err)
	err = api.validateKey(context.Background(), "k")
	require.Error(t, err)
	assert.True(t, errors.Is(err, source.ErrNotSupported))
}

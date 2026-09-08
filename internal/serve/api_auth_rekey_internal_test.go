package serve

// The live re-key: a credential saved or removed through the Setup surface
// must reach the ALREADY-REGISTERED source object, not just the database.
// Before #333 it did not, and "log in, then search still fails until you
// restart" was the first-run experience of the surface whose whole job is
// first run (A1's recorded follow-up).
//
// The fixture is a custom `api` source pointed at a local httptest server
// that echoes back the key it was sent, so "which key is the live source
// using" is an observable fact rather than an inspection of private state.

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echoSourceID is the id the re-key fixture's custom source registers under.
const echoSourceID = "echo"

// newKeyEchoServer answers every request with one mod whose NAME is the API
// key the request carried.
func newKeyEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Test-Key")
		if key == "" {
			key = "(none)"
		}
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // best-effort test-server write
		_, _ = fmt.Fprintf(w, `{"results":[{"id":"1","name":%q}]}`, key)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// echoDefinitionYAML is an auth-capable api definition against base.
func echoDefinitionYAML(base string) string {
	return fmt.Sprintf(`id: %s
name: Echo Source
type: api
allow_http: true
api:
  base_url: %s
  auth:
    api_key:
      in: header
      name: X-Test-Key
  endpoints:
    search:
      path: /search
      list: results
  mappings:
    mod:
      id: id
      name: name
`, echoSourceID, base)
}

// liveKey asks the REGISTERED source which key it is actually sending.
func liveKey(t *testing.T, s *Server) string {
	t.Helper()
	src, err := s.svc.GetSource(echoSourceID)
	require.NoError(t, err)
	res, err := src.Search(t.Context(), source.SearchQuery{PageSize: 1})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	return res.Mods[0].Name
}

// newRekeyFixtureServer builds the deploy fixture server with the echo
// source saved and registered through the editor's own PUT route - so the
// two features are exercised together, exactly as the SPA drives them.
func newRekeyFixtureServer(t *testing.T) *Server {
	t.Helper()
	s, _ := newDeployFixtureServer(t)
	// No environment key: the stored token is what resolves here.
	t.Setenv(app.EnvKeyForSourceID(echoSourceID), "")

	body, err := json.Marshal(sourceSaveRequest{YAML: echoDefinitionYAML(newKeyEchoServer(t).URL)})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK,
		doAPI(s, http.MethodPut, "/api/v1/sources/"+echoSourceID, string(body)).Code)
	return s
}

func TestAPIAuthLogin_ReKeysTheRunningSource(t *testing.T) {
	s := newRekeyFixtureServer(t)
	require.Equal(t, "(none)", liveKey(t, s), "the fixture starts unauthenticated")

	rec := doAPI(s, http.MethodPost, "/api/v1/auth/"+echoSourceID, `{"api_key":"key-B"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, "key-B", liveKey(t, s),
		"the next call through the registry uses the new key - no restart")
}

func TestAPIAuthLogout_ReKeysTheRunningSourceBackToNothing(t *testing.T) {
	s := newRekeyFixtureServer(t)
	require.Equal(t, http.StatusOK, doAPI(s, http.MethodPost, "/api/v1/auth/"+echoSourceID, `{"api_key":"key-B"}`).Code)
	require.Equal(t, "key-B", liveKey(t, s))

	rec := doAPI(s, http.MethodDelete, "/api/v1/auth/"+echoSourceID, "")
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, "(none)", liveKey(t, s), "a logout falls back to the env/none state immediately")
}

// TestAPIAuthLogin_EnvironmentStillWins pins that the re-key resolves the
// key exactly as startup does - ResolveAPIKey's env-over-stored-token
// precedence - rather than blindly installing whatever was just posted.
func TestAPIAuthLogin_EnvironmentStillWins(t *testing.T) {
	s := newRekeyFixtureServer(t)
	t.Setenv(app.EnvKeyForSourceID(echoSourceID), "key-ENV")

	require.Equal(t, http.StatusOK, doAPI(s, http.MethodPost, "/api/v1/auth/"+echoSourceID, `{"api_key":"key-B"}`).Code)
	assert.Equal(t, "key-ENV", liveKey(t, s))
}

// TestAPIAuthLogin_ReKeyFailureStillStoresTheCredential pins the
// best-effort contract: with the definition file gone from under it the
// source cannot be rebuilt, so the live swap fails - and the request still
// succeeds, because the key IS stored. That is the pre-#333 "picked up at
// the next start" behaviour, not a credential write to report as failed.
func TestAPIAuthLogin_ReKeyFailureStillStoresTheCredential(t *testing.T) {
	s := newRekeyFixtureServer(t)
	path, err := app.SourceDefinitionFile(s.svc.ConfigDir(), echoSourceID)
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))

	rec := doAPI(s, http.MethodPost, "/api/v1/auth/"+echoSourceID, `{"api_key":"key-B"}`)
	require.Equal(t, http.StatusOK, rec.Code, "the credential write is what the caller asked for, and it happened")

	token, err := s.svc.GetSourceToken(t.Context(), echoSourceID)
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, "key-B", token.APIKey)
	assert.Equal(t, "(none)", liveKey(t, s), "the live source is untouched until a restart")
}

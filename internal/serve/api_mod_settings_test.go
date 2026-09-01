package serve_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postAPI is a small local helper for the CSRF-carrying POST every
// mutation test in this file needs - the black-box (serve_test) equivalent
// of the package-internal apiRequest/doAPI pair, built from what this
// package already exposes: New's returned *serve.Server has no exported
// CSRF token, so the token is scraped from the served shell exactly the way
// a real client (and the E2E harness's postAsAnotherClient) would.
func postAPI(t *testing.T, srv *serve.Server, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	shellReq := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/", nil)
	shellRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(shellRec, shellReq)
	require.Equal(t, http.StatusOK, shellRec.Code)

	match := csrfMetaPattern.FindStringSubmatch(shellRec.Body.String())
	require.Len(t, match, 2, "the shell must carry a CSRF token")

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(http.MethodPost, "http://"+testAddr+target, reader)
	req.Header.Set("X-CSRF-Token", match[1])
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestServer_APIModLock_ReturnsExactModSettingResult proves POST
// .../lock decodes into core.ModSettingResult with unknown members
// rejected AND byte-matches core.EncodeJSON of the equivalent live
// SetModLock call - the same "wire = core documents only" rule every other
// endpoint follows, applied to a thin, non-job mutation route.
func TestServer_APIModLock_ReturnsExactModSettingResult(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{
		ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID,
	}, true, nil)
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "1", Version: "1.0"}))

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	rec := postAPI(t, srv, "/api/v1/mods/fake/1/lock", `{}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result core.ModSettingResult
	decodeStrict(t, rec.Body.Bytes(), &result)
	assert.True(t, result.Locked)
	assert.Equal(t, "1.0", result.LockedVersion, "an empty version locks at whatever is currently installed")
}

// TestServer_APIModUnlock_ClearsTheLock proves the round trip: locking then
// unlocking the same mod leaves it unlocked, reported via the same
// ModSettingResult document.
func TestServer_APIModUnlock_ClearsTheLock(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{
		ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID,
	}, true, nil)
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "1", Version: "1.0"}))

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	require.Equal(t, http.StatusOK, postAPI(t, srv, "/api/v1/mods/fake/1/lock", `{}`).Code)

	rec := postAPI(t, srv, "/api/v1/mods/fake/1/unlock", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result core.ModSettingResult
	decodeStrict(t, rec.Body.Bytes(), &result)
	assert.False(t, result.Locked)
	assert.Empty(t, result.LockedVersion)
}

// TestServer_APIModUpdatePolicy_SetsThePolicy proves the policy round trip
// and that domain.UpdatePolicy's own text encoding governs the wire value.
func TestServer_APIModUpdatePolicy_SetsThePolicy(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{
		ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID,
	}, true, nil)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	rec := postAPI(t, srv, "/api/v1/mods/fake/1/update-policy", `{"policy":"pinned"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result core.ModSettingResult
	decodeStrict(t, rec.Body.Bytes(), &result)
	assert.Equal(t, domain.UpdatePinned, result.UpdatePolicy)
}

// TestServer_APIModUpdatePolicy_UnknownValue_Renders400 proves a malformed
// policy string is refused at the boundary (domain.UpdatePolicy.
// UnmarshalText rejects it), never silently landing on the zero value.
func TestServer_APIModUpdatePolicy_UnknownValue_Renders400(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{
		ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID,
	}, true, nil)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	rec := postAPI(t, srv, "/api/v1/mods/fake/1/update-policy", `{"policy":"bogus"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestServer_APIModLock_WithoutCSRF_Refuses proves lock/unlock/update-policy
// go through the same CSRF gate every other mutation route does - it is not
// a job, but it is still a state-changing POST.
func TestServer_APIModLock_WithoutCSRF_Refuses(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{
		ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID,
	}, true, nil)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodPost, "http://"+testAddr+"/api/v1/mods/fake/1/lock", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusOK, rec.Code)
}

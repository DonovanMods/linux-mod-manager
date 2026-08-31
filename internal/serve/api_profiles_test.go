package serve_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_APIProfiles_ReturnsExactProfileListing is /api/v1/profiles's
// headline RED test (docs/plans/2026-08-30-serve-impl.md Task 5): the body
// must decode into core.ProfileListing with unknown members rejected AND
// byte-match core.EncodeJSON of the same live ListProfiles call.
func TestServer_APIProfiles_ReturnsExactProfileListing(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "survival")
	require.NoError(t, err)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/profiles", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var listing core.ProfileListing
	decodeStrict(t, rec.Body.Bytes(), &listing)
	assert.Len(t, listing.Profiles, 2)

	want, err := svc.ListProfiles(context.Background(), game.ID)
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// TestServer_APIProfiles_UnknownProfileParam_StillSucceeds proves
// /api/v1/profiles only needs the GAME half of the selection to resolve,
// like its page (pages_profiles.go): an unresolvable ?profile= is
// irrelevant to a query that lists every profile a game has, so it must
// not 404.
func TestServer_APIProfiles_UnknownProfileParam_StillSucceeds(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/profiles?profile=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var listing core.ProfileListing
	decodeStrict(t, rec.Body.Bytes(), &listing)
}

// TestServer_APIProfiles_UnknownGameParam_Renders404 proves the game half
// still gates the endpoint: an unresolvable ?game= answers the envelope at
// 404.
func TestServer_APIProfiles_UnknownGameParam_Renders404(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/profiles?game=nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, "nope")
}

// TestServer_APIProfiles_InternalFailure_Renders500 is the task-5 gate
// review's Minor 2 fix: ListProfiles' 500 branch (api.go's
// handleAPIProfiles) was untested - the package's only 500 case lived on
// /api/v1/mods, and unlike that one, ListProfiles reads profiles from disk
// rather than the DB, so closing the DB (the /mods test's fault) has no
// effect here. Making the profiles directory unreadable forces the same
// ListProfiles failure the handler's own call would hit.
func TestServer_APIProfiles_InternalFailure_Renders500(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}

	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)

	profilesDir := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles")
	require.NoError(t, os.Chmod(profilesDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(profilesDir, 0o755) })

	srv := serve.New(svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/profiles", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var envelope apiErrorEnvelope
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.NotEmpty(t, envelope.Error)
}

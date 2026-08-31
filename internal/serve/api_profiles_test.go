package serve_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

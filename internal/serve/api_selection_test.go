package serve_test

// The shared ?game/?profile resolution, asserted through /api/v1/mods -
// resolveSelection has no exported surface of its own, and every scoped
// endpoint resolves through it (docs/plans/2026-08-30-serve-impl.md Task 4
// ruling on game/profile selection).
//
// Ported from the deleted selection_test.go, which asserted the same
// resolution through the /mods PAGE. The page rendered an unresolved
// selection as a friendly 200 with a switcher; /api/v1 answers the 404
// envelope whose details list the valid choices (Task 5 ruling), and the
// SPA builds its own game/profile picker from exactly those details - so
// the assertions moved from "the warning is on the page and the valid
// choices are offered" to "the warning is in the envelope and the valid
// choices are in its details".

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selectionEnvelope is the {"error","details"} envelope an unresolved
// selection answers with, decoded far enough to read the warning and the
// choices a client would offer instead.
type selectionEnvelope struct {
	Error   string `json:"error"`
	Details struct {
		Games []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"games"`
		Profiles []string `json:"profiles"`
	} `json:"details"`
}

// getSelection drives GET /api/v1/mods with the given query and decodes the
// selection envelope it answered with.
func getSelection(t *testing.T, srv *serve.Server, query string) (int, selectionEnvelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods"+query, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var envelope selectionEnvelope
	if rec.Code != http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	}
	return rec.Code, envelope
}

// TestServer_APISelection_UnknownGameParam_NamesTheValidGames proves an
// explicit ?game= naming an unconfigured game is refused with a warning
// that names the value, and still lists the game(s) a client may switch to.
func TestServer_APISelection_UnknownGameParam_NamesTheValidGames(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:          "g2",
		Name:        "Second Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
	}))
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	code, envelope := getSelection(t, srv, "?game=nope")

	require.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, envelope.Error, "unknown game")
	assert.Contains(t, envelope.Error, "nope")
	require.NotEmpty(t, envelope.Details.Games, "the valid games must be offered, not just the refusal")
	names := make([]string, 0, len(envelope.Details.Games))
	for _, g := range envelope.Details.Games {
		names = append(names, g.Name)
	}
	assert.Contains(t, names, "Fixture Game")
}

// TestServer_APISelection_NoDefaultGame_OffersTheConfiguredGame proves a
// game configured but with no default game SET (`lmm game set-default`
// never run) is refused rather than resolved arbitrarily - and that the
// refusal still carries the one configured game, so a client can offer a
// working picker instead of a dead end (epic re-review N-1, whose page-side
// fix was the nav switcher).
func TestServer_APISelection_NoDefaultGame_OffersTheConfiguredGame(t *testing.T) {
	svc := newFixtureServiceNoGames(t)
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "g1", Name: "Undefaulted Game", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
	}))
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	code, envelope := getSelection(t, srv, "")

	require.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, envelope.Error, "no default game is configured")
	require.Len(t, envelope.Details.Games, 1, "the one configured game must be a pickable option")
	assert.Equal(t, "g1", envelope.Details.Games[0].ID)
	assert.Equal(t, "Undefaulted Game", envelope.Details.Games[0].Name)
}

// TestServer_APISelection_DefaultSelection_ResolvesGameAndProfile proves the
// zero-param case resolves the configured default game and its default
// profile rather than refusing.
func TestServer_APISelection_DefaultSelection_ResolvesGameAndProfile(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var list struct {
		GameID  string `json:"game_id"`
		Profile string `json:"profile"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Equal(t, "g1", list.GameID)
	assert.Equal(t, "default", list.Profile)
}

// TestServer_APIScopedEndpoints_UnresolvedSelection_Renders404 holds every
// scoped GET endpoint to the Task 5 ruling: unlike the old pages (which
// degraded to a 200 friendly empty state), the API has no page to degrade
// to, so an unresolved selection answers the {"error","details"} envelope
// at 404 with details listing the valid choices. Each endpoint resolves
// through the one resolveSelection, so each row is a wiring check: zero
// games configured (the choices are empty), an explicit ?game= naming an
// unconfigured game (the warning names the value; the choices are the real
// game), and on /mods an explicit ?profile= naming an unconfigured profile
// (the task-5 gate review's Minor 3: the resolved game is kept, so the
// choices are its real profiles). /search carries q=boots so the row
// proves its missing-q 400 does not swallow the selection 404.
func TestServer_APIScopedEndpoints_UnresolvedSelection_Renders404(t *testing.T) {
	noGames := func(t *testing.T) *serve.Server {
		return serve.New(t.Context(), newFixtureServiceNoGames(t), slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	}
	oneGame := func(t *testing.T) *serve.Server {
		svc, _ := newFixtureServiceWithSource(t, newFakeSource("fake"))
		return serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	}

	tests := []struct {
		name         string
		server       func(t *testing.T) *serve.Server
		target       string
		wantError    []string
		wantGames    []string // IDs; nil means none offered
		wantProfiles []string
	}{
		{"mods, no games", noGames, "/api/v1/mods", nil, nil, nil},
		{"health, no games", noGames, "/api/v1/health", nil, nil, nil},
		{"conflicts, no games", noGames, "/api/v1/conflicts", nil, nil, nil},
		{"mods, unknown game", oneGame, "/api/v1/mods?game=nope", []string{"nope"}, []string{"g1"}, nil},
		{"status, unknown game", oneGame, "/api/v1/status?game=nope", []string{"nope"}, []string{"g1"}, nil},
		{"profiles, unknown game", oneGame, "/api/v1/profiles?game=nope", []string{"nope"}, []string{"g1"}, nil},
		{"search, unknown game", oneGame, "/api/v1/search?q=boots&game=nope", []string{"nope"}, []string{"g1"}, nil},
		{"mods, unknown profile", oneGame, "/api/v1/mods?profile=nope", []string{"unknown profile", "nope"}, []string{"g1"}, []string{"default"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := tt.server(t)
			req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+tt.target, nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			var envelope struct {
				Error   string `json:"error"`
				Details struct {
					Games    []core.GameListEntry `json:"games"`
					Profiles []string             `json:"profiles"`
				} `json:"details"`
			}
			decodeStrict(t, rec.Body.Bytes(), &envelope)
			assert.NotEmpty(t, envelope.Error)
			for _, w := range tt.wantError {
				assert.Contains(t, envelope.Error, w)
			}
			if tt.wantGames != nil {
				ids := make([]string, 0, len(envelope.Details.Games))
				for _, g := range envelope.Details.Games {
					ids = append(ids, g.ID)
				}
				assert.Equal(t, tt.wantGames, ids)
			} else {
				assert.Empty(t, envelope.Details.Games)
			}
			if tt.wantProfiles != nil {
				assert.Equal(t, tt.wantProfiles, envelope.Details.Profiles)
			}
		})
	}
}

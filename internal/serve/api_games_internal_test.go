package serve

// httptest coverage for the Setup surface's game routes (api_games.go):
// the chooser listing, the catalog search, the add write, and the detect
// scan's two halves. Package-internal because the fixtures build the
// *Server directly (doAPI needs the process CSRF token).

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gamesCatalogSource is a fake source.GameCatalog - the catalog tests
// never touch a real CurseForge (nothing in this package may reach the
// network).
type gamesCatalogSource struct {
	fixtureSource
	entries []source.GameEntry
	listErr error
}

func (g *gamesCatalogSource) ID() string   { return "fakecat" }
func (g *gamesCatalogSource) Name() string { return "Fake Catalog" }
func (g *gamesCatalogSource) ListGames(context.Context) ([]source.GameEntry, error) {
	return g.entries, g.listErr
}

var _ source.GameCatalog = (*gamesCatalogSource)(nil)

// namedFixtureSource is fixtureSource with a settable id/name, for the
// add-game fixtures below that need "nexusmods"/"curseforge" specifically
// registered - AddGame refuses an id no registered source claims (#333
// Important #1), and these tests are exercising the add flow, not that
// check.
type namedFixtureSource struct {
	fixtureSource
	id, name string
}

func (n *namedFixtureSource) ID() string   { return n.id }
func (n *namedFixtureSource) Name() string { return n.name }

// newGamesServer builds a Server over a Service with no games configured -
// the first-run state the Setup surface exists for - but with "nexusmods"
// and "curseforge" pre-registered, since the add-game tests below spend
// those two ids as SourceID.
func newGamesServer(t *testing.T) *Server {
	t.Helper()
	sandboxEnv(t)
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&namedFixtureSource{id: "nexusmods", name: "NexusMods"})
	svc.RegisterSource(&namedFixtureSource{id: "curseforge", name: "CurseForge"})
	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr})
}

// TestAPIGames_EmptyIsTheFirstRunSignal pins that a fresh install answers
// with an empty array (never null) - the SPA branches on it to show the
// first-run flow rather than an empty chooser.
func TestAPIGames_EmptyIsTheFirstRunSignal(t *testing.T) {
	s := newGamesServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/games", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

// TestAPIGames_ListsTheCoreDocument pins the wire rule: the response is
// byte-identically core.EncodeJSON of svc.ListGameEntries - the array `lmm
// game list --json` emits, with no serve-side re-rendering.
func TestAPIGames_ListsTheCoreDocument(t *testing.T) {
	s, game := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/games", "")
	require.Equal(t, http.StatusOK, rec.Code)

	want, err := s.svc.ListGameEntries(t.Context())
	require.NoError(t, err)
	requireEncodesLikeInternal(t, rec.Body.Bytes(), want)

	var got []core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got, json.RejectUnknownMembers(true)))
	require.Len(t, got, 1)
	assert.Equal(t, game.ID, got[0].ID)
}

// TestAPIGamesCatalog_ReturnsTheCoreReport pins the search behind the add
// form's game field.
func TestAPIGamesCatalog_ReturnsTheCoreReport(t *testing.T) {
	s := newGamesServer(t)
	s.svc.RegisterSource(&gamesCatalogSource{entries: []source.GameEntry{
		{ID: "432", Name: "Minecraft", Slug: "minecraft"},
		{ID: "7", Name: "Valheim", Slug: "valheim"},
	}})

	rec := doAPI(s, http.MethodGet, "/api/v1/games/catalog?source=fakecat&q=mine", "")
	require.Equal(t, http.StatusOK, rec.Code)

	var report core.GameCatalogReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report, json.RejectUnknownMembers(true)))
	assert.Equal(t, "fakecat", report.SourceID)
	require.Len(t, report.Matches, 1)
	assert.Equal(t, "minecraft", report.Matches[0].GameID)
	assert.Equal(t, "432", report.Matches[0].Identifier)
}

// TestAPIGamesCatalog_Refusals pins every failure's status: a missing
// parameter and a catalog-less source are the caller's (400), an
// unregistered source is 404, a source that refused for want of a
// credential is 401 (#333 - the SPA's answer to that one is "authenticate
// <source> first", not "the upstream broke"), and any OTHER failure of the
// source's own call is 502 - this server proxied that call, it did not
// itself fail.
func TestAPIGamesCatalog_Refusals(t *testing.T) {
	tests := []struct {
		name   string
		target string
		src    source.ModSource
		want   int
	}{
		{"no source param", "/api/v1/games/catalog?q=x", nil, http.StatusBadRequest},
		{"no query param", "/api/v1/games/catalog?source=fakecat", &gamesCatalogSource{}, http.StatusBadRequest},
		{"unknown source", "/api/v1/games/catalog?source=nope&q=x", nil, http.StatusNotFound},
		{"source has no catalog", "/api/v1/games/catalog?source=fake&q=x", &fixtureSource{}, http.StatusBadRequest},
		{"source needs a credential", "/api/v1/games/catalog?source=fakecat&q=x", &gamesCatalogSource{listErr: domain.ErrAuthRequired}, http.StatusUnauthorized},
		{"source needs a credential, wrapped", "/api/v1/games/catalog?source=fakecat&q=x", &gamesCatalogSource{listErr: fmt.Errorf("listing games: %w", domain.ErrAuthRequired)}, http.StatusUnauthorized},
		{"source call failed for another reason", "/api/v1/games/catalog?source=fakecat&q=x", &gamesCatalogSource{listErr: errors.New("connection reset")}, http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newGamesServer(t)
			if tt.src != nil {
				s.svc.RegisterSource(tt.src)
			}
			rec := doAPI(s, http.MethodGet, tt.target, "")
			assert.Equal(t, tt.want, rec.Code, "body: %s", rec.Body.String())
			var env apiErrorEnvelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.NotEmpty(t, env.Error)
		})
	}
}

// TestAPIGameAdd_WritesTheGameAndAnswersWithItsRow drives the add end to
// end and asserts the END STATE, not just the document: games.yaml holds
// the game and its default profile exists.
func TestAPIGameAdd_WritesTheGameAndAnswersWithItsRow(t *testing.T) {
	s := newGamesServer(t)
	install := t.TempDir()

	rec := doAPI(s, http.MethodPost, "/api/v1/games", `{
		"source_id": "nexusmods",
		"identifier": "skyrimspecialedition",
		"name": "Skyrim Special Edition",
		"install_path": `+jsonString(install)+`
	}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "skyrimspecialedition", entry.ID)
	assert.Equal(t, filepath.Join(install, "mods"), entry.ModPath)

	got, err := s.svc.GetGame("skyrimspecialedition")
	require.NoError(t, err)
	assert.Equal(t, "Skyrim Special Edition", got.Name)

	profile, err := s.svc.NewProfileManager().Get(t.Context(), "skyrimspecialedition", "default")
	require.NoError(t, err)
	assert.Equal(t, "default", profile.Name)
}

// TestAPIGameAdd_ExplicitGameIDKeysTheCatalogPath pins the one member with
// no CLI flag: the SPA's catalog flow passes the game_id core suggested,
// which is what keeps a CurseForge add keyed "minecraft".
func TestAPIGameAdd_ExplicitGameIDKeysTheCatalogPath(t *testing.T) {
	s := newGamesServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/games", `{
		"source_id": "curseforge", "identifier": "432", "game_id": "minecraft",
		"name": "Minecraft", "install_path": `+jsonString(t.TempDir())+`
	}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "minecraft", entry.ID)
	assert.Equal(t, map[string]string{"curseforge": "432"}, entry.SourceIDs)
}

// TestAPIGameAdd_FieldErrorsNameTheirField pins the 400 the SPA form needs:
// the envelope's details carry {field, value, reason}, so the message lands
// on the offending input rather than beside the form.
func TestAPIGameAdd_FieldErrorsNameTheirField(t *testing.T) {
	install := t.TempDir()
	tests := []struct {
		name, body, field string
	}{
		{"missing source", `{"identifier":"a","name":"A","install_path":` + jsonString(install) + `}`, "source_id"},
		{"missing identifier", `{"source_id":"nexusmods","name":"A","install_path":` + jsonString(install) + `}`, "identifier"},
		{"missing name", `{"source_id":"nexusmods","identifier":"a","install_path":` + jsonString(install) + `}`, "name"},
		{"missing install path", `{"source_id":"nexusmods","identifier":"a","name":"A"}`, "install_path"},
		{"install path does not exist", `{"source_id":"nexusmods","identifier":"a","name":"A","install_path":"/definitely/not/here"}`, "install_path"},
		// #333 Minor #2: an explicit game_id (the WIRE key - core.GameSpec.ID)
		// containing a path separator is rejected as "game_id", not the old
		// "id" that named no member of the request body at all.
		{"game_id has a path separator", `{"source_id":"nexusmods","identifier":"a","name":"A","game_id":"a/b","install_path":` + jsonString(install) + `}`, "game_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newGamesServer(t)
			rec := doAPI(s, http.MethodPost, "/api/v1/games", tt.body)
			require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

			var env struct {
				Error   string             `json:"error"`
				Details core.GameSpecError `json:"details"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.Equal(t, tt.field, env.Details.Field)
			assert.NotEmpty(t, env.Details.Reason)
		})
	}
}

// TestAPIGameAdd_UnregisteredSourceIs400 pins #333 Important #1: a
// source_id no registered source claims is a 400 naming that field, and no
// games.yaml row is written for it.
func TestAPIGameAdd_UnregisteredSourceIs400(t *testing.T) {
	s := newGamesServer(t)
	body := `{"source_id":"no-such-source","identifier":"acme","name":"Acme","install_path":` + jsonString(t.TempDir()) + `}`

	rec := doAPI(s, http.MethodPost, "/api/v1/games", body)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	var env struct {
		Error   string             `json:"error"`
		Details core.GameSpecError `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, "source_id", env.Details.Field)

	games, err := s.svc.ListGameEntries(t.Context())
	require.NoError(t, err)
	assert.Empty(t, games, "no games.yaml row for a game whose source was refused")
}

// TestAPIGameAdd_DuplicateIs409 pins the collision status: neither bad
// input nor a missing thing, but a clash with state the caller could not
// have known about - the same classification the profile routes give.
func TestAPIGameAdd_DuplicateIs409(t *testing.T) {
	s := newGamesServer(t)
	body := `{"source_id":"nexusmods","identifier":"acme","name":"Acme","install_path":` + jsonString(t.TempDir()) + `}`

	require.Equal(t, http.StatusOK, doAPI(s, http.MethodPost, "/api/v1/games", body).Code)
	rec := doAPI(s, http.MethodPost, "/api/v1/games", body)
	assert.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
}

// TestAPIGameAdd_RejectsUnknownMembers pins the strict decode every
// mutation body gets: a typo'd key is a 400, never a silently ignored
// field.
func TestAPIGameAdd_RejectsUnknownMembers(t *testing.T) {
	s := newGamesServer(t)
	rec := doAPI(s, http.MethodPost, "/api/v1/games",
		`{"source_id":"nexusmods","identifier":"a","name":"A","install_path":"/tmp","instal_path":"typo"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAPIGameAdd_RequiresCSRF pins that the add is a state-changing route
// like every other: no token, no write.
func TestAPIGameAdd_RequiresCSRF(t *testing.T) {
	s := newGamesServer(t)
	rec := doAPIWithoutCSRF(s, http.MethodPost, "/api/v1/games",
		`{"source_id":"nexusmods","identifier":"a","name":"A","install_path":"/tmp"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestAPIGamesDetect_ListingIsAWellFormedDocument pins the scan half. The
// sandbox has no Steam library, so the scan finds nothing - which is
// exactly the shape that must still be a document with an empty (never
// null) game list rather than a null or an error.
func TestAPIGamesDetect_ListingIsAWellFormedDocument(t *testing.T) {
	s := newGamesServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/games/detect", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var listing core.GameDetectListing
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listing, json.RejectUnknownMembers(true)))
	assert.NotNil(t, listing.Games)
	assert.Empty(t, listing.Games)
}

// TestAPIGameDetectApply_RefusesABadSelection pins that the selection is
// validated against the machine's own scan: with nothing detected, every
// selector is out of range, and the refusal is the caller's (400) rather
// than a 500.
func TestAPIGameDetectApply_RefusesABadSelection(t *testing.T) {
	s := newGamesServer(t)

	for _, body := range []string{`{"select":[]}`, `{"select":["1"]}`, `{"select":["skyrim-se"]}`} {
		rec := doAPI(s, http.MethodPost, "/api/v1/games/detect", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "body %s -> %s", body, rec.Body.String())
	}
}

// TestAPIGameDetectApply_RequiresCSRF pins the write's guard.
func TestAPIGameDetectApply_RequiresCSRF(t *testing.T) {
	s := newGamesServer(t)
	rec := doAPIWithoutCSRF(s, http.MethodPost, "/api/v1/games/detect", `{"select":["1"]}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestAPIGameSetDefault_WritesTheCoreDocument pins the Setup page's
// default-game affordance (#333, added on top of task A1's wire): the
// response is byte-identical to `lmm game set-default --json`'s own
// core.SettingsResult, so no new golden is needed for it.
func TestAPIGameSetDefault_WritesTheCoreDocument(t *testing.T) {
	s := newGamesServer(t)
	install := t.TempDir()
	require.Equal(t, http.StatusOK, doAPI(s, http.MethodPost, "/api/v1/games",
		`{"source_id":"nexusmods","identifier":"acme","name":"Acme","install_path":`+jsonString(install)+`}`).Code)

	rec := doAPI(s, http.MethodPost, "/api/v1/games/acme/set-default", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	requireEncodesLikeInternal(t, rec.Body.Bytes(), &core.SettingsResult{DefaultGame: "acme"})

	got, err := s.svc.DefaultGame(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "acme", got)
}

// TestAPIGameSetDefault_UnknownGameIs404 pins that a game the caller could
// not have known was gone (a stale row, a typo) is refused rather than
// silently writing an unresolvable default into config.yaml.
func TestAPIGameSetDefault_UnknownGameIs404(t *testing.T) {
	s := newGamesServer(t)
	rec := doAPI(s, http.MethodPost, "/api/v1/games/nope/set-default", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

// TestAPIGameSetDefault_RequiresCSRF pins the state-changing route's own
// gate, like every other write in this file.
func TestAPIGameSetDefault_RequiresCSRF(t *testing.T) {
	s := newGamesServer(t)
	rec := doAPIWithoutCSRF(s, http.MethodPost, "/api/v1/games/acme/set-default", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestAPIGameClearDefault_WritesTheCoreDocument pins the clear half: the
// same document, DefaultGame empty, and unconditional - clearing an
// already-unset default is still a 200, matching the CLI.
func TestAPIGameClearDefault_WritesTheCoreDocument(t *testing.T) {
	s := newGamesServer(t)
	install := t.TempDir()
	require.Equal(t, http.StatusOK, doAPI(s, http.MethodPost, "/api/v1/games",
		`{"source_id":"nexusmods","identifier":"acme","name":"Acme","install_path":`+jsonString(install)+`}`).Code)
	require.Equal(t, http.StatusOK, doAPI(s, http.MethodPost, "/api/v1/games/acme/set-default", "").Code)

	rec := doAPI(s, http.MethodDelete, "/api/v1/games/default", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	requireEncodesLikeInternal(t, rec.Body.Bytes(), &core.SettingsResult{})

	got, err := s.svc.DefaultGame(t.Context())
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestAPIGameClearDefault_RequiresCSRF pins the state-changing route's own
// gate.
func TestAPIGameClearDefault_RequiresCSRF(t *testing.T) {
	s := newGamesServer(t)
	rec := doAPIWithoutCSRF(s, http.MethodDelete, "/api/v1/games/default", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestAPIGamesUnknownSubpath pins that a path under /api/v1/games that no
// route claims still gets the JSON 404 envelope, not net/http's
// text/plain one.
func TestAPIGamesUnknownSubpath(t *testing.T) {
	s := newGamesServer(t)
	rec := doAPI(s, http.MethodGet, "/api/v1/games/nope/whatever", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))
}

// TestAPIGamesIDIsPUTOnly is the same fallback seen from a path that IS
// claimed, just not for this method: since #326 gave /api/v1/games/{id} a
// PUT (the source<->game mapping), a GET of it answers 405 naming PUT
// rather than the generic 404 it used to get when nothing claimed the path
// at all.
func TestAPIGamesIDIsPUTOnly(t *testing.T) {
	s := newGamesServer(t)
	rec := doAPI(s, http.MethodGet, "/api/v1/games/nope", "")
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "PUT", rec.Header().Get("Allow"))
	assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))
}

// jsonString quotes a path for embedding in a request-body literal, so a
// t.TempDir() containing a backslash or a quote cannot produce invalid
// JSON.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// --- #206: unknown installed games ---

// fakeSteamApp fabricates one installed Steam app inside the SANDBOXED
// HOME newGamesServer already established (sandboxEnv), so a detect scan
// in these tests reads a fabricated library and never the host's. It
// returns the install path the scan will report.
func fakeSteamApp(t *testing.T, appID, name, installDir string) string {
	t.Helper()
	t.Setenv("STEAM_ROOT", "")
	steamapps := filepath.Join(os.Getenv("HOME"), ".steam", "steam", "steamapps")
	install := filepath.Join(steamapps, "common", installDir)
	require.NoError(t, os.MkdirAll(install, 0o755))
	acf := "\n\"AppState\"\n{\n\t\"appid\"\t\t\"" + appID + "\"\n\t\"name\"\t\t\"" + name +
		"\"\n\t\"installdir\"\t\t\"" + installDir + "\"\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "appmanifest_"+appID+".acf"), []byte(acf), 0o644))
	return install
}

// TestAPIGamesDetect_AllIncludesUnknownRows pins ?all=1: the same document,
// widened with the rows no known-games entry covers. Those carry no
// `known` member at all (never a literal false) and index 0 - they are
// listed, not selectable - while the known rows keep the numbering they
// have without the parameter, so one index means one game either way.
func TestAPIGamesDetect_AllIncludesUnknownRows(t *testing.T) {
	s := newGamesServer(t)
	fakeSteamApp(t, "489830", "Skyrim Special Edition", "Skyrim Special Edition")
	unknownInstall := fakeSteamApp(t, "526870", "Satisfactory", "Satisfactory")

	narrow := decodeDetectListing(t, s, "/api/v1/games/detect")
	require.Len(t, narrow.Games, 1)
	assert.Equal(t, "skyrim-se", narrow.Games[0].Slug)
	assert.True(t, narrow.Games[0].Known)
	assert.Equal(t, 1, narrow.Games[0].Index)

	wide := decodeDetectListing(t, s, "/api/v1/games/detect?all=1")
	require.Len(t, wide.Games, 2)
	byID := map[string]core.GameDetectEntry{}
	for _, row := range wide.Games {
		byID[row.SteamAppID] = row
	}
	assert.Equal(t, 1, byID["489830"].Index, "known numbering must not shift when unknown rows join")
	unknown := byID["526870"]
	assert.False(t, unknown.Known)
	assert.Equal(t, 0, unknown.Index, "an unknown row is listed, not selectable")
	assert.Equal(t, "satisfactory", unknown.Slug)
	assert.Equal(t, unknownInstall, unknown.InstallPath)
	assert.Empty(t, unknown.ModPath)
	assert.Empty(t, unknown.Sources)
}

// TestAPIGamesDetect_AllRejectsAMistypedFlag: only "1"/"true" widen the
// scan, so a mistyped parameter reads as "not asked for" rather than
// silently publishing rows the caller never requested.
func TestAPIGamesDetect_AllRejectsAMistypedFlag(t *testing.T) {
	s := newGamesServer(t)
	fakeSteamApp(t, "526870", "Satisfactory", "Satisfactory")

	for _, query := range []string{"", "?all=", "?all=0", "?all=yes"} {
		listing := decodeDetectListing(t, s, "/api/v1/games/detect"+query)
		assert.Emptyf(t, listing.Games, "%q must not widen the scan", query)
	}
	assert.Len(t, decodeDetectListing(t, s, "/api/v1/games/detect?all=true").Games, 1)
}

func decodeDetectListing(t *testing.T, s *Server, path string) core.GameDetectListing {
	t.Helper()
	rec := doAPI(s, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var listing core.GameDetectListing
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listing, json.RejectUnknownMembers(true)))
	return listing
}

// TestAPIGameDetectApply_RefusesAnUnknownRow pins the POST half's
// unchanged semantics: it configures a game from its curated entry, and an
// unknown candidate has none. The refusal is the caller's (400), names the
// row, and points at the route that CAN add it.
func TestAPIGameDetectApply_RefusesAnUnknownRow(t *testing.T) {
	s := newGamesServer(t)
	fakeSteamApp(t, "526870", "Satisfactory", "Satisfactory")

	rec := doAPI(s, http.MethodPost, "/api/v1/games/detect", `{"select":["satisfactory"]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "526870")
	assert.Contains(t, body, "from_steam_app_id")
}

// TestAPIGameAdd_FromSteamAppIDPrefillsAKnownGame is #206's web half: the
// app id alone adds a curated installed game, with the name, install path,
// game id, mod path and source map all supplied by the scan - the SPA
// derives none of them.
func TestAPIGameAdd_FromSteamAppIDPrefillsAKnownGame(t *testing.T) {
	s := newGamesServer(t)
	install := fakeSteamApp(t, "489830", "Skyrim Special Edition", "Skyrim Special Edition")

	rec := doAPI(s, http.MethodPost, "/api/v1/games", `{"from_steam_app_id":"489830"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "skyrim-se", entry.ID)
	assert.Equal(t, "Skyrim Special Edition", entry.Name)
	assert.Equal(t, install, entry.InstallPath)
	assert.Equal(t, filepath.Join(install, "Data"), entry.ModPath)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, entry.SourceIDs)
}

// TestAPIGameAdd_FromSteamAppIDUnknownGameTakesTheSourcePair covers the
// uncurated half, and that the body's other members are overrides: the mod
// path defaults to <install>/mods unless the form corrects it.
func TestAPIGameAdd_FromSteamAppIDUnknownGameTakesTheSourcePair(t *testing.T) {
	s := newGamesServer(t)
	install := fakeSteamApp(t, "526870", "Satisfactory", "Satisfactory")

	rec := doAPI(s, http.MethodPost, "/api/v1/games",
		`{"from_steam_app_id":"526870","source_id":"nexusmods","identifier":"satisfactory"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "satisfactory", entry.ID)
	assert.Equal(t, filepath.Join(install, "mods"), entry.ModPath)
	assert.Equal(t, map[string]string{"nexusmods": "satisfactory"}, entry.SourceIDs)

	// An override wins, field by field.
	other := t.TempDir()
	rec = doAPI(s, http.MethodPost, "/api/v1/games",
		`{"from_steam_app_id":"526870","source_id":"nexusmods","identifier":"satisfactory",`+
			`"game_id":"sf-modded","name":"My SF","mod_path":`+jsonString(other)+`}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "sf-modded", entry.ID)
	assert.Equal(t, "My SF", entry.Name)
	assert.Equal(t, other, entry.ModPath)
}

// TestAPIGameAdd_FromSteamAppIDUnknownAppID is the caller's error (400)
// carrying the field a form marks.
func TestAPIGameAdd_FromSteamAppIDUnknownAppID(t *testing.T) {
	s := newGamesServer(t)
	fakeSteamApp(t, "526870", "Satisfactory", "Satisfactory")

	rec := doAPI(s, http.MethodPost, "/api/v1/games", `{"from_steam_app_id":"999999"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "from_steam_app_id")
}

// fakeSteamWorkshopManifest writes a populated appworkshop_<appID>.acf next
// to the fabricated library fakeSteamApp built, which is what makes
// detection prefill `steamworkshop: <appID>` and stamp the item count
// (#269 Unit 9). items is how many installed entries the manifest declares.
func fakeSteamWorkshopManifest(t *testing.T, appID string, fileIDs ...string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".steam", "steam", "steamapps", "workshop")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := "\"AppWorkshop\"\n{\n\t\"appid\"\t\t\"" + appID + "\"\n\t\"WorkshopItemsInstalled\"\n\t{\n"
	for _, id := range fileIDs {
		body += "\t\t\"" + id + "\"\n\t\t{\n\t\t\t\"manifest\"\t\t\"1122334455667788990\"\n\t\t}\n"
	}
	body += "\t}\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "appworkshop_"+appID+".acf"), []byte(body), 0o644))
}

// TestAPIGamesDetect_ListsWorkshopBearingUncuratedGamesWithoutAll is #368
// on the web's own half: the first-run/Setup detect list asked for the
// NARROW listing, so a game whose only claim to being moddable is the
// thirty Workshop items Steam already downloaded was invisible unless the
// caller thought to pass ?all=1. It is now in the default document, with
// its count and its prefilled source map, while a plain uncurated game
// still needs the parameter.
func TestAPIGamesDetect_ListsWorkshopBearingUncuratedGamesWithoutAll(t *testing.T) {
	s := newGamesServer(t)
	fakeSteamApp(t, "1133870", "Space Engineers 2", "SpaceEngineers2")
	fakeSteamWorkshopManifest(t, "1133870", "3617086610", "3512001122")
	fakeSteamApp(t, "526870", "Satisfactory", "Satisfactory")

	narrow := decodeDetectListing(t, s, "/api/v1/games/detect")
	require.Len(t, narrow.Games, 1, "the Workshop-bearing row is listed by default; the plain uncurated one is not")
	row := narrow.Games[0]
	assert.Equal(t, "1133870", row.SteamAppID)
	assert.Equal(t, 2, row.WorkshopItems)
	assert.False(t, row.Known)
	assert.Equal(t, 0, row.Index, "listed is not selectable")
	assert.Equal(t, map[string]string{"steamworkshop": "1133870"}, row.Sources)

	wide := decodeDetectListing(t, s, "/api/v1/games/detect?all=1")
	assert.Len(t, wide.Games, 2, "--all still adds everything else")
}

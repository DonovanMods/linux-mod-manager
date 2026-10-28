package nexusmods

// #540: a mod NexusMods no longer has failed the whole update check instead
// of landing in UpdateCheckReport.catalog_missing (#539's seam).
//
// testdata/v1-mod-status/ holds the v1 REST API's own answers, recorded
// 2026-10-08 from api.nexusmods.com against skyrimspecialedition (game.json
// is trimmed of its category list; every body is the API's own, only
// pretty-printed):
//
//   - mod-404.json         GET /v1/games/{game}/mods/4.json         -> 404
//   - mod-removed.json     GET .../mods/17.json                     -> 200, status "removed", available false
//   - mod-wastebinned.json GET .../mods/20.json                     -> 200, status "wastebinned", available false
//   - mod-hidden.json      GET .../mods/9.json                      -> 200, status "hidden", available false
//   - mod-not-published.json GET .../mods/18.json                   -> 200, status "not_published", available false
//   - mod-published.json   GET .../mods/1.json                      -> 200, status "published"
//   - files-404.json       GET .../mods/4/files.json                -> 404
//   - files-unavailable.json GET .../mods/17/files.json             -> 403 (the same for 9, 18 and 20)
//   - game.json            GET /v1/games/skyrimspecialedition.json  -> 200
//   - game-404.json        GET /v1/games/nosuchgamedomain.json      -> 404
//   - mod-in-unknown-game-404.json GET /v1/games/nosuchgamedomain/mods/17.json -> 404
//
// The last one is why a mod 404 alone is not proof the mod is gone: an
// unknown game domain answers the same status, so the game is confirmed
// before the mod is reported missing.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorded is one recorded v1 answer: its status and its fixture body.
type recorded struct {
	status  int
	fixture string
}

// statusServer answers each path in routes with its recorded response,
// anything else with a 500 (a test that reaches one fails on the error it
// gets back), and counts the requests made to each path.
func statusServer(t *testing.T, routes map[string]recorded) (*NexusMods, map[string]*atomic.Int32) {
	t.Helper()
	hits := make(map[string]*atomic.Int32, len(routes))
	for p := range routes {
		hits[p] = new(atomic.Int32)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		hits[r.URL.Path].Add(1)
		var body []byte
		if strings.HasPrefix(rec.fixture, "{") {
			body = []byte(rec.fixture)
		} else {
			var err error
			body, err = os.ReadFile(filepath.Join("testdata", "v1-mod-status", rec.fixture))
			if err != nil {
				t.Errorf("reading fixture: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	nm := New(srv.Client(), "testapikey")
	nm.client.SetBaseURL(srv.URL)
	return nm, hits
}

const sse = "/v1/games/skyrimspecialedition"

var gameOK = recorded{http.StatusOK, "game.json"}

func TestNexusMods_GetMod_404IsErrModNotFound(t *testing.T) {
	nm, _ := statusServer(t, map[string]recorded{
		sse + ".json":        gameOK,
		sse + "/mods/4.json": {http.StatusNotFound, "mod-404.json"},
	})

	_, err := nm.GetMod(context.Background(), "skyrimspecialedition", "4")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	assert.Contains(t, err.Error(), "mod 4", "the error names the mod")
}

// A 404 for a game domain NexusMods does not know is the same status, and
// says nothing about the mod: a misconfigured game must not report every one
// of its mods as gone.
func TestNexusMods_GetMod_404ForAnUnknownGameIsNotErrModNotFound(t *testing.T) {
	nm, _ := statusServer(t, map[string]recorded{
		"/v1/games/nosuchgamedomain.json":         {http.StatusNotFound, "game-404.json"},
		"/v1/games/nosuchgamedomain/mods/17.json": {http.StatusNotFound, "mod-in-unknown-game-404.json"},
	})

	_, err := nm.GetMod(context.Background(), "nosuchgamedomain", "17")
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrModNotFound)
	assert.Contains(t, err.Error(), "nosuchgamedomain")
}

// When the game lookup itself fails the 404 is unconfirmed: a failed read
// never marks a mod missing.
func TestNexusMods_GetMod_404WithAFailedGameLookupIsNotErrModNotFound(t *testing.T) {
	nm, _ := statusServer(t, map[string]recorded{
		sse + ".json":        {http.StatusBadGateway, `{"code":502,"message":"Bad Gateway"}`},
		sse + "/mods/4.json": {http.StatusNotFound, "mod-404.json"},
	})

	_, err := nm.GetMod(context.Background(), "skyrimspecialedition", "4")
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrModNotFound)
}

// The API's own permanent states: removed, and moved to the wastebin by its
// author. Both answer 200 with almost every field blanked.
func TestNexusMods_GetMod_RemovedStatusesAreErrModNotFound(t *testing.T) {
	for _, tc := range []struct{ id, fixture, status string }{
		{"17", "mod-removed.json", "removed"},
		{"20", "mod-wastebinned.json", "wastebinned"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			nm, _ := statusServer(t, map[string]recorded{
				sse + "/mods/" + tc.id + ".json": {http.StatusOK, tc.fixture},
			})

			_, err := nm.GetMod(context.Background(), "skyrimspecialedition", tc.id)
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrModNotFound)
			assert.Contains(t, err.Error(), tc.status, "the error carries the API's own status")
		})
	}
}

// Hidden, unpublished or under moderation is a state the mod can come back
// from, so it is not gone: GetMod still answers the mod.
func TestNexusMods_GetMod_TemporaryStatusesAreNotErrModNotFound(t *testing.T) {
	for _, tc := range []struct{ id, fixture string }{
		{"9", "mod-hidden.json"},
		{"18", "mod-not-published.json"},
		{"77", `{"mod_id":77,"domain_name":"skyrimspecialedition","version":"1.0","status":"under_moderation","available":false}`},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			nm, _ := statusServer(t, map[string]recorded{
				sse + "/mods/" + tc.id + ".json": {http.StatusOK, tc.fixture},
			})

			mod, err := nm.GetMod(context.Background(), "skyrimspecialedition", tc.id)
			require.NoError(t, err)
			assert.Equal(t, tc.id, mod.ID)
		})
	}
}

// Any other failure keeps its status and body, and is not ErrModNotFound.
func TestNexusMods_GetMod_500IsNotErrModNotFound(t *testing.T) {
	nm, _ := statusServer(t, map[string]recorded{
		sse + "/mods/4.json": {http.StatusInternalServerError, `{"code":500,"message":"backend unavailable"}`},
	})

	_, err := nm.GetMod(context.Background(), "skyrimspecialedition", "4")
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrModNotFound)
	assert.Contains(t, err.Error(), "API error (status 500)")
	assert.Contains(t, err.Error(), "backend unavailable")
}

func TestNexusMods_GetModFiles_404IsErrModNotFound(t *testing.T) {
	nm, _ := statusServer(t, map[string]recorded{
		sse + ".json":              gameOK,
		sse + "/mods/4/files.json": {http.StatusNotFound, "files-404.json"},
	})

	_, err := nm.GetModFiles(context.Background(), &domain.Mod{ID: "4", GameID: "skyrimspecialedition"})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

// One check over a gone mod of every kind, a temporarily unavailable one, a
// failing one and a live one with an update: the live one's update is
// returned, each gone mod is a *source.ModNotFoundError naming its id - so
// SplitModNotFound, the seam core's update check reads, files exactly those
// under catalog_missing - and everything else stays a failure.
func TestNexusMods_CheckUpdates_GoneModsAreTypedPerMod(t *testing.T) {
	nm, hits := statusServer(t, map[string]recorded{
		sse + ".json":               gameOK,
		sse + "/mods/4.json":        {http.StatusNotFound, "mod-404.json"},
		sse + "/mods/17.json":       {http.StatusOK, "mod-removed.json"},
		sse + "/mods/20.json":       {http.StatusOK, "mod-wastebinned.json"},
		sse + "/mods/9.json":        {http.StatusOK, "mod-hidden.json"},
		sse + "/mods/9/files.json":  {http.StatusForbidden, `{"code":403,"message":"Mod not available: 9"}`},
		sse + "/mods/66.json":       {http.StatusInternalServerError, `{"code":500,"message":"backend unavailable"}`},
		sse + "/mods/1.json":        {http.StatusOK, "mod-published.json"},
		sse + "/mods/1/files.json":  {http.StatusOK, `{"files":[{"file_id":100,"version":"2.8","is_primary":true}],"file_updates":[]}`},
		sse + "/mods/17/files.json": {http.StatusForbidden, "files-unavailable.json"},
		sse + "/mods/20/files.json": {http.StatusForbidden, `{"code":403,"message":"Mod not available: 20"}`},
	})

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "4", Name: "Deleted", Version: "1.0", GameID: "skyrimspecialedition"}},
		{Mod: domain.Mod{ID: "17", Name: "Removed", Version: "1.0", GameID: "skyrimspecialedition"}},
		{Mod: domain.Mod{ID: "9", Name: "Hidden", Version: "1.0", GameID: "skyrimspecialedition"}},
		{Mod: domain.Mod{ID: "1", Name: "Live", Version: "2.0", GameID: "skyrimspecialedition"}},
		{Mod: domain.Mod{ID: "66", Name: "Flaky", Version: "1.0", GameID: "skyrimspecialedition"}},
		{Mod: domain.Mod{ID: "20", Name: "Binned", Version: "1.0", GameID: "skyrimspecialedition"}},
	}

	updates, err := nm.CheckUpdates(context.Background(), installed)
	require.Len(t, updates, 1, "the live mod's update survives the gone ones")
	assert.Equal(t, "1", updates[0].InstalledMod.ID)
	assert.Equal(t, "2.8", updates[0].NewVersion)
	require.Error(t, err)

	missing, rest := source.SplitModNotFound(err)
	var ids []string
	for _, nf := range missing {
		ids = append(ids, nf.ModID)
		assert.Contains(t, nf.Error(), nameOf(installed, nf.ModID), "each miss names its mod")
	}
	assert.ElementsMatch(t, []string{"4", "17", "20"}, ids, "exactly the gone mods are typed")

	require.Error(t, rest, "the hidden and failing mods are still failures")
	assert.NotErrorIs(t, rest, domain.ErrModNotFound)
	for _, want := range []string{"Hidden", "Flaky", "API error (status 500)"} {
		assert.Contains(t, rest.Error(), want)
	}
	for _, gone := range []string{"Deleted", "Removed", "Binned"} {
		assert.NotContains(t, rest.Error(), gone)
	}

	// A gone mod's files endpoint answers 403; it is never asked.
	assert.Zero(t, hits[sse+"/mods/17/files.json"].Load())
	assert.Zero(t, hits[sse+"/mods/20/files.json"].Load())
}

// Only mods missing: SplitModNotFound leaves nothing, so core reports the
// check as succeeded with the mod under catalog_missing.
func TestNexusMods_CheckUpdates_OnlyGoneModsLeaveNoOtherError(t *testing.T) {
	nm, hits := statusServer(t, map[string]recorded{
		sse + ".json":        gameOK,
		sse + "/mods/4.json": {http.StatusNotFound, "mod-404.json"},
		sse + "/mods/5.json": {http.StatusNotFound, `{"error":"Mod ID 5 not found for skyrimspecialedition"}`},
	})

	updates, err := nm.CheckUpdates(context.Background(), []domain.InstalledMod{
		{Mod: domain.Mod{ID: "4", Name: "Deleted", Version: "1.0", GameID: "skyrimspecialedition"}},
		{Mod: domain.Mod{ID: "5", Name: "Also Deleted", Version: "1.0", GameID: "skyrimspecialedition"}},
	})
	assert.Empty(t, updates)
	missing, rest := source.SplitModNotFound(err)
	assert.Len(t, missing, 2)
	assert.NoError(t, rest)
	assert.Equal(t, int32(1), hits[sse+".json"].Load(), "the game is confirmed once, not once per mod")
}

// An unknown game domain is a configuration problem, not N missing mods.
func TestNexusMods_CheckUpdates_UnknownGameIsNotCatalogMissing(t *testing.T) {
	nm, _ := statusServer(t, map[string]recorded{
		"/v1/games/nosuchgamedomain.json":         {http.StatusNotFound, "game-404.json"},
		"/v1/games/nosuchgamedomain/mods/17.json": {http.StatusNotFound, "mod-in-unknown-game-404.json"},
	})

	_, err := nm.CheckUpdates(context.Background(), []domain.InstalledMod{
		{Mod: domain.Mod{ID: "17", Name: "Some Mod", Version: "1.0", GameID: "nosuchgamedomain"}},
	})
	missing, rest := source.SplitModNotFound(err)
	assert.Empty(t, missing)
	require.Error(t, rest)
	assert.NotErrorIs(t, rest, domain.ErrModNotFound)
}

func nameOf(installed []domain.InstalledMod, id string) string {
	for _, m := range installed {
		if m.ID == id {
			return m.Name
		}
	}
	return ""
}

// A mod removed between the check's two reads: its files endpoint's 404 is
// the same confirmed miss as the mod endpoint's.
func TestNexusMods_CheckUpdates_FilesEndpoint404IsTypedPerMod(t *testing.T) {
	nm, _ := statusServer(t, map[string]recorded{
		sse + ".json":              gameOK,
		sse + "/mods/1.json":       {http.StatusOK, "mod-published.json"},
		sse + "/mods/1/files.json": {http.StatusNotFound, `{"code":404,"message":"No Mod Found: 1"}`},
	})

	_, err := nm.CheckUpdates(context.Background(), []domain.InstalledMod{
		{Mod: domain.Mod{ID: "1", Name: "Just Removed", Version: "2.0", GameID: "skyrimspecialedition"}},
	})
	missing, rest := source.SplitModNotFound(err)
	require.Len(t, missing, 1)
	assert.Equal(t, "1", missing[0].ModID)
	assert.NoError(t, rest)
}

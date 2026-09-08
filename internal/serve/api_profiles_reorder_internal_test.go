package serve

// POST /api/v1/profiles/{name}/reorder and GET /api/v1/conflicts?order=
// (#332): the reorder modal's commit and its live preview. Both drive the
// profiles fixture, whose active profile really holds two ordered mods.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reorderTarget is the reorder route for the fixture's active profile.
func reorderTarget(game *domain.Game) string {
	return scoped("/api/v1/profiles/"+activeProfile+"/reorder", game)
}

// decodeProfileResult strict-decodes a reorder/CRUD response into the core
// document those routes promise.
func decodeProfileResult(t *testing.T, raw []byte) core.ProfileResult {
	t.Helper()
	var got core.ProfileResult
	require.NoError(t, json.Unmarshal(raw, &got, json.RejectUnknownMembers(true)),
		"the response must decode into core.ProfileResult with no unknown members")
	return got
}

// seedSharedConflict makes the fixture's two active mods contend for one
// game path: each gains the SAME extra file in its cache entry, and the
// profile is redeployed so deployed_files records a real owner (a conflict
// with no recorded owner is skipped by the query). p3 is last in the saved
// order, so it deploys last and owns the path.
func seedSharedConflict(t *testing.T, s *Server, game *domain.Game) {
	t.Helper()
	for _, modID := range []string{"p1", "p3"} {
		require.NoError(t, s.svc.GetGameCache(game).Store(
			game.ID, fixtureSourceID, modID, "1.0", sharedConflictPath, []byte(modID+" shared")))
	}
	deployFixtureProfile(t, s, game)
}

// sharedConflictPath is the one game path both fixture mods provide.
const sharedConflictPath = "Mods/shared.pak"

// TestAPIProfileReorder_PersistsTheOrderAndAnswersTheProfileDocument is the
// headline: the load order really moves on disk, and the response is the
// re-read profile - what was persisted, not what was asked for.
func TestAPIProfileReorder_PersistsTheOrderAndAnswersTheProfileDocument(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, reorderTarget(game), `{"ids":["p3","p1"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := decodeProfileResult(t, rec.Body.Bytes())
	require.Len(t, got.Profile.Mods, 2)
	assert.Equal(t, "p3", got.Profile.Mods[0].ModID)
	assert.Equal(t, "p1", got.Profile.Mods[1].ModID)

	stored, err := svc.NewProfileManager().Get(t.Context(), game.ID, activeProfile)
	require.NoError(t, err)
	assert.Equal(t, "p3", stored.Mods[0].ModID, "the reorder must be persisted, not just reported")
	assert.Equal(t, got.Profile.Mods, stored.Mods, "the document must be the profile as it now stands")
}

// TestAPIProfileReorder_PartialOrderKeepsTheRestInPlace pins
// ResolveReorder's own rule reaching the wire: naming one mod moves it
// first and leaves everything else in its existing relative order.
func TestAPIProfileReorder_PartialOrderKeepsTheRestInPlace(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, reorderTarget(game), `{"ids":["p3"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := decodeProfileResult(t, rec.Body.Bytes())
	require.Len(t, got.Profile.Mods, 2)
	assert.Equal(t, "p3", got.Profile.Mods[0].ModID)
	assert.Equal(t, "p1", got.Profile.Mods[1].ModID)
}

// TestAPIProfileReorder_BadInputAnswersTheEnvelope covers every refusal the
// route owns: an id no profile mod matches, a repeated id, an empty list, a
// stray member, and a profile that does not exist.
func TestAPIProfileReorder_BadInputAnswersTheEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		target func(*domain.Game) string
		body   string
		status int
	}{
		{"unknown mod", reorderTarget, `{"ids":["p1","nope"]}`, http.StatusBadRequest},
		{"duplicate mod", reorderTarget, `{"ids":["p1","p1"]}`, http.StatusBadRequest},
		{"empty list", reorderTarget, `{"ids":[]}`, http.StatusBadRequest},
		{"empty id", reorderTarget, `{"ids":[""]}`, http.StatusBadRequest},
		{"no body", reorderTarget, ``, http.StatusBadRequest},
		{"unknown member", reorderTarget, `{"ids":["p1"],"nope":1}`, http.StatusBadRequest},
		{
			"unknown profile",
			func(g *domain.Game) string { return scoped("/api/v1/profiles/ghost/reorder", g) },
			`{"ids":["p1"]}`, http.StatusNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, game := newProfilesFixtureServer(t)
			rec := doAPI(s, http.MethodPost, tc.target(game), tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())

			var env apiErrorEnvelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.NotEmpty(t, env.Error, "every refusal carries the JSON envelope")
		})
	}
}

// TestAPIProfileReorder_UnknownGameAnswers404 pins the selection half: the
// profile is in the path, but the game still comes from ?game=.
func TestAPIProfileReorder_UnknownGameAnswers404(t *testing.T) {
	s, _, _ := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/profiles/"+activeProfile+"/reorder?game=ghost", `{"ids":["p1"]}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestAPIProfileReorder_RefusesWithoutCSRF: a mutation entry point without
// the token is refused before it can write anything.
func TestAPIProfileReorder_RefusesWithoutCSRF(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := doAPIWithoutCSRF(s, http.MethodPost, reorderTarget(game), `{"ids":["p3","p1"]}`)
	require.Equal(t, http.StatusForbidden, rec.Code)

	stored, err := svc.NewProfileManager().Get(t.Context(), game.ID, activeProfile)
	require.NoError(t, err)
	assert.Equal(t, "p1", stored.Mods[0].ModID, "a refused request must not reorder anything")
}

// TestAPIConflicts_OrderPreviewMatchesTheRealReorder is the preview
// contract Task B renders against: the document ?order= returns is exactly
// the document the same reorder, really committed, produces afterwards.
func TestAPIConflicts_OrderPreviewMatchesTheRealReorder(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)
	seedSharedConflict(t, s, game)

	preview := doAPI(s, http.MethodGet, scoped("/api/v1/conflicts?order=p3,p1", game), "")
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())

	var previewed core.ConflictReport
	require.NoError(t, json.Unmarshal(preview.Body.Bytes(), &previewed, json.RejectUnknownMembers(true)))
	require.Len(t, previewed.Conflicts, 1)
	assert.Equal(t, "fake:p1", previewed.Conflicts[0].LoadOrderWinner.Key,
		"the proposed order must move the winner to the mod it puts last")

	// Committing the very same order must produce the very same document.
	rec := doAPI(s, http.MethodPost, reorderTarget(game), `{"ids":["p3","p1"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	after := doAPI(s, http.MethodGet, scoped("/api/v1/conflicts", game), "")
	require.Equal(t, http.StatusOK, after.Code, after.Body.String())
	assert.Equal(t, preview.Body.String(), after.Body.String(),
		"the preview must be byte-identical to the committed report")
}

// TestAPIConflicts_NoOrderParamIsTheSavedOrder: the param is additive -
// leaving it off is the endpoint's original behaviour.
func TestAPIConflicts_NoOrderParamIsTheSavedOrder(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)
	seedSharedConflict(t, s, game)

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/conflicts", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var report core.ConflictReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report, json.RejectUnknownMembers(true)))
	require.Len(t, report.Conflicts, 1)
	assert.Equal(t, "fake:p3", report.Conflicts[0].LoadOrderWinner.Key,
		"p3 is last in the saved order, so it wins")
}

// TestAPIConflicts_BadOrderParamAnswersTheEnvelope: the preview refuses the
// same input the commit refuses, with the same status.
func TestAPIConflicts_BadOrderParamAnswersTheEnvelope(t *testing.T) {
	for _, order := range []string{"nope", "p1,,p3"} {
		t.Run(order, func(t *testing.T) {
			s, _, game := newProfilesFixtureServer(t)
			rec := doAPI(s, http.MethodGet, scoped("/api/v1/conflicts?order="+order, game), "")
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

			var env apiErrorEnvelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.NotEmpty(t, env.Error)
		})
	}
}

package serve

// POST /api/v1/conflicts/mark and /unmark (#668): the Conflicts card's
// "Mark as intended" and "Unmark". Asserted on the end state - the saved
// profile's known_conflicts and GET /api/v1/conflicts' known flag.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func conflictsKnownFlag(t *testing.T, s *Server, game *domain.Game, order string) bool {
	t.Helper()
	target := "/api/v1/conflicts"
	if order != "" {
		target += "?order=" + order
	}
	rec := doAPI(s, http.MethodGet, scoped(target, game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.ConflictReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report, json.RejectUnknownMembers(true)))
	require.Len(t, report.Conflicts, 1)
	return report.Conflicts[0].Known
}

func decodeKnownConflictsResult(t *testing.T, raw []byte) core.KnownConflictsResult {
	t.Helper()
	var got core.KnownConflictsResult
	require.NoError(t, json.Unmarshal(raw, &got, json.RejectUnknownMembers(true)))
	return got
}

func TestAPIConflictsMark_MarksThePairAndTheReportSaysKnown(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	seedSharedConflict(t, s, game)
	assert.False(t, conflictsKnownFlag(t, s, game, ""))

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/conflicts/mark", game), `{"pairs":[{"winner":"fake:p3","loser":"fake:p1"}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	want := domain.KnownConflicts{{Winner: "fake:p3", Loser: "fake:p1", Origin: domain.KnownConflictUser}}
	got := decodeKnownConflictsResult(t, rec.Body.Bytes())
	assert.Equal(t, want, got.Added)
	assert.Equal(t, activeProfile, got.Profile)

	stored, err := svc.NewProfileManager().Get(t.Context(), game.ID, activeProfile)
	require.NoError(t, err)
	assert.Equal(t, want, stored.KnownConflicts, "the mark is persisted")

	assert.True(t, conflictsKnownFlag(t, s, game, ""))
	assert.False(t, conflictsKnownFlag(t, s, game, "p3,p1"), "the reorder preview flips it back to a conflict")

	rec = doAPI(s, http.MethodPost, scoped("/api/v1/conflicts/unmark", game), `{"pairs":[{"winner":"p1","loser":"p3"}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got = decodeKnownConflictsResult(t, rec.Body.Bytes())
	assert.Equal(t, want, got.Removed)
	assert.Equal(t, domain.KnownConflicts{}, got.KnownConflicts)
	assert.False(t, conflictsKnownFlag(t, s, game, ""))
}

func TestAPIConflictsMark_AllCurrent(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)
	seedSharedConflict(t, s, game)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/conflicts/mark", game), `{"all_current":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeKnownConflictsResult(t, rec.Body.Bytes())
	assert.Equal(t, domain.KnownConflicts{{Winner: "fake:p3", Loser: "fake:p1", Origin: domain.KnownConflictUser}}, got.Added)
	assert.True(t, conflictsKnownFlag(t, s, game, ""))
}

func TestAPIConflictsMark_BadInputAnswersTheEnvelope(t *testing.T) {
	tests := []struct {
		name, route, body, profile string
		status                     int
	}{
		{"unknown mod", "mark", `{"pairs":[{"winner":"p3","loser":"nope"}]}`, "", http.StatusBadRequest},
		{"same mod twice", "mark", `{"pairs":[{"winner":"p3","loser":"fake:p3"}]}`, "", http.StatusBadRequest},
		{"no pairs", "mark", `{}`, "", http.StatusBadRequest},
		{"no body", "unmark", ``, "", http.StatusBadRequest},
		{"pairs and all_current", "mark", `{"all_current":true,"pairs":[{"winner":"p3","loser":"p1"}]}`, "", http.StatusBadRequest},
		{"unmark all_current", "unmark", `{"all_current":true}`, "", http.StatusBadRequest},
		{"unknown member", "mark", `{"pairs":[],"nope":1}`, "", http.StatusBadRequest},
		{"unknown profile", "mark", `{"pairs":[{"winner":"p3","loser":"p1"}]}`, "ghost", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, svc, game := newProfilesFixtureServer(t)
			seedSharedConflict(t, s, game)
			target := scoped("/api/v1/conflicts/"+tc.route, game)
			if tc.profile != "" {
				target = scopedTo("/api/v1/conflicts/"+tc.route, game, tc.profile)
			}
			rec := doAPI(s, http.MethodPost, target, tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			var env apiErrorEnvelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.NotEmpty(t, env.Error, "every refusal carries the JSON envelope")

			stored, err := svc.NewProfileManager().Get(t.Context(), game.ID, activeProfile)
			require.NoError(t, err)
			assert.Empty(t, stored.KnownConflicts, "a refusal writes nothing")
		})
	}
}

package serve

// #461: POST /api/v1/plans/{kind} on a game whose adapter core refuses
// answered 500 - a server fault - for a known, handled state that carries
// its own remedy. It answers 409 with the typed refusal now, exactly as
// GET /api/v1/conflicts does since #455, so the SPA's confirm surface can
// render the sentence as what it is: the game's configuration refusing
// the flow until the user fixes it.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIPlans_ARefusedAdapterIsA409CarryingTheRefusal(t *testing.T) {
	modBody := `{"source_id":"` + fixtureSourceID + `","mod_id":"m1"}`
	cases := []struct {
		kind string
		// body builds the plan request; it may seed state first.
		body func(t *testing.T, s *Server, game *domain.Game) string
	}{
		{"deploy", func(*testing.T, *Server, *domain.Game) string { return "" }},
		{"install", func(*testing.T, *Server, *domain.Game) string { return modBody }},
		{"updates", func(*testing.T, *Server, *domain.Game) string {
			return `{"mods":["` + fixtureSourceID + `:m1"]}`
		}},
		{"profile_apply", func(*testing.T, *Server, *domain.Game) string { return `{"profile":"default"}` }},
		{"profile_sync", func(*testing.T, *Server, *domain.Game) string { return `{"profile":"default"}` }},
		{"mod_relink", func(*testing.T, *Server, *domain.Game) string { return modBody }},
		{"adopt", func(*testing.T, *Server, *domain.Game) string { return "" }},
		{"import_archive", func(t *testing.T, s *Server, _ *domain.Game) string {
			return archivePlanBody(uploadArchive(t, s, "MyMod-1.2.zip", map[string]string{"mymod.esp": "mod bytes"}))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			s, svc, game := newFlowFixtureServer(t)
			body := tc.body(t, s, game)
			game.Adapter = "no-such-adapter"
			require.NoError(t, svc.SaveGame(t.Context(), game))

			rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/"+tc.kind, game), body)
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			var envelope struct {
				Error   string `json:"error"`
				Details struct {
					GameID  string `json:"game_id"`
					Adapter string `json:"adapter"`
				} `json:"details"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
			assert.Contains(t, envelope.Error, `unknown adapter "no-such-adapter"`)
			assert.Equal(t, game.ID, envelope.Details.GameID)
			assert.Equal(t, "no-such-adapter", envelope.Details.Adapter)
		})
	}
}

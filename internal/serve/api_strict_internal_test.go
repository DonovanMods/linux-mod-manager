package serve

// Two rules every mutation entry point shares, held per route and per plan
// kind in one place: a request body is strictly decoded, and a plan is
// redeemed at most once.

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPI_MutationBodiesRejectUnknownMembers pins the strict decode every
// mutation body gets: a typo'd key is a 400, never a silently ignored field
// - which for verify_fix would mean an unfiltered repair of the whole
// profile.
func TestAPI_MutationBodiesRejectUnknownMembers(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T) (s *Server, method, target, body string)
	}{
		{"verify_fix plan", func(t *testing.T) (*Server, string, string, string) {
			s, _, game := newFlowFixtureServer(t)
			return s, http.MethodPost, scoped("/api/v1/plans/verify_fix", game), `{"mod_filtr":"m1"}`
		}},
		{"skip-update", func(t *testing.T) (*Server, string, string, string) {
			s, _, game := newUpdatesFixtureServer(t)
			return s, http.MethodPost, skipUpdateRoute(game, "skip-update", "u1"), `{"verison":"1.5"}`
		}},
		{"game sources", func(t *testing.T) (*Server, string, string, string) {
			return newGameSourcesServer(t), http.MethodPut, "/api/v1/games/skyrim-se",
				`{"sources":{"nexusmods":"x"},"source":{"typo":"y"}}`
		}},
		{"mod convert", func(t *testing.T) (*Server, string, string, string) {
			s, _, game := newConvertServer(t, []string{"pak"})
			return s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/convert", game), `{"enabld":false}`
		}},
		{"game add", func(t *testing.T) (*Server, string, string, string) {
			return newGamesServer(t), http.MethodPost, "/api/v1/games",
				`{"source_id":"nexusmods","identifier":"a","name":"A","install_path":"/tmp","instal_path":"typo"}`
		}},
		{"source index refresh", func(t *testing.T) (*Server, string, string, string) {
			s, _, game, _ := newIndexFixtureServer(t)
			return s, http.MethodPost, indexPath(game.ID), `{"refersh":true}`
		}},
		{"snapshot create", func(t *testing.T) (*Server, string, string, string) {
			s, _, game := newFlowFixtureServer(t)
			return s, http.MethodPost, scoped("/api/v1/snapshots", game), `{"nam":"typo"}`
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, method, target, body := tc.arrange(t)
			rec := doAPI(s, method, target, body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

// TestFlow_UsedPlanIsRefused is the plan store's single-use rule seen from
// a double submission, per plan kind (deploy's own is
// TestAPIStartJob_PlanIsSingleUse): the first redemption runs, the second
// is a 409. It matters most for snapshot_restore, where replaying the
// handle would re-run a destructive five-stage flow; profile_sync is
// review M2's row - it shares the generic machinery but had nothing
// pinning it.
func TestFlow_UsedPlanIsRefused(t *testing.T) {
	tests := []struct {
		kind    string
		arrange func(t *testing.T, s *Server, game *domain.Game)
		body    string
		options string
		after   func(t *testing.T, s *Server, game *domain.Game)
	}{
		{kind: "mod_relink", body: relinkPlanBody},
		{kind: "purge", arrange: deployFixtureProfile},
		{kind: "profile_sync", body: `{"profile":"default"}`},
		{
			kind: "snapshot_restore",
			arrange: func(t *testing.T, s *Server, game *domain.Game) {
				deployFixtureProfile(t, s, game)
				snapshotFixture(t, s, game.ID, "known-good")
			},
			body:    `{"snapshot":"known-good"}`,
			options: `{"no_safety_snapshot":true}`,
		},
		{
			kind: "uninstall", arrange: deployFixtureProfile, body: uninstallPlanBody,
			// The mod is removed exactly once.
			after: func(t *testing.T, s *Server, game *domain.Game) {
				_, err := s.svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
				require.ErrorIs(t, err, domain.ErrModNotFound)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			s, _, game := newFlowFixtureServer(t)
			if tc.arrange != nil {
				tc.arrange(t, s, game)
			}

			id, _ := planFlow(t, s, game, tc.kind, tc.body)
			j := startFlowJob(t, s, id, tc.options)
			require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

			again := doAPI(s, http.MethodPost, "/api/v1/jobs", `{"plan_id":"`+string(id)+`"}`)
			require.Equal(t, http.StatusConflict, again.Code, again.Body.String())
			if tc.after != nil {
				tc.after(t, s, game)
			}
		})
	}
}

package serve

// #451 F4: `planErrorStatus` (plankinds.go) mapped a moved mod_path -
// *core.ModPathMissingError - to the default 500. It is the same class of
// refusal as the LoaderRequiredError/ErrProfileNotActive cases right above
// it in that switch: the request is well-formed, the game's CURRENT state
// refuses it until the user resolves it (`lmm game edit --mod-path` back to
// where the files are, or a purge), so it belongs at 409, not 500.
//
// This drives POST /api/v1/plans/{kind} for every plan kind #451 finding F3
// covers - deploy and install already asked the moved check through
// currentInstalledSnapshot before this task; profile_apply, switch,
// profile_import, adopt and snapshot_restore gained their own Plan-time
// guard in the F3 commit; updates and rollback ask it the same
// pre-existing way deploy/install do (PlanUpdateBatch/PlanRollback both
// call currentInstalledSnapshot). purge, profile_sync, mod_relink,
// verify_fix, import_archive and workshop_adopt are out of scope for this
// finding (not named by it) and are not exercised here.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// putGameIntoMovedModPathState deploys the flow fixture's one mod for real
// - so deployed_files rows exist under the game's ORIGINAL mod_path - then
// hand-edits games.yaml to point mod_path somewhere else, the way a user
// with an editor would, and has svc read the file again. The new directory
// is created, so the only thing any kind's Plan can refuse over is the
// #451 moved check itself, never adopt's unrelated "mod_path does not
// exist" stat check (modPathStatProblem).
func putGameIntoMovedModPathState(t *testing.T, s *Server, svc *core.Service, game *domain.Game) *domain.Game {
	t.Helper()
	deployFixtureProfile(t, s, game)

	oldPath := game.ModPath
	newPath := oldPath + "-moved"
	require.NoError(t, os.MkdirAll(newPath, 0o755))

	path := filepath.Join(svc.ConfigDir(), "games.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), oldPath)
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(data), oldPath, newPath)), 0o644))

	reloaded, err := svc.ReloadGames()
	require.NoError(t, err)
	require.True(t, reloaded)

	moved, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	return moved
}

// TestFlowModPathMoved_EveryAffectedPlanKindIs409 is table-driven over
// every plan kind whose Plan asks about a moved mod_path (see the file
// doc comment for which, and why the rest are left out).
func TestFlowModPathMoved_EveryAffectedPlanKindIs409(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	ctx := context.Background()

	// rollback's Plan checks for a previous version BEFORE it ever reaches
	// the moved check (core.PlanRollback), so the fixture mod needs one or
	// this case would refuse for an unrelated reason first.
	mod, err := svc.GetInstalledMod(ctx, fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	mod.PreviousVersion = "0.9"
	require.NoError(t, svc.SaveInstalledMod(ctx, mod))

	// switch needs a second profile to name as its target.
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "other")
	require.NoError(t, err)

	// snapshot_restore needs an existing snapshot to name.
	_, err = svc.CreateSnapshot(ctx, game, "default", "known-good")
	require.NoError(t, err)

	moved := putGameIntoMovedModPathState(t, s, svc, game)

	cases := []struct {
		kind, body string
	}{
		{"deploy", ""},
		{"install", `{"source_id":"` + fixtureSourceID + `","mod_id":"m1"}`},
		{"profile_apply", `{"profile":"default"}`},
		{"switch", `{"profile":"other"}`},
		{"profile_import", `{"data":"name: default\ngame_id: g1\nmods: []\n"}`},
		{"adopt", `{"skip_match":true}`},
		{"updates", `{"mods":["` + fixtureSourceID + `:m1"]}`},
		{"rollback", `{"source_id":"` + fixtureSourceID + `","mod_id":"m1"}`},
		{"snapshot_restore", `{"snapshot":"known-good"}`},
	}

	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/"+tc.kind, moved), tc.body)
			require.Equal(t, http.StatusConflict, rec.Code, "kind %s: %s", tc.kind, rec.Body.String())

			var env struct {
				Error   string                   `json:"error"`
				Details core.ModPathMissingError `json:"details"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.NotEmpty(t, env.Details.DeployedUnder, "kind %s: must be the MOVED reason - %s", tc.kind, rec.Body.String())
			assert.Equal(t, game.ID, env.Details.GameID)
		})
	}
}

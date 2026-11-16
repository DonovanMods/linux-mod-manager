package serve

// #638 over /api/v1: the plan documents the web UI renders name the mod a
// shared path passes to, as core computes it - `handoffs` on the plan.

import (
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedSharingMod adds "Mod Two" after the fixture's Mod One in load
// order, shipping the same file, and deploys: Mod Two holds the path.
func seedSharingMod(t *testing.T, s *Server, svc *core.Service, game *domain.Game) {
	t.Helper()
	ctx := t.Context()
	mod := domain.Mod{ID: "m2", SourceID: fixtureSourceID, Name: "Mod Two", Version: "1.0", GameID: game.ID}
	require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version},
		deployFixtureFile, []byte("two")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{Mod: mod, ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true}))
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version}))
	deployFixtureProfile(t, s, game)
	require.Equal(t, "two", deployedContent(t, game, deployFixtureFile))
}

// planHandOffs decodes a plan response's handoffs.
func planHandOffs(t *testing.T, raw []byte) []core.HandOff {
	t.Helper()
	var resp struct {
		Plan struct {
			Handoffs []core.HandOff `json:"handoffs"`
		} `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp), string(raw))
	return resp.Plan.Handoffs
}

var wantFixtureHandOff = core.HandOff{
	Path: deployFixtureFile,
	From: core.HandOffMod{SourceID: fixtureSourceID, ModID: "m2", Name: "Mod Two"},
	To:   core.HandOffMod{SourceID: fixtureSourceID, ModID: "m1", Name: "Mod One"},
}

func TestFlowUninstall_PlanNamesTheHandOff(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedSharingMod(t, s, svc, game)

	id, raw := planFlow(t, s, game, "uninstall", `{"source_id":"`+fixtureSourceID+`","mod_id":"m2"}`)
	assert.Equal(t, []core.HandOff{wantFixtureHandOff}, planHandOffs(t, raw))

	// The job does what the plan said.
	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "pak bytes", deployedContent(t, game, deployFixtureFile), "Mod One holds the path now")
}

func TestFlowProfileApply_PlanNamesTheHandOff(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedSharingMod(t, s, svc, game)
	require.NoError(t, svc.NewProfileManager().SetModDisabled(t.Context(), game.ID, "default", fixtureSourceID, "m2", true))

	_, raw := planFlow(t, s, game, "profile_apply", `{"profile":"default"}`)
	assert.Equal(t, []core.HandOff{wantFixtureHandOff}, planHandOffs(t, raw))
}

// A plan with nothing to hand on carries no handoffs key at all.
func TestFlowUninstall_PlanWithoutHandOffsOmitsTheKey(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)
	_, raw := planFlow(t, s, game, "uninstall", uninstallPlanBody)
	assert.NotContains(t, string(raw), `"handoffs"`)
}

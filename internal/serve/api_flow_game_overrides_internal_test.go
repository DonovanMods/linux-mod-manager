package serve

// #631 through the entry points the SPA drives: a switch plan lists the
// override changes it makes (a stock INI put back), its job puts the game's
// own file back, and an apply plan of an override-only change is not "no
// changes" - its job writes the override.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planOverrides decodes a plan response's plan.overrides and no_changes.
func planOverrides(t *testing.T, raw []byte) ([]core.OverrideChange, bool) {
	t.Helper()
	var resp struct {
		Plan struct {
			Overrides []core.OverrideChange `json:"overrides"`
			NoChanges bool                  `json:"no_changes"`
		} `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	return resp.Plan.Overrides, resp.Plan.NoChanges
}

// setFlowOverrides replaces profile's overrides.
func setFlowOverrides(t *testing.T, svc *core.Service, gameID, profile string, overrides map[string][]byte) {
	t.Helper()
	p, err := svc.NewProfileManager().Get(context.Background(), gameID, profile)
	require.NoError(t, err)
	p.Overrides = overrides
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))
}

func TestFlowGameOverrides_SwitchRestoresAndApplyPlaces(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	ini := filepath.Join(game.InstallPath, "Engine.ini")
	require.NoError(t, os.WriteFile(ini, []byte("stock"), 0o644))
	setFlowOverrides(t, svc, game.ID, "default", map[string][]byte{"Engine.ini": []byte("tweaked")})

	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	require.Equal(t, "tweaked", readFile(t, ini))

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "plain")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "plain",
		domain.ModReference{SourceID: fixtureSourceID, ModID: "m1", Version: "1.0"}))

	id, raw := planFlow(t, s, game, "switch", `{"profile":"plain"}`)
	overrides, _ := planOverrides(t, raw)
	assert.Equal(t, []core.OverrideChange{{Path: "Engine.ini", Root: domain.RootInstallPath, Action: core.OverrideRestore}}, overrides)
	j = startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "stock", readFile(t, ini), "the switch put the game's own file back")

	// The override added to the now-active profile by hand: the apply
	// plan has a change to make, and its job makes it.
	setFlowOverrides(t, svc, game.ID, "plain", map[string][]byte{"Engine.ini": []byte("plain's")})
	id, raw = planFlow(t, s, game, "profile_apply", `{"profile":"plain"}`)
	overrides, noChanges := planOverrides(t, raw)
	assert.False(t, noChanges)
	assert.Equal(t, []core.OverrideChange{{Path: "Engine.ini", Root: domain.RootInstallPath, Action: core.OverridePlace}}, overrides)
	j = startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "plain's", readFile(t, ini))

	_, raw = planFlow(t, s, game, "profile_apply", `{"profile":"plain"}`)
	overrides, noChanges = planOverrides(t, raw)
	assert.True(t, noChanges, "the game matches the profile now")
	assert.Empty(t, overrides)
}

// Owner ruling on #631: a deploy job takes a dropped game-file override
// away too, and the deploy plan the SPA renders lists it.
func TestFlowGameOverrides_DeployRestoresADroppedOverride(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	ini := filepath.Join(game.InstallPath, "Engine.ini")
	require.NoError(t, os.WriteFile(ini, []byte("stock"), 0o644))
	setFlowOverrides(t, svc, game.ID, "default", map[string][]byte{"Engine.ini": []byte("tweaked")})
	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	require.Equal(t, "tweaked", readFile(t, ini))

	setFlowOverrides(t, svc, game.ID, "default", nil)
	id, raw := planFlow(t, s, game, "deploy", `{}`)
	overrides, _ := planOverrides(t, raw)
	assert.Equal(t, []core.OverrideChange{{Path: "Engine.ini", Root: domain.RootInstallPath, Action: core.OverrideRestore}}, overrides)
	j = startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "stock", readFile(t, ini), "the deploy put the game's own file back")
}

package serve

// #633 through the entry points the SPA drives: three mods ship d3d11.dll
// to the game root, over the game's own copy - the ENB (m1), then Early
// (m0), then ReShade (m2) in load order, so ReShade's file is deployed. The
// uninstall job that removes ReShade hands the path to Early in the same
// job: Early's file is there with no deploy, the job's result says so, and
// the conflicts read names Early as the owner. The disable route that turns
// Early off hands it to the ENB, and uninstalling the ENB, the last mod that
// ships it, puts the game's own file back.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedHandOffFixture is gameRootFlowFixture plus Early (m0) and ReShade
// (m2), which ship d3d11.dll too, deployed: load order m1, m0, m2.
func sharedHandOffFixture(t *testing.T) (*Server, *core.Service, *domain.Game, string) {
	t.Helper()
	s, svc, game := gameRootFlowFixture(t)
	ctx := context.Background()
	stock := filepath.Join(game.InstallPath, "d3d11.dll")

	// A third mod also ships it, before ENB in load order, so the path is
	// still a conflict once ReShade is gone.
	for _, m := range []struct{ id, name, content string }{{"m0", "Early", "early d3d11"}, {"m2", "ReShade", "reshade d3d11"}} {
		mod := domain.Mod{ID: m.id, SourceID: fixtureSourceID, Name: m.name, Version: "1.0", GameID: game.ID}
		require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version},
			"d3d11.dll", []byte(m.content)))
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod: mod, ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
		}))
		require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default",
			domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version}))
	}
	// Load order: m1 ("Mod One", the ENB), m0 (Early), m2 (ReShade) - Early sits between.
	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	require.Equal(t, "reshade d3d11", readFile(t, stock), "the load-order winner's file is at the game root")
	return s, svc, game, stock
}

func TestFlowSharedHandOff_RemovingTheWinnerHandsThePathOn(t *testing.T) {
	s, _, game, stock := sharedHandOffFixture(t)

	j := runFlow(t, s, game, "uninstall", `{"source_id":"`+fixtureSourceID+`","mod_id":"m2"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	uresult, ok := j.status().Result.(*core.UninstallResult)
	require.True(t, ok, "%T", j.status().Result)
	assert.True(t, anyHas(uresult.Warnings, "Early's file is deployed there now in place of ReShade's"), "the job's result says who has the path: %q", uresult.Warnings)
	assert.Equal(t, "early d3d11", readFile(t, stock), "the next mod in load order took the path, with no deploy")

	owner := func() string {
		rec := doAPI(s, http.MethodGet, scoped("/api/v1/conflicts", game), "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var doc struct {
			Conflicts []core.ProfileConflict `json:"conflicts"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc), rec.Body.String())
		for _, c := range doc.Conflicts {
			if c.Path == "d3d11.dll" && c.Root == domain.RootInstallPath {
				assert.False(t, c.Stale, "the deployed file is the load-order winner's: %+v", c)
				assert.Equal(t, c.LoadOrderWinner.Key, c.Owner.Key)
				return c.Owner.Key
			}
		}
		return ""
	}
	assert.Equal(t, domain.ModKey(fixtureSourceID, "m0"), owner(), "the conflicts read names the new owner")

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/m0/disable", game), "")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var started struct {
		JobID jobID `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	j = awaitJob(t, s, started.JobID)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	dresult, ok := j.status().Result.(*core.DisableResult)
	require.True(t, ok, "%T", j.status().Result)
	assert.True(t, anyHas(dresult.Warnings, "Mod One's file is deployed there now in place of Early's"), "the disable's result says who has the path: %q", dresult.Warnings)
	assert.Equal(t, "enb d3d11", readFile(t, stock), "m1, the last mod that ships it, has it now")

	j = runFlow(t, s, game, "uninstall", `{"source_id":"`+fixtureSourceID+`","mod_id":"m1"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "the game's own", readFile(t, stock), "with no enabled mod left to ship it, the game's own file is back")
}

func anyHas(msgs []string, want string) bool {
	for _, m := range msgs {
		if strings.Contains(m, want) {
			return true
		}
	}
	return false
}

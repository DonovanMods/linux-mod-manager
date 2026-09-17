package serve

// #462 over /api/v1: every kind that deploys into the game directory is
// refused for a profile that is not the game's active one - 409, naming
// `lmm profile switch`, before anything changes - and the removals
// (uninstall, disable) and a profile import run recorded-only for it.

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// altScoped is scoped for profile alt.
func altScoped(target string, game *domain.Game) string {
	return target + "?" + gameParam + "=" + url.QueryEscape(game.ID) + "&" + profileParam + "=alt"
}

func TestFlowInactiveProfile_DeployingKindsAreRefused(t *testing.T) {
	for _, tc := range []struct{ kind, body string }{
		{"install", installPlanBody("m3")},
		{"updates", updatesBatchBody},
		{"rollback", rollbackPlanBody},
		{"adopt", `{}`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			s, _, game := mixedFlowServer(t)
			rec := doAPI(s, http.MethodPost, altPlanTarget(tc.kind, game), tc.body)
			requireConflict(t, rec)
			assert.FileExists(t, deployedFixturePath(game), "the active profile's deployment is untouched")
		})
	}

	t.Run("import_archive", func(t *testing.T) {
		s, _, game := mixedFlowServer(t)
		id := uploadArchive(t, s, "New-1.0.zip", map[string]string{"Mods/new.pak": "new"})
		rec := doAPI(s, http.MethodPost, altPlanTarget("import_archive", game), archivePlanBody(id))
		requireConflict(t, rec)
		assert.NoFileExists(t, deployedPath(game, "Mods/new.pak"))
	})

	t.Run("enable", func(t *testing.T) {
		s, svc, game := mixedFlowServer(t)
		rec := doAPI(s, http.MethodPost, altScoped("/api/v1/mods/"+fixtureSourceID+"/m2/enable", game), "")
		requireConflict(t, rec)
		assert.Contains(t, rec.Body.String(), `cannot enable a mod in profile \"alt\"`, "the CLI's words for the same refusal")
		assert.Empty(t, s.jobs.list(), "no job started")
		row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m2", game.ID, "alt")
		require.NoError(t, err)
		assert.True(t, row.Enabled, "alt's row is as it was")
	})
}

func TestFlowInactiveProfile_DisableRemovesOnlyItsOwn(t *testing.T) {
	s, svc, game := mixedFlowServer(t)

	rec := doAPI(s, http.MethodPost, altScoped("/api/v1/mods/"+fixtureSourceID+"/m1/disable", game), "")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var started struct {
		JobID jobID `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	j := awaitJob(t, s, started.JobID)

	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.FileExists(t, deployedFixturePath(game), "default records the file too")
	result, ok := j.status().Result.(*core.DisableResult)
	require.True(t, ok, "%T", j.status().Result)
	assert.True(t, result.RecordedOnly)
	assert.Equal(t, "default", result.ActiveProfile)
	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "alt")
	require.NoError(t, err)
	assert.True(t, profile.FindRef(fixtureSourceID, "m1").Disabled)
}

func TestFlowInactiveProfile_UninstallIsRecordedOnly(t *testing.T) {
	s, _, game := mixedFlowServer(t)

	rec := doAPI(s, http.MethodPost, altPlanTarget("uninstall", game), uninstallPlanBody)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		PlanID planID             `json:"plan_id"`
		Plan   core.UninstallPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Plan.RecordedOnly)
	assert.Empty(t, resp.Plan.Files, "default records the file too")
	require.Len(t, resp.Plan.Kept, 1)

	j := startFlowJob(t, s, resp.PlanID, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.FileExists(t, deployedFixturePath(game), "the active profile's file stays")
}

func TestFlowInactiveProfile_ProfileImportIsRecordedOnly(t *testing.T) {
	s, _, game := mixedFlowServer(t)
	doc := "name: alt\ngame_id: " + game.ID + "\nmods:\n  - source_id: " + fixtureSourceID + "\n    mod_id: m1\n    version: \"1.0\"\n"

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/profile_import", game), importPlanBody(t, doc))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Plan.RecordedOnly)
	assert.Equal(t, "default", resp.Plan.ActiveProfile)
}

func TestFlowInactiveProfile_VerifyFixRepairsNothingThatDeploys(t *testing.T) {
	s, _, game := mixedFlowServer(t)
	before := snapshotTree(t, game.ModPath)

	rec := doAPI(s, http.MethodPost, altPlanTarget("verify_fix", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		PlanID planID `json:"plan_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	j := startFlowJob(t, s, resp.PlanID, "")

	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, before, snapshotTree(t, game.ModPath))
}

// snapshotTree maps every file under root to its content, links followed.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = string(data)
		return nil
	}))
	return out
}

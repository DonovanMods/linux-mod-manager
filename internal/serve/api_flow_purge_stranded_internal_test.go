package serve

// #466 review F2 (#451): with every mod uninstalled after a games.yaml hand
// edit moved mod_path, the purge the refusal names has no mod to list. Its
// plan still says which files it removes from the old mod_path, and the job
// removes them.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlowPurge_PlanNamesAndJobRemovesStrandedFiles(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)
	deployed := deployedFixturePath(game)
	require.FileExists(t, deployed)

	gamesFile := filepath.Join(svc.ConfigDir(), "games.yaml")
	data, err := os.ReadFile(gamesFile)
	require.NoError(t, err)
	edited := strings.Replace(string(data), "mod_path: "+game.ModPath+"\n", "mod_path: "+game.ModPath+"-moved\n", 1)
	require.NotEqual(t, string(data), edited, "games.yaml names the mod_path")
	require.NoError(t, os.WriteFile(gamesFile, []byte(edited), 0o644))
	_, err = svc.ReloadGames()
	require.NoError(t, err)
	moved, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	installed, err := svc.GetInstalledMods(t.Context(), game.ID, "default")
	require.NoError(t, err)
	for _, m := range installed {
		_, err := svc.UninstallMod(t.Context(), moved, "default", m.SourceID, m.ID, core.UninstallOptions{})
		require.NoError(t, err)
	}
	require.FileExists(t, deployed)

	_, raw := planFlow(t, s, moved, "purge", "")
	assert.Contains(t, string(raw), `"stranded"`, "the plan names the files it removes: %s", raw)
	assert.Contains(t, string(raw), filepath.ToSlash(filepath.Base(deployed)))

	j := runFlow(t, s, moved, "purge", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.NoFileExists(t, deployed)
	problem, err := svc.ModPathProblem(t.Context(), moved)
	require.NoError(t, err)
	if problem != nil {
		// The new directory does not exist yet; that is all that is left.
		assert.Empty(t, problem.DeployedUnder, "nothing is recorded under the old mod_path any more")
	}
}

// TestFlowPurge_APlanIsStaleOnceTheModPathMovesBack is #466 re-review R2:
// the job applies the plan-time game, so a mod_path moved back between the
// plan and the job used to remove files the plan had described as under an
// earlier mod_path. The job now fails stale and removes nothing.
func TestFlowPurge_APlanIsStaleOnceTheModPathMovesBack(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)
	deployed := deployedFixturePath(game)
	require.FileExists(t, deployed)

	gamesFile := filepath.Join(svc.ConfigDir(), "games.yaml")
	setModPath := func(from, to string) {
		t.Helper()
		data, err := os.ReadFile(gamesFile)
		require.NoError(t, err)
		edited := strings.Replace(string(data), "mod_path: "+from+"\n", "mod_path: "+to+"\n", 1)
		require.NotEqual(t, string(data), edited, "games.yaml names the mod_path")
		require.NoError(t, os.WriteFile(gamesFile, []byte(edited), 0o644))
	}
	setModPath(game.ModPath, game.ModPath+"-moved")
	_, err := svc.ReloadGames()
	require.NoError(t, err)
	moved, err := svc.GetGame(game.ID)
	require.NoError(t, err)

	id, raw := planFlow(t, s, moved, "purge", "")
	require.Contains(t, string(raw), `"stranded"`, "%s", raw)

	// Moved back; the job's own request reloads games.yaml.
	setModPath(game.ModPath+"-moved", game.ModPath)
	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobFailed, j.status().State)
	require.ErrorIs(t, j.failure(), core.ErrStalePlan)
	assert.FileExists(t, deployed, "a stale plan removes nothing")
}

package serve

// #630 through the entry points the SPA drives: a deploy job breaks a mod's
// link into a copy holding the profile's override, the Health read reports
// the file as overridden by the profile, a verify_fix job leaves it, and a
// switch to a profile without the override puts the plain link back.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overriddenFlowFixture is the deploy fixture with its mod path moved under
// the install path, and the default profile overriding the mod's one file.
func overriddenFlowFixture(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)
	game.ModPath = filepath.Join(game.InstallPath, "mods")
	require.NoError(t, svc.SaveGame(context.Background(), game))
	profile, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	profile.Overrides = map[string][]byte{"mods/" + deployFixtureFile: []byte("tweaked")}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))
	return s, svc, game
}

func TestFlowOverridden_DeployHealthFixAndSwitch(t *testing.T) {
	s, svc, game := overriddenFlowFixture(t)
	deployed := deployedFixturePath(game)

	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.DeployResult)
	require.True(t, ok, "%T", j.status().Result)
	assert.Equal(t, 1, result.Deployed, "%+v", result)
	assert.Empty(t, result.Skipped)
	for _, w := range append(result.Warnings, result.Notes...) {
		assert.NotContains(t, w, "one.pak", "the deploy says nothing about the override it applied")
	}
	info, err := os.Lstat(deployed)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the mod's link was broken into a copy")
	assert.Equal(t, "tweaked", readFile(t, deployed))

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+"&force=1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"status": "deployed_overridden"`)
	assert.Contains(t, rec.Body.String(), "overridden by the profile")
	assert.NotContains(t, rec.Body.String(), "deployed_modified")

	j = runFlow(t, s, game, "verify_fix", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	report, ok := j.status().Result.(*core.VerifyReport)
	require.True(t, ok)
	var overridden int
	for _, f := range report.Result.Findings {
		if f.Status == core.VerifyStatusDeployedOverridden {
			overridden++
		}
	}
	assert.Equal(t, 1, overridden, "%+v", report.Result.Findings)
	assert.Equal(t, "tweaked", readFile(t, deployed), "--fix leaves the override")

	// A profile with the same mod and no override: switching to it puts
	// the plain link back.
	pm := svc.NewProfileManager()
	_, err = pm.Create(context.Background(), game.ID, "plain")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "plain",
		domain.ModReference{SourceID: fixtureSourceID, ModID: "m1", Version: "1.0"}))
	j = runFlow(t, s, game, "switch", `{"profile":"plain"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	info, err = os.Lstat(deployed)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "the plain link is back")
	assert.Equal(t, "pak bytes", readFile(t, deployed))
}

// TestFlowOverridden_RemovingTheOverrideRedeploysTheLink: the override is
// taken out of the profile and the web UI deploys again.
func TestFlowOverridden_RemovingTheOverrideRedeploysTheLink(t *testing.T) {
	s, svc, game := overriddenFlowFixture(t)
	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	profile, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	profile.Overrides = nil
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	j = runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	info, err := os.Lstat(deployedFixturePath(game))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "the plain link is back")
	rec := doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+"&force=1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, strings.Contains(rec.Body.String(), "deployed_overridden"))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

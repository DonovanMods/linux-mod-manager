package serve

// #427 review F8: a verify --fix job's re-download can raise a
// download-time warning (#425) - here #424's "BepInEx found in ...;
// declare it" notice - and it has to reach the job's stream as the
// WarningEvent the SPA's job activity renders. verify's own sub-line is a
// "verify" event, which the SPA does not read, so the warning used to be
// visible in the terminal and nowhere in the web UI.

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlowHealthFix_JobCarriesTheDownloadWarning(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	app.RegisterAdapters(svc)
	ctx := t.Context()

	// BepInEx installed, not declared, and the game deploying into its
	// root: the configuration #424's notice is about.
	preloader := filepath.Join(game.InstallPath, "BepInEx", "core", "BepInEx.Preloader.dll")
	require.NoError(t, os.MkdirAll(filepath.Dir(preloader), 0o755))
	require.NoError(t, os.WriteFile(preloader, []byte("preloader"), 0o644))
	rooted := *game
	rooted.ModPath = rooted.InstallPath
	require.NoError(t, svc.SaveGame(ctx, &rooted))
	game = &rooted

	src.mods["m5"] = &installSourceMod{
		mod:     domain.Mod{ID: "m5", SourceID: fixtureSourceID, Name: "Jotunn", Version: "1.0", GameID: game.ID},
		files:   []domain.DownloadableFile{{ID: "j1", Name: "Main", FileName: "Jotunn-1.0.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32}},
		members: map[string]string{"j1": "Jotunn/Jotunn.dll"},
	}
	plan, err := svc.PlanInstall(ctx, game, "default", fixtureSourceID, "m5", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	// The cache entry goes, so the repair re-downloads it.
	require.NoError(t, svc.GetGameCache(game).Delete(game.ID, fixtureSourceID, "m5", "1.0"))

	j := runFlow(t, s, game, "verify_fix", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	rec := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(j.id)+"/events", "")
	require.Equal(t, http.StatusOK, rec.Code)
	notice := "BepInEx found in " + game.InstallPath + "; declare it with `lmm game edit " + game.ID + " --loader bepinex`"
	var warnings []string
	for _, frame := range parseSSE(t, rec.Body.String()) {
		if frame.Event != "warning" {
			continue
		}
		var payload struct {
			Data struct {
				Op      string `json:"op"`
				Phase   string `json:"phase"`
				ModName string `json:"mod_name"`
				Message string `json:"message"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(frame.Data), &payload))
		assert.Equal(t, "verify", payload.Data.Op)
		assert.Equal(t, "download_warning", payload.Data.Phase)
		assert.Equal(t, "Jotunn", payload.Data.ModName)
		warnings = append(warnings, payload.Data.Message)
	}
	assert.Equal(t, []string{notice}, warnings, "the job's stream carries the warning the SPA renders")
}

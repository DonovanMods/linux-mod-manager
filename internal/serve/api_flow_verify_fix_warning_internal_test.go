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
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
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

// #517: a verify --fix job's RESULT says what each repair did and what still
// fails. A row repaired to "ok" is otherwise identical to an untouched one,
// and the repair's sub-line is an event the bounded ring may have dropped by
// the time anyone opens the finished job - so the finding itself carries it.
func TestFlowHealthFix_ResultSaysWhatWasRepairedAndWhatStillFails(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	ctx := t.Context()

	const (
		cachedPage = "https://example.test/mods/cached"
		stuckPage  = "https://example.test/mods/stuck"
	)
	for _, m := range []struct{ id, name, file, member, page string }{
		{"c1", "Cached Mod", "cf1", "Cached/cached.pak", cachedPage},
		{"s1", "Stuck Mod", "sf1", "Stuck/stuck.pak", stuckPage},
	} {
		src.mods[m.id] = &installSourceMod{
			mod:     domain.Mod{ID: m.id, SourceID: fixtureSourceID, Name: m.name, Version: "1.0", GameID: game.ID, SourceURL: m.page},
			files:   []domain.DownloadableFile{{ID: m.file, Name: "Main", FileName: m.id + ".zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32}},
			members: map[string]string{m.file: m.member},
		}
		plan, err := svc.PlanInstall(ctx, game, "default", fixtureSourceID, m.id, false)
		require.NoError(t, err)
		_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
		require.NoError(t, err)
	}
	// Cached Mod lost its checksum but keeps its complete cache entry; Stuck
	// Mod lost the cache entry itself. The source serves neither.
	require.NoError(t, svc.SaveFileChecksum(ctx, fixtureSourceID, "c1", game.ID, "default", "cf1", ""))
	require.NoError(t, svc.GetGameCache(game).Delete(game.ID, fixtureSourceID, "s1", "1.0"))
	src.refuse = map[string]error{
		"c1": &source.ManualDownloadError{Reason: "the author has turned off API downloads"},
		"s1": &source.ManualDownloadError{Reason: "the author has turned off API downloads"},
	}

	j := runFlow(t, s, game, "verify_fix", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	// The document a browser fetches, not the in-memory value.
	rec := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(j.id), "")
	require.Equal(t, http.StatusOK, rec.Code)
	var status struct {
		Result struct {
			Result struct {
				Findings []map[string]any `json:"findings"`
			} `json:"result"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	byMod := map[string]map[string]any{}
	for _, f := range status.Result.Result.Findings {
		if id, _ := f["mod_id"].(string); id == "c1" || id == "s1" {
			byMod[id] = f
		}
	}
	require.Contains(t, byMod, "c1")
	require.Contains(t, byMod, "s1")

	assert.Equal(t, "ok", byMod["c1"]["status"])
	assert.Equal(t, "Checksum filled from the cached files (the source won't serve this file)", byMod["c1"]["repair"])
	assert.NotContains(t, byMod["c1"], "mod_url", "a repaired row has no page to send anyone to")

	assert.Equal(t, "missing", byMod["s1"]["status"])
	assert.Equal(t, stuckPage, byMod["s1"]["mod_url"])
	assert.Equal(t, fixtureSourceID, byMod["s1"]["source_id"])
	assert.NotContains(t, byMod["s1"], "repair")

	// The sub-lines stream under the event name the SPA follows.
	rec = doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(j.id)+"/events", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var lines []string
	for _, frame := range parseSSE(t, rec.Body.String()) {
		if frame.Event != "verify" {
			continue
		}
		var payload struct {
			Data struct {
				Kind   string `json:"kind"`
				Detail string `json:"detail"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(frame.Data), &payload))
		if payload.Data.Kind == "repair_detail" {
			lines = append(lines, payload.Data.Detail)
		}
	}
	assert.Contains(t, lines, "Checksum filled from the cached files (the source won't serve this file)")
	assert.Contains(t, lines, "Download it manually from: "+stuckPage)
}

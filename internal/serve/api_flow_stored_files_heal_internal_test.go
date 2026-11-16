package serve

// #662 over /api/v1: the profile apply, switch and import plans the web UI
// renders name each mod whose stored main files mix versions and that the
// run heals to one (`heals`), and each collection mod that installs the
// curator's mixed files as chosen (`curator_mixed`); the job records the
// healed ids on the profile.

import (
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mixedMod serves "Mixed": a 2.0 main file, its 1.0 predecessor (archived,
// still a main file) and a 1.0 optional patch.
func mixedMod(game *domain.Game) *installSourceMod {
	return &installSourceMod{
		mod: domain.Mod{ID: "mx", SourceID: fixtureSourceID, Name: "Mixed", Version: "2.0", GameID: game.ID},
		files: []domain.DownloadableFile{
			{ID: "mx2", Name: "Main", FileName: "main-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 64},
			{ID: "mx1", Name: "Main", FileName: "main-1.0.zip", Version: "1.0", Category: "OLD_VERSION", Size: 64},
			{ID: "mxp", Name: "Patch", FileName: "patch-1.0.zip", Version: "1.0", Category: "OPTIONAL", Size: 64},
		},
		members: map[string]string{"mx2": "Mods/main2.pak", "mx1": "Mods/main1.pak", "mxp": "Mods/patch.pak"},
	}
}

var mixedIDs = []string{"mx1", "mx2", "mxp"}

var wantMixedHeal = core.StoredFilesHeal{SourceID: fixtureSourceID, ModID: "mx", Name: "Mixed", Version: "2.0", Dropped: []core.MixedVersion{
	{Version: "1.0", Files: []core.MixedVersionFile{{ID: "mx1", FileName: "main-1.0.zip"}}},
}}

// planStoredFiles decodes a plan response's heals and curator notes.
func planStoredFiles(t *testing.T, raw []byte) ([]core.StoredFilesHeal, []core.CuratorMixedFiles) {
	t.Helper()
	var resp struct {
		Plan struct {
			Heals        []core.StoredFilesHeal   `json:"heals"`
			CuratorMixed []core.CuratorMixedFiles `json:"curator_mixed"`
		} `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp), string(raw))
	return resp.Plan.Heals, resp.Plan.CuratorMixed
}

func profileRefOf(t *testing.T, s *Server, game *domain.Game, profile string) *domain.ModReference {
	t.Helper()
	p, err := s.svc.NewProfileManager().Get(t.Context(), game.ID, profile)
	require.NoError(t, err)
	ref := p.FindRef(fixtureSourceID, "mx")
	require.NotNil(t, ref)
	return ref
}

func TestFlowProfileApply_PlanNamesTheHealAndTheJobRecordsIt(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	src.mods["mx"] = mixedMod(game)
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: fixtureSourceID, ModID: "mx", Version: "2.0", FileIDs: mixedIDs}))

	id, raw := planFlow(t, s, game, "profile_apply", `{"profile":"default"}`)
	heals, curator := planStoredFiles(t, raw)
	assert.Equal(t, []core.StoredFilesHeal{wantMixedHeal}, heals)
	assert.Empty(t, curator)

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, []string{"mx2", "mxp"}, profileRefOf(t, s, game, "default").FileIDs)

	_, raw = planFlow(t, s, game, "profile_apply", `{"profile":"default"}`)
	assert.NotContains(t, string(raw), `"heals"`, "a healed reference is healed")
}

func TestFlowProfileApply_PlanNamesTheCuratorsMixedFiles(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	src.mods["mx"] = mixedMod(game)
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: fixtureSourceID, ModID: "mx", Version: "2.0", FileIDs: mixedIDs, FromCollection: true}))

	id, raw := planFlow(t, s, game, "profile_apply", `{"profile":"default"}`)
	heals, curator := planStoredFiles(t, raw)
	assert.Empty(t, heals)
	require.Len(t, curator, 1)
	assert.Equal(t, "Mixed", curator[0].Name)

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.ElementsMatch(t, mixedIDs, profileRefOf(t, s, game, "default").FileIDs, "the curator's files, all of them")
}

func TestFlowSwitch_PlanNamesTheHeal(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	src.mods["mx"] = mixedMod(game)
	pm := svc.NewProfileManager()
	_, err := pm.Create(t.Context(), game.ID, "stable")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(t.Context(), game.ID, "stable",
		domain.ModReference{SourceID: fixtureSourceID, ModID: "mx", Version: "2.0", FileIDs: mixedIDs}))

	id, raw := planFlow(t, s, game, "switch", `{"profile":"stable"}`)
	heals, _ := planStoredFiles(t, raw)
	assert.Equal(t, []core.StoredFilesHeal{wantMixedHeal}, heals)

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, []string{"mx2", "mxp"}, profileRefOf(t, s, game, "stable").FileIDs)
}

func TestFlowProfileImport_PlanNamesTheHeal(t *testing.T) {
	s, _, game, src := newInstallFixtureServer(t)
	src.mods["mx"] = mixedMod(game)
	doc := "name: imported\ngame_id: g1\nmods:\n  - source_id: " + fixtureSourceID + "\n    mod_id: mx\n    version: \"2.0\"\n    file_ids: [mx1, mx2, mxp]\n"
	body, err := json.Marshal(map[string]string{"data": doc})
	require.NoError(t, err)

	_, raw := planFlow(t, s, game, "profile_import", string(body))
	heals, _ := planStoredFiles(t, raw)
	assert.Equal(t, []core.StoredFilesHeal{wantMixedHeal}, heals)
}

package serve

// #634: a mod whose files each carry their own FOMOD installer, over
// /api/v1: the job's question holds one labelled entry per file, the
// re-post answers each under its file key, and "Reconfigure installer…"
// (mod_fomod) lists the installers and asks the one its file_id picks.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const perFileModID = "bugs"

// perFilePatchConfig is the patch file's installer.
const perFilePatchConfig = `<config><moduleName>Patch Installer</moduleName>
<installSteps order="Explicit"><installStep name="Patches"><optionalFileGroups order="Explicit">
<group name="Which" type="SelectExactlyOne"><plugins order="Explicit">
<plugin name="USSEP"><files><file source="p\ussep.esp" destination="bugs_patch.esp"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
<plugin name="Vanilla"><files><file source="p\vanilla.esp" destination="bugs_patch.esp"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep></installSteps></config>`

func perFilePatchArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"fomod/ModuleConfig.xml": perFilePatchConfig,
		"p/ussep.esp":            "USSEP",
		"p/vanilla.esp":          "VANILLA",
	} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// addPerFileMod serves "bugs" as two files, each an installer.
func addPerFileMod(t *testing.T, src *installSource) {
	t.Helper()
	src.mods[perFileModID] = &installSourceMod{
		mod: domain.Mod{ID: perFileModID, SourceID: fixtureSourceID, Name: "Scrambled Bugs", Version: "1.0", GameID: "g1"},
		files: []domain.DownloadableFile{
			{ID: "b1", Name: "Main", FileName: "main.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512},
			{ID: "b2", Name: "Patches", FileName: "patch.zip", Version: "1.0", Category: "OPTIONAL", Size: 512},
		},
		archives: map[string][]byte{"b1": fomodTestArchive(t), "b2": perFilePatchArchive(t)},
	}
}

func patchesStep(option string, idx int) domain.FomodStepChoice {
	return domain.FomodStepChoice{Name: "Patches", Groups: []domain.FomodGroupChoice{
		{Name: "Which", Choices: []domain.FomodOptionChoice{{Name: option, Idx: idx}}}}}
}

// perFileOptions is the apply options with answers keyed per file, plus
// extra members.
func perFileOptions(t *testing.T, answers map[string]domain.FomodChoices, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"fomod": answers, "file_ids": []string{"b1", "b2"}}
	for k, v := range extra {
		body[k] = v
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	return string(b)
}

func TestFlowFomodPerFile_InstallAsksEachFileThenTakesPerFileAnswers(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	addPerFileMod(t, src)

	j := runFlow(t, s, game, "install", installPlanBody(perFileModID), `{"file_ids":["b1","b2"]}`)
	q := fomodJobQuestion(t, j)
	require.Len(t, q.Mods, 2, "one labelled question per installer")
	assert.Equal(t, "b1", q.Mods[0].FileID)
	assert.Equal(t, "Main", q.Mods[0].FileName)
	assert.Equal(t, "Wizard Installer", q.Mods[0].ModuleName)
	assert.Equal(t, "b2", q.Mods[1].FileID)
	assert.Equal(t, "Patch Installer", q.Mods[1].ModuleName)
	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, perFileModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "nothing was written")

	answers := map[string]domain.FomodChoices{
		domain.FomodFileKey(fixtureSourceID, perFileModID, "b1"): {Steps: []domain.FomodStepChoice{sizeStep("2K", 0)}},
		domain.FomodFileKey(fixtureSourceID, perFileModID, "b2"): {Steps: []domain.FomodStepChoice{patchesStep("Vanilla", 1)}},
	}
	id, _ := planFlow(t, s, game, "install", installPlanBody(perFileModID))
	j = startFlowJob(t, s, id, perFileOptions(t, answers, nil))
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "2K", deployedContent(t, game, fomodFile))
	assert.Equal(t, "VANILLA", deployedContent(t, game, "bugs_patch.esp"))

	// The mod page reports the per-file choices.
	rec := doAPI(s, "GET", scoped("/api/v1/mods/"+fixtureSourceID+"/"+perFileModID, game), "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var detail core.ModDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	require.NotNil(t, detail.Installed.Fomod, "reconfigurable")
	assert.Len(t, detail.Installed.FomodFiles, 2)

	// Reconfigure: the plan lists the installers; file_id picks one.
	body := `{"source_id":"` + fixtureSourceID + `","mod_id":"` + perFileModID + `"}`
	id, raw := planFlow(t, s, game, "mod_fomod", body)
	var planned struct {
		Plan core.FomodReconfigurePlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &planned))
	require.Len(t, planned.Plan.Installers, 2, "%s", raw)
	assert.Equal(t, "Patches", planned.Plan.Installers[1].Name)
	j = startFlowJob(t, s, id, `{"file_id":"b2"}`)
	q = fomodJobQuestion(t, j)
	require.Len(t, q.Mods, 1, "only the picked installer asks")
	assert.Equal(t, "b2", q.Mods[0].FileID)
	assert.Equal(t, core.FomodReasonReconfigure, q.Mods[0].Reason)

	id, _ = planFlow(t, s, game, "mod_fomod", body)
	b, err := json.Marshal(map[string]any{"file_id": "b2", "fomod": map[string]domain.FomodChoices{
		domain.FomodFileKey(fixtureSourceID, perFileModID, "b2"): {Steps: []domain.FomodStepChoice{patchesStep("USSEP", 0)}},
	}})
	require.NoError(t, err)
	j = startFlowJob(t, s, id, string(b))
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "USSEP", deployedContent(t, game, "bugs_patch.esp"))
	assert.Equal(t, "2K", deployedContent(t, game, fomodFile), "the other installer kept its choices")
	p, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	ref := p.FindRef(fixtureSourceID, perFileModID)
	require.NotNil(t, ref)
	assert.Equal(t, "USSEP", ref.FomodFiles["b2"].Steps[0].Groups[0].Choices[0].Name)
	assert.Equal(t, "2K", ref.FomodFiles["b1"].Steps[0].Groups[0].Choices[0].Name)

	// A file that is not one of its installers is refused, nothing done.
	id, _ = planFlow(t, s, game, "mod_fomod", body)
	j = startFlowJob(t, s, id, `{"file_id":"nope"}`)
	require.Equal(t, jobFailed, j.status().State)
	assert.Contains(t, j.status().Error.Error, `file "nope" is not one of its installers`)
}

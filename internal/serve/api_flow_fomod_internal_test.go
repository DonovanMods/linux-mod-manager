package serve

// #615: FOMOD installers over /api/v1 - the job that fails with the
// installer's question, the option images the wizard shows, the re-posted
// plan and job carrying the answers, and "Reconfigure installer…"
// (mod_fomod).

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fomodModID = "fm1"
	fomodFile  = "textures/t.dds"
)

// fomodTestPNG is a 1x1 PNG: a real image the browser can decode.
var fomodTestPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0xf0,
	0x1f, 0x00, 0x05, 0x00, 0x01, 0xff, 0x89, 0x99, 0x3d, 0x1d, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
	0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

// fomodTestConfig: a required plugin, step one's exactly-one texture
// group (2K with an image, 4K, which reveals step two), and step two's
// optional readme.
const fomodTestConfig = `<config><moduleName>Wizard Installer</moduleName>
<requiredInstallFiles><file source="core\plugin.esp" destination="plugin.esp"/></requiredInstallFiles>
<installSteps order="Explicit">
<installStep name="Size"><optionalFileGroups order="Explicit">
<group name="Texture" type="SelectExactlyOne"><plugins order="Explicit">
<plugin name="2K"><description>Light on VRAM.</description><image path="fomod\images\2k.png"/><files><file source="tex\2k.dds" destination="textures\t.dds"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
<plugin name="4K"><description>Heavy.</description><files><file source="tex\4k.dds" destination="textures\t.dds"/></files><conditionFlags><flag name="big">on</flag></conditionFlags><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep>
<installStep name="Extras"><visible><flagDependency flag="big" value="on"/></visible><optionalFileGroups order="Explicit">
<group name="Docs" type="SelectAny"><plugins order="Explicit">
<plugin name="Readme"><description>A text file.</description><files><file source="docs\readme.txt" destination="readme.txt"/></files><typeDescriptor><type name="Recommended"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep>
</installSteps></config>`

// fomodTestArchive is the installer archive the fixture serves.
func fomodTestArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string][]byte{
		"fomod/ModuleConfig.xml": []byte(fomodTestConfig),
		"fomod/images/2k.png":    fomodTestPNG,
		"core/plugin.esp":        []byte("PLUGIN"),
		"tex/2k.dds":             []byte("2K"),
		"tex/4k.dds":             []byte("4K"),
		"docs/readme.txt":        []byte("README"),
	} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// addFomodMod serves fm1 as a FOMOD installer.
func addFomodMod(t *testing.T, src *installSource) {
	t.Helper()
	src.mods[fomodModID] = &installSourceMod{
		mod:      domain.Mod{ID: fomodModID, SourceID: fixtureSourceID, Name: "Wizard Mod", Version: "1.0", GameID: "g1"},
		files:    []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "wizard.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}},
		archives: map[string][]byte{"w1": fomodTestArchive(t)},
	}
}

// fomodJobQuestion decodes a failed job's FOMOD question.
func fomodJobQuestion(t *testing.T, j *job) core.FomodChoicesRequiredError {
	t.Helper()
	st := j.status()
	require.Equal(t, jobFailed, st.State)
	require.NotNil(t, st.Error)
	raw, err := json.Marshal(st.Error.Details)
	require.NoError(t, err)
	var q core.FomodChoicesRequiredError
	require.NoError(t, json.Unmarshal(raw, &q))
	require.NotEmpty(t, q.Mods, "details: %s", raw)
	return q
}

// fomodAnswer is the apply options naming fm1's answer.
func fomodAnswer(t *testing.T, steps ...domain.FomodStepChoice) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"fomod": map[string]domain.FomodChoices{
		fixtureSourceID + ":" + fomodModID: {Steps: steps},
	}})
	require.NoError(t, err)
	return string(b)
}

func sizeStep(option string, idx int) domain.FomodStepChoice {
	return domain.FomodStepChoice{Name: "Size", Groups: []domain.FomodGroupChoice{
		{Name: "Texture", Choices: []domain.FomodOptionChoice{{Name: option, Idx: idx}}}}}
}

func TestFlowFomod_InstallAsksShowsImagesThenTakesTheAnswer(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	addFomodMod(t, src)

	j := runFlow(t, s, game, "install", installPlanBody(fomodModID), "")
	q := fomodJobQuestion(t, j)
	question := q.Mods[0]
	assert.Equal(t, "Wizard Installer", question.ModuleName)
	assert.Equal(t, core.FomodReasonUnanswered, question.Reason)
	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, fomodModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "nothing was written")

	// The wizard's image, through the read-only route.
	opt := question.Steps[0].Groups[0].Options[0]
	require.Equal(t, `fomod\images\2k.png`, opt.Image)
	imageURL := func(path string) string {
		return "/api/v1/fomod/images?" + url.Values{
			"source": {question.SourceID}, "mod": {question.ModID}, "config": {question.ConfigSHA256}, "path": {path},
		}.Encode()
	}
	rec := doAPI(s, http.MethodGet, imageURL(opt.Image), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, fomodTestPNG, rec.Body.Bytes())
	for _, bad := range []string{"core/plugin.esp", "../../../etc/passwd", "/etc/passwd", "fomod/ModuleConfig.xml", ""} {
		rec := doAPI(s, http.MethodGet, imageURL(bad), "")
		assert.Equal(t, http.StatusNotFound, rec.Code, "%q: %s", bad, rec.Body.String())
	}

	// 4K reveals the Extras step: the re-post asks again, about it.
	id, _ := planFlow(t, s, game, "install", installPlanBody(fomodModID))
	j = startFlowJob(t, s, id, fomodAnswer(t, sizeStep("4K", 1)))
	q = fomodJobQuestion(t, j)
	require.Len(t, q.Mods[0].Steps, 2)
	assert.Equal(t, "Extras", q.Mods[0].Steps[1].Name)
	assert.True(t, q.Mods[0].Steps[1].Groups[0].Options[0].Selected, "Recommended is preselected")

	// The full answer installs.
	id, _ = planFlow(t, s, game, "install", installPlanBody(fomodModID))
	j = startFlowJob(t, s, id, fomodAnswer(t, sizeStep("4K", 1),
		domain.FomodStepChoice{Name: "Extras", Groups: []domain.FomodGroupChoice{{Name: "Docs", Choices: []domain.FomodOptionChoice{}}}}))
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "4K", deployedContent(t, game, fomodFile))
	assert.Equal(t, "PLUGIN", deployedContent(t, game, "plugin.esp"))
	assert.NoFileExists(t, deployedPath(game, "readme.txt"))
}

func TestFlowFomod_ReconfigureAsksPreselectedThenRedeploys(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	addFomodMod(t, src)
	id, _ := planFlow(t, s, game, "install", installPlanBody(fomodModID))
	j := startFlowJob(t, s, id, fomodAnswer(t, sizeStep("2K", 0)))
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	require.Equal(t, "2K", deployedContent(t, game, fomodFile))

	// The mod page knows it can reconfigure.
	rec := doAPI(s, http.MethodGet, scoped("/api/v1/mods/"+fixtureSourceID+"/"+fomodModID, game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var detail core.ModDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	require.NotNil(t, detail.Installed.Fomod)

	body := `{"source_id":"` + fixtureSourceID + `","mod_id":"` + fomodModID + `"}`
	id, raw := planFlow(t, s, game, "mod_fomod", body)
	assert.Contains(t, string(raw), `"choices"`)
	j = startFlowJob(t, s, id, "")
	q := fomodJobQuestion(t, j)
	assert.Equal(t, core.FomodReasonReconfigure, q.Mods[0].Reason)
	assert.True(t, q.Mods[0].Steps[0].Groups[0].Options[0].Selected, "the recorded 2K is preselected")
	assert.Equal(t, "2K", deployedContent(t, game, fomodFile), "nothing changed")

	id, _ = planFlow(t, s, game, "mod_fomod", body)
	j = startFlowJob(t, s, id, fomodAnswer(t, sizeStep("4K", 1),
		domain.FomodStepChoice{Name: "Extras", Groups: []domain.FomodGroupChoice{{Name: "Docs", Choices: []domain.FomodOptionChoice{{Name: "Readme", Idx: 0}}}}}))
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "4K", deployedContent(t, game, fomodFile))
	assert.Equal(t, "README", deployedContent(t, game, "readme.txt"))
	ref := func() *domain.ModReference {
		p, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
		require.NoError(t, err)
		return p.FindRef(fixtureSourceID, fomodModID)
	}()
	require.NotNil(t, ref)
	assert.Equal(t, "4K", ref.Fomod.Steps[0].Groups[0].Choices[0].Name)
}

func TestFlowFomod_ReconfigureRefusesAPlainMod(t *testing.T) {
	s, _, game, _ := newInstallFixtureServer(t)
	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/mod_fomod", game), `{"source_id":"`+fixtureSourceID+`","mod_id":"m1"}`)
	assert.NotEqual(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "not installed through a FOMOD installer")

	rec = doAPI(s, http.MethodPost, scoped("/api/v1/plans/mod_fomod", game), `{"mod_id":"m1"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestFlowFomod_UpdateBatchSkipCarriesTheQuestionAndTakesTheAnswer: the
// web UI's update path (#615 review 5): an update whose recorded choices no
// longer fit its new installer is skipped with the question on the batch
// job's result, and the batch re-posted with "fomod" (what the wizard's
// Finish sends) applies it.
func TestFlowFomod_UpdateBatchSkipCarriesTheQuestionAndTakesTheAnswer(t *testing.T) {
	src := newUpdatesSource(t)
	src.archives = map[string][]byte{"u1-" + updateToVersion: fomodTestArchive(t)}
	s, svc, game := newUpdatesFixtureServerWith(t, src)
	require.NoError(t, svc.NewProfileManager().UpsertMod(t.Context(), game.ID, "default", domain.ModReference{
		SourceID: fixtureSourceID, ModID: "u1", Version: updateFromVersion,
		Fomod: domain.FomodChoices{ConfigSHA256: "old", Steps: []domain.FomodStepChoice{sizeStep("Gone", 0)}},
	}))
	body := `{"mods":["` + fixtureSourceID + `:u1"]}`

	j := runFlow(t, s, game, "updates", body, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	raw, err := json.Marshal(j.status().Result)
	require.NoError(t, err)
	var result core.UpdateBatchResult
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result.Skipped, 1, "%s", raw)
	require.True(t, result.Skipped[0].FomodReanswer)
	require.NotNil(t, result.Skipped[0].FomodChoicesRequired, "the skip carries its question")
	assert.Equal(t, "u1", result.Skipped[0].FomodChoicesRequired.Mods[0].ModID)

	answer, err := json.Marshal(map[string]any{"fomod": map[string]domain.FomodChoices{
		fixtureSourceID + ":u1": {Steps: []domain.FomodStepChoice{sizeStep("4K", 1),
			{Name: "Extras", Groups: []domain.FomodGroupChoice{{Name: "Docs", Choices: []domain.FomodOptionChoice{}}}}}},
	}})
	require.NoError(t, err)
	j = runFlow(t, s, game, "updates", body, string(answer))
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	raw, err = json.Marshal(j.status().Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result.Applied, 1, "%s", raw)
	assert.Equal(t, "4K", deployedContent(t, game, fomodFile))
}

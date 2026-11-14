package serve

// #609 over /api/v1: a collection whose recorded FOMOD choices were made
// against another archive than the one downloaded comes back from its
// profile_import job with the installer's question, its reason and both
// md5s - the document the web wizard shows - and the re-posted answer
// imports it.

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// md5PinnedCollection records fm1's 4K choice against the archive whose
// md5 is sum.
func md5PinnedCollection(sum string) []byte {
	return []byte("lmm_collection: 1\nname: Pinned Pack\nrequires: [fomod]\nmods:\n" +
		"  - source_id: " + fixtureSourceID + "\n    mod_id: " + fomodModID + "\n    version: \"1.0\"\n    file_ids: [w1]\n" +
		"    fomod: {steps: [{name: Size, groups: [{name: Texture, choices: [{name: 4K}]}]}, {name: Extras, groups: [{name: Docs, choices: []}]}]}\n" +
		"    files: [{id: w1, md5: \"" + sum + "\"}]\n")
}

func TestFlowFomod_ACollectionForAnotherArchiveAsksWithTheReason(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	addFomodMod(t, src)
	archive := src.mods[fomodModID].archives["w1"]
	sum := md5.Sum(archive)
	recorded := strings.Repeat("0", 32)

	j := runFlow(t, s, game, "profile_import", lmmcPlanBody(t, md5PinnedCollection(recorded), ""), `{"install":true}`)
	st := j.status()
	require.Equal(t, jobFailed, st.State)
	require.NotNil(t, st.Error)
	raw, err := json.Marshal(st.Error.Details)
	require.NoError(t, err)
	var result core.ProfileImportResult
	require.NoError(t, json.Unmarshal(raw, &result))
	require.NotNil(t, result.FomodChoicesRequired, "details: %s", raw)
	q := result.FomodChoicesRequired
	require.Len(t, q.Mods, 1)
	assert.Equal(t, core.FomodReasonArchiveMismatch, q.Mods[0].Reason)
	require.NotNil(t, q.Mods[0].ArchiveMismatch)
	assert.Equal(t, recorded, q.Mods[0].ArchiveMismatch.ExpectedMD5)
	assert.Equal(t, hex.EncodeToString(sum[:]), q.Mods[0].ArchiveMismatch.ActualMD5)
	_, err = config.LoadProfile(svc.ConfigDir(), game.ID, "Pinned Pack")
	require.Error(t, err, "nothing was written")

	id, _ := planFlow(t, s, game, "profile_import", lmmcPlanBody(t, md5PinnedCollection(recorded), ""))
	answer := strings.Replace(fomodAnswer(t, sizeStep("2K", 0)), "{", `{"install":true,`, 1)
	j = startFlowJob(t, s, id, answer)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "Pinned Pack")
	require.NoError(t, err)
	ref := saved.FindRef(fixtureSourceID, fomodModID)
	require.NotNil(t, ref)
	assert.Equal(t, "2K", ref.Fomod.Steps[0].Groups[0].Choices[0].Name)
}

func TestFlowFomod_ACollectionForThisArchiveReplaysUnasked(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	addFomodMod(t, src)
	sum := md5.Sum(src.mods[fomodModID].archives["w1"])

	j := runFlow(t, s, game, "profile_import", lmmcPlanBody(t, md5PinnedCollection(hex.EncodeToString(sum[:])), ""), `{"install":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	saved, err := config.LoadProfile(svc.ConfigDir(), game.ID, "Pinned Pack")
	require.NoError(t, err)
	assert.Equal(t, "4K", saved.FindRef(fixtureSourceID, fomodModID).Fomod.Steps[0].Groups[0].Choices[0].Name)
}

package serve

// #610 C5 through the endpoints the web UI drives: a NexusMods collection
// with an exact file list, a patch to code and a bundled item. The plan
// carries each; the job refuses the patch to code without
// allow_any_overrides (nothing written) and with it saves the profile,
// recording the file list and the patch, and installs the bundled item -
// on a free account, since the collection carries it.

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nexusC5Recipe = `{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact",
	 "FileList": [{"Path": "Ten\\a.json", "MD5": "0cc175b9c0f1b6a831c399e269772661"}]},
	{"Name": "Eleven", "Version": "1.1", "SourceType": "nexus", "ModID": "11", "FileID": "110", "UpdatePolicy": "exact",
	 "Patches": {"Eleven\\Eleven.dll": "CABDDFC4"}},
	{"Name": "Marker", "Version": "1.0.0", "SourceType": "bundle", "UpdatePolicy": "exact", "FileExpression": "Bundled - Marker (v1.0.0)"}]}`

// seedNexusC5Trees writes the recipe's bundled/ and patches/ trees beside
// its manifest, as a fetch extracts them.
func seedNexusC5Trees(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	entry := filepath.Join(svc.GlobalCacheDir(), game.ID, "collections", "nexusmods-abcdef", "3")
	for rel, data := range map[string][]byte{
		"bundled/Bundled - Marker (v1.0.0)/[CP] Marker/manifest.json": []byte(`{"Name": "Marker"}`),
		"patches/Eleven/Eleven/Eleven.dll.diff":                       append([]byte("BSDIFF40"), make([]byte, 24)...),
		".lmm-recipe-files":                                           nil,
	} {
		p := filepath.Join(entry, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, data, 0o644))
	}
}

func TestAPIFlow_ProfileImportNexus_FileListsPatchesAndBundles(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedNexusCollectionRecipe(t, svc, game, source.TierFree, nexusC5Recipe)
	seedNexusC5Trees(t, svc, game)

	planID, raw := planFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef"}`)
	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	c := resp.Plan.Collection
	require.NotNil(t, c)
	assert.Empty(t, c.Unsupported)
	assert.Equal(t, 1, c.RestrictedPatches)
	require.Len(t, c.Bundled, 1)
	byID := map[string]core.CollectionImportItem{}
	for _, it := range c.Items {
		byID[it.ModID] = it
	}
	assert.Equal(t, 1, byID["10"].FileList)
	require.Len(t, byID["11"].Patches, 1)
	assert.Equal(t, `Eleven\Eleven.dll`, byID["11"].Patches[0].Path)
	assert.NotEmpty(t, byID["11"].Patches[0].Restricted)
	assert.Equal(t, core.CollectionItemBundled, c.Source.Items[2].State)

	// Without allow_any_overrides: refused, nothing written.
	j := startFlowJob(t, s, planID, `{"install":true}`)
	require.Equal(t, jobFailed, j.status().State)
	assert.Contains(t, j.status().Error.Error, "patches code")
	_, err := svc.NewProfileManager().Get(t.Context(), game.ID, "Three Mods")
	require.ErrorIs(t, err, domain.ErrProfileNotFound)

	planID, _ = planFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef"}`)
	j = startFlowJob(t, s, planID, `{"install":true,"allow_any_overrides":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "Three Mods")
	require.NoError(t, err)
	require.NotNil(t, profile.FindRef("nexusmods", "10"))
	assert.Len(t, profile.FindRef("nexusmods", "10").FileList["100"], 1)
	require.NotNil(t, profile.FindRef("nexusmods", "11"))
	assert.Len(t, profile.FindRef("nexusmods", "11").Patches["110"], 1)
	require.NotNil(t, profile.FindRef(domain.SourceCollection, "nexusmods-abcdef.marker-v1-0-0"))
	installed, err := svc.GetInstalledMods(t.Context(), game.ID, "Three Mods")
	require.NoError(t, err)
	require.Len(t, installed, 1, "the bundled item installs on a free account; the two downloads are pending")
	assert.Equal(t, domain.SourceCollection, installed[0].SourceID)
}

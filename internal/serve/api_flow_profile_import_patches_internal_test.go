package serve

// #610 review B1 through the endpoints: an older profile export naming a
// patch this machine keeps - for another file - plans with the patch in
// tree_inputs, restricted; the job refuses without allow_any_overrides,
// writing nothing, and saves the profile with it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keepUnadmittedPatch puts a patch in game's patch store with no import's
// admission, as a collection imported for another file would leave it.
func keepUnadmittedPatch(t *testing.T, svc *core.Service, game *domain.Game) string {
	t.Helper()
	patch := append([]byte("BSDIFF40"), make([]byte, 24)...)
	sum := sha256.Sum256(patch)
	sha := hex.EncodeToString(sum[:])
	dir := filepath.Join(svc.GlobalCacheDir(), game.ID, "collections", "patches")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, sha+".diff"), patch, 0o644))
	return sha
}

func TestFlowProfileImport_APatchNoImportAdmittedNeedsItsBox(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	sha := keepUnadmittedPatch(t, svc, game)
	doc := fmt.Sprintf("name: patched\ngame_id: g1\nmods:\n  - source_id: fake\n    mod_id: p2\n    version: \"2.0\"\n    file_ids: [\"f\"]\n"+
		"    patches:\n      \"f\": [{path: data/config.json, crc32: CABDDFC4, sha256: %s}]\n", sha)

	planID, raw := planFlow(t, s, game, "profile_import", importPlanBody(t, doc))
	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.Len(t, resp.Plan.TreeInputs, 1)
	require.Len(t, resp.Plan.TreeInputs[0].Patches, 1)
	assert.Contains(t, resp.Plan.TreeInputs[0].Patches[0].Restricted, "another file")

	j := startFlowJob(t, s, planID, `{}`)
	require.Equal(t, jobFailed, j.status().State)
	assert.Contains(t, j.status().Error.Error, "allow-any-overrides")
	_, err := svc.NewProfileManager().Get(t.Context(), game.ID, "patched")
	require.ErrorIs(t, err, domain.ErrProfileNotFound)

	planID, _ = planFlow(t, s, game, "profile_import", importPlanBody(t, doc))
	j = startFlowJob(t, s, planID, `{"allow_any_overrides":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	p, err := svc.NewProfileManager().Get(t.Context(), game.ID, "patched")
	require.NoError(t, err)
	assert.Len(t, p.FindRef("fake", "p2").Patches["f"], 1)
}

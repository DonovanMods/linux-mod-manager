package serve

// The whole archive-import flow, end to end through the entry points the
// SPA drives: POST /api/v1/uploads, POST /api/v1/plans/import_archive, POST
// /api/v1/jobs - with assertions on the END STATE (the cache, the DB row,
// the profile, the deployed tree), not just the documents.

import (
	"archive/zip"
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zipBytes builds a zip archive in memory - the body a browser posts.
func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(path)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

// uploadArchive posts one archive and returns its handle.
func uploadArchive(t *testing.T, s *Server, filename string, files map[string]string) uploadID {
	t.Helper()
	rec := postUpload(t, s, filename, zipBytes(t, files))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decodeUpload(t, rec.Body.Bytes()).UploadID
}

// archivePlanBody is the plan request for one staged upload.
func archivePlanBody(id uploadID) string {
	return `{"upload_id":"` + string(id) + `"}`
}

func TestAPIFlow_ImportArchive_InstallsTheUploadedMod(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	id := uploadArchive(t, s, "MyMod-1.2.zip", map[string]string{"mymod.esp": "mod bytes"})

	planID, planBody := planFlow(t, s, game, "import_archive", archivePlanBody(id))

	// The plan document is core.ImportArchivePlan verbatim - the same one
	// `lmm import --dry-run --json` emits.
	var planned struct {
		Plan core.ImportArchivePlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(planBody, &planned))
	assert.Equal(t, domain.SourceLocal, planned.Plan.LinkedSource)
	assert.Equal(t, []string{"mymod.esp"}, planned.Plan.Files)
	assert.Empty(t, planned.Plan.Conflicts)

	j := startFlowJob(t, s, planID, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	result, ok := j.status().Result.(*core.ImportArchiveResult)
	require.True(t, ok)
	require.NotNil(t, result.Mod)
	assert.Equal(t, domain.SourceLocal, result.LinkedSource)
	assert.Equal(t, 1, result.Deployed)

	// End state: the file is really in the game directory, the DB row
	// exists, and the profile names it.
	assert.Equal(t, "mod bytes", deployedContent(t, game, "mymod.esp"))
	mods, err := svc.GetInstalledMods(t.Context(), game.ID, "default")
	require.NoError(t, err)
	var found bool
	for _, m := range mods {
		if m.Name == result.Mod.Name {
			found = true
		}
	}
	assert.True(t, found, "the imported mod has an installed_mods row")
}

// TestAPIFlow_ImportArchive_RemovesTheStagedArchiveOnlyOnSuccess pins the
// upload lifecycle: a successful import reclaims the staged file, a failed
// one keeps it for the retry.
func TestAPIFlow_ImportArchive_RemovesTheStagedArchiveOnlyOnSuccess(t *testing.T) {
	t.Run("success reclaims it", func(t *testing.T) {
		s, _, game := newFlowFixtureServer(t)
		id := uploadArchive(t, s, "MyMod-1.2.zip", map[string]string{"mymod.esp": "x"})
		staged, ok := s.uploads.Get(id)
		require.True(t, ok)

		j := runFlow(t, s, game, "import_archive", archivePlanBody(id), "")
		require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

		_, ok = s.uploads.Get(id)
		assert.False(t, ok, "the handle is gone")
		assert.NoDirExists(t, staged.Dir, "and so is the disk it held")
	})

	t.Run("a failure keeps it for the retry", func(t *testing.T) {
		s, _, game := newFlowFixtureServer(t)
		id := uploadArchive(t, s, "MyMod-1.2.zip", map[string]string{"mymod.esp": "x"})
		planID, _ := planFlow(t, s, game, "import_archive", archivePlanBody(id))

		// Delete the staged file underneath the plan: the fingerprint
		// precondition #314 added is what refuses it, and this is the only
		// way that check can ever fire over an upload.
		staged, ok := s.uploads.Get(id)
		require.True(t, ok)
		require.NoError(t, os.Remove(staged.Path))

		j := startFlowJob(t, s, planID, "")
		require.Equal(t, jobFailed, j.status().State)

		_, ok = s.uploads.Get(id)
		assert.True(t, ok, "the upload survives a failed apply")
	})
}

// TestAPIFlow_ImportArchive_ConflictRefusalIsTheTypedError pins that the
// Overwrite affordance's answer travels as accept_conflicts and maps to
// ImportArchiveOptions.AcceptConflicts - the same round trip install's job
// performs, so the SPA reuses one renderer.
func TestAPIFlow_ImportArchive_ConflictRefusalIsTheTypedError(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	// An archive whose single file is the one the fixture mod already owns.
	id := uploadArchive(t, s, "Clash-1.0.zip", map[string]string{deployFixtureFile: "other bytes"})
	planID, planBody := planFlow(t, s, game, "import_archive", archivePlanBody(id))

	var resp struct {
		Plan core.ImportArchivePlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(planBody, &resp))
	require.NotEmpty(t, resp.Plan.Conflicts, "the plan previews the conflict with no ingest at all")

	j := startFlowJob(t, s, planID, "")
	require.Equal(t, jobFailed, j.status().State)
	require.NotNil(t, j.status().Error)
	assert.NotNil(t, j.status().Error.Details, "the conflict list reaches the SPA as typed details")

	// Answering it: re-plan (the first plan was single-use) and apply with
	// accept_conflicts.
	id2 := uploadArchive(t, s, "Clash-1.0.zip", map[string]string{deployFixtureFile: "other bytes"})
	j2 := runFlow(t, s, game, "import_archive", archivePlanBody(id2), `{"accept_conflicts":true}`)
	assert.Equal(t, jobSucceeded, j2.status().State, "%v", j2.status().Error)
}

func TestAPIFlow_ImportArchive_RefusesAnUnknownUpload(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/import_archive", game),
		`{"upload_id":"deadbeefdeadbeefdeadbeefdeadbeef"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "an expired or cancelled handle is the caller's to fix")
}

func TestAPIFlow_ImportArchive_RefusesAPlanWithNoUpload(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/import_archive", game), `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "upload_id")
}

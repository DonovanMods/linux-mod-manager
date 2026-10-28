package serve

// #530's update-from-file, end to end through the entry points the SPA
// drives: POST /api/v1/uploads, POST /api/v1/plans/update_from_archive, POST
// /api/v1/jobs - with assertions on the END STATE (the DB row, the profile,
// the deployed tree, the upload), not just the documents.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// manualUpdateSource is the fixture source as a CurseForge mod whose author
// disabled third-party downloads: it lists Mod One's 2.0 builds, its update
// check advertises one of them, and it refuses every download.
type manualUpdateSource struct{ fixtureSource }

func (*manualUpdateSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return &domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "2.0", GameID: "g1",
		SourceURL: "https://example.test/mods/m1"}, nil
}

func (*manualUpdateSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "200", FileName: "ModOne-2.0.zip", Version: "2.0"},
		{ID: "201", FileName: "ModOne-Fabric-2.0.zip", Version: "2.0"},
	}, nil
}

func (*manualUpdateSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var out []domain.Update
	for _, im := range installed {
		if im.ID == "m1" && im.Version != "2.0" {
			// The check names the exact file it advertises (#504), as
			// CurseForge's does.
			out = append(out, domain.Update{InstalledMod: im, NewVersion: "2.0", FileIDReplacements: map[string]string{"100": "200"}})
		}
	}
	return out, nil
}

func (*manualUpdateSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", &source.ManualDownloadError{Reason: "mod author has disabled third-party downloads"}
}

func newUpdateFromArchiveServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)
	svc.RegisterSource(&manualUpdateSource{})
	return s, svc, game
}

func updateFromArchiveBody(id uploadID) string {
	return `{"upload_id":"` + string(id) + `","source_id":"` + fixtureSourceID + `","mod_id":"m1"}`
}

func TestAPIFlow_UpdateFromArchive_UpdatesTheInstalledMod(t *testing.T) {
	s, svc, game := newUpdateFromArchiveServer(t)
	id := uploadArchive(t, s, "ModOne-2.0 (1).zip", map[string]string{"modone.pak": "v2 bytes"})

	planID, planBody := planFlow(t, s, game, "update_from_archive", updateFromArchiveBody(id))
	var planned struct {
		Plan core.UpdateFromArchivePlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(planBody, &planned))
	assert.Equal(t, core.ArchiveMatchAdvertised, planned.Plan.Match)
	assert.True(t, planned.Plan.MatchNormalized)
	assert.Equal(t, "2.0", planned.Plan.ToVersion)
	assert.Equal(t, []string{"200"}, planned.Plan.FileIDs)

	j := startFlowJob(t, s, planID, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	result, ok := j.status().Result.(*core.UpdateApplyResult)
	require.True(t, ok)
	assert.Equal(t, core.UpdateUpdated, result.Status)

	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.Equal(t, []string{"200"}, row.FileIDs)
	assert.Equal(t, "1.0", row.PreviousVersion)
	assert.Equal(t, "v2 bytes", deployedContent(t, game, "modone.pak"))

	prof, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", prof.FindRef(fixtureSourceID, "m1").Version)

	_, ok = s.uploads.Get(id)
	assert.False(t, ok, "a successful update reclaims the upload")
}

// A mismatch fails the job with the typed refusal and keeps the upload;
// "Update anyway" (accept_mismatch) on a fresh plan over the same upload
// completes it.
func TestAPIFlow_UpdateFromArchive_MismatchIsAnsweredWithAcceptMismatch(t *testing.T) {
	s, svc, game := newUpdateFromArchiveServer(t)
	id := uploadArchive(t, s, "ModOne-Fabric-2.0.zip", map[string]string{"modone.pak": "fabric bytes"})

	j := runFlow(t, s, game, "update_from_archive", updateFromArchiveBody(id), "")
	require.Equal(t, jobFailed, j.status().State)
	envelope := j.status().Error
	require.NotNil(t, envelope)
	details, err := json.Marshal(envelope.Details)
	require.NoError(t, err)
	assert.JSONEq(t, `{"archive_name":"ModOne-Fabric-2.0.zip","advertised":{"id":"200","file_name":"ModOne-2.0.zip","version":"2.0"},"matched":{"id":"201","file_name":"ModOne-Fabric-2.0.zip","version":"2.0"}}`, string(details))
	_, ok := s.uploads.Get(id)
	require.True(t, ok, "a refused update keeps the upload for the retry")

	j = runFlow(t, s, game, "update_from_archive", updateFromArchiveBody(id), `{"accept_mismatch":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"201"}, row.FileIDs)
}

// The plan's own refusals are the caller's to answer: a version nothing
// names is a 400 carrying version_required, and the installed version again
// is a 409.
func TestAPIPlans_UpdateFromArchive_Refusals(t *testing.T) {
	s, _, game := newUpdateFromArchiveServer(t)

	id := uploadArchive(t, s, "renamed.zip", map[string]string{"modone.pak": "x"})
	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/update_from_archive", game), updateFromArchiveBody(id))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"version_required": true`)

	rec = doAPI(s, http.MethodPost, scoped("/api/v1/plans/update_from_archive", game),
		`{"upload_id":"`+string(id)+`","source_id":"`+fixtureSourceID+`","mod_id":"m1","version":"1.0"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	rec = doAPI(s, http.MethodPost, scoped("/api/v1/plans/update_from_archive", game), `{"upload_id":"`+string(id)+`"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

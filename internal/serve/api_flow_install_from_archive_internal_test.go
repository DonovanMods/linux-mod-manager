package serve

// #535's install from a file, end to end through the entry points the SPA
// drives: the install job that fails because the source refuses the
// download, then POST /api/v1/uploads, POST /api/v1/plans/import_archive
// with the failure's identity, POST /api/v1/jobs - with assertions on the
// END STATE (the DB row, the profile, the deployed tree, the upload).

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

// manualInstallSource is the fixture source plus Map Utils ("mu"), not
// installed, whose author disabled third-party downloads: it lists the
// primary 1.2 build and a Classic flavor's, and refuses every download.
type manualInstallSource struct{ fixtureSource }

func (f *manualInstallSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if modID == "mu" {
		return &domain.Mod{ID: "mu", SourceID: fixtureSourceID, Name: "Map Utils", Version: "1.2", GameID: "g1",
			SourceURL: "https://example.test/mods/mu"}, nil
	}
	return f.fixtureSource.GetMod(ctx, gameID, modID)
}

func (f *manualInstallSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if mod.ID == "mu" {
		return []domain.DownloadableFile{
			{ID: "300", FileName: "MapUtils-1.2.zip", Version: "1.2", Category: "MAIN", IsPrimary: true},
			{ID: "301", FileName: "MapUtils-Classic-1.2.zip", Version: "1.2c", Category: "MAIN"},
		}, nil
	}
	return f.fixtureSource.GetModFiles(ctx, mod)
}

func (*manualInstallSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", &source.ManualDownloadError{Reason: "mod author has disabled third-party downloads"}
}

func newInstallFromArchiveServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)
	svc.RegisterSource(&manualInstallSource{})
	return s, svc, game
}

func installFromArchiveBody(id uploadID, sourceID, modID, fileID string) string {
	return `{"upload_id":"` + string(id) + `","source_id":"` + sourceID + `","mod_id":"` + modID +
		`","install_from_file":true,"expected_file_id":"` + fileID + `"}`
}

// The failed install's envelope carries the identity and the file it
// tried; installing from the uploaded archive with them matches that file,
// installs the mod at its version and file ID, and reclaims the upload.
func TestAPIFlow_InstallFromArchive_CompletesAFailedManualInstall(t *testing.T) {
	s, svc, game := newInstallFromArchiveServer(t)

	failed := runFlow(t, s, game, "install", `{"source_id":"`+fixtureSourceID+`","mod_id":"mu"}`, "")
	require.Equal(t, jobFailed, failed.status().State)
	raw, err := json.Marshal(failed.status().Error.Details)
	require.NoError(t, err)
	var details struct {
		SourceID       string `json:"source_id"`
		ModID          string `json:"mod_id"`
		ManualDownload bool   `json:"manual_download"`
		FileID         string `json:"file_id"`
		FileName       string `json:"file_name"`
	}
	require.NoError(t, json.Unmarshal(raw, &details))
	assert.True(t, details.ManualDownload)
	assert.Equal(t, "300", details.FileID)
	assert.Equal(t, "MapUtils-1.2.zip", details.FileName)

	id := uploadArchive(t, s, "MapUtils-1.2 (1).zip", map[string]string{"maputils.pak": "map bytes"})
	planID, planBody := planFlow(t, s, game, "import_archive", installFromArchiveBody(id, details.SourceID, details.ModID, details.FileID))
	var planned struct {
		Plan core.ImportArchivePlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(planBody, &planned))
	assert.Equal(t, core.ArchiveMatchAdvertised, planned.Plan.Match)
	assert.True(t, planned.Plan.MatchNormalized)
	assert.Equal(t, "1.2", planned.Plan.Mod.Version)

	j := startFlowJob(t, s, planID, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "mu", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.2", row.Version)
	assert.Equal(t, []string{"300"}, row.FileIDs)
	assert.Equal(t, "map bytes", deployedContent(t, game, "maputils.pak"))
	prof, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.NotNil(t, prof.FindRef(fixtureSourceID, "mu"))
	assert.Equal(t, []string{"300"}, prof.FindRef(fixtureSourceID, "mu").FileIDs)

	_, ok := s.uploads.Get(id)
	assert.False(t, ok, "a successful install reclaims the upload")
}

// Another flavor's build fails the job with the install's typed refusal and
// keeps the upload; "Install anyway" (accept_mismatch) completes it.
func TestAPIFlow_InstallFromArchive_MismatchIsAnsweredWithAcceptMismatch(t *testing.T) {
	s, svc, game := newInstallFromArchiveServer(t)
	id := uploadArchive(t, s, "MapUtils-Classic-1.2.zip", map[string]string{"maputils.pak": "classic bytes"})
	body := installFromArchiveBody(id, fixtureSourceID, "mu", "300")

	j := runFlow(t, s, game, "import_archive", body, "")
	require.Equal(t, jobFailed, j.status().State)
	details, err := json.Marshal(j.status().Error.Details)
	require.NoError(t, err)
	assert.JSONEq(t, `{"archive_name":"MapUtils-Classic-1.2.zip","advertised":{"id":"300","file_name":"MapUtils-1.2.zip","version":"1.2"},"matched":{"id":"301","file_name":"MapUtils-Classic-1.2.zip","version":"1.2c"},"install":true}`, string(details))
	_, ok := s.uploads.Get(id)
	require.True(t, ok, "a refused install keeps the upload for the retry")

	j = runFlow(t, s, game, "import_archive", body, `{"accept_mismatch":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "mu", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.2c", row.Version)
	assert.Equal(t, []string{"301"}, row.FileIDs)
}

// The plan's own refusals: an installed mod is a 409 (update it from the
// file instead), and an install from a file names its mod.
func TestAPIPlans_InstallFromArchive_Refusals(t *testing.T) {
	s, _, game := newInstallFromArchiveServer(t)
	id := uploadArchive(t, s, "ModOne-2.0.zip", map[string]string{"modone.pak": "x"})

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/import_archive", game), installFromArchiveBody(id, fixtureSourceID, "m1", ""))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "--from-file")

	rec = doAPI(s, http.MethodPost, scoped("/api/v1/plans/import_archive", game), `{"upload_id":"`+string(id)+`","install_from_file":true}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

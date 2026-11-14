package serve

// #639 over /api/v1: a profile that pins another file of the version a mod
// is installed at is drift. The apply plan says the mod is reinstalled
// because its files changed, and the job reinstalls it with the new file.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapFilesSource is the profiles fixture's source with a second file of
// p1 at the same version, "p1-f2", which deploys Mods/p1-alt.pak.
type swapFilesSource struct {
	*profilesSource
	alt *httptest.Server
}

func newSwapFilesSource(t *testing.T) *swapFilesSource {
	t.Helper()
	s := &swapFilesSource{profilesSource: newProfilesSource(t)}
	s.alt = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipWith("Mods/p1-alt.pak", "p1 alt payload"))
	}))
	t.Cleanup(s.alt.Close)
	return s
}

func (s *swapFilesSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	files, err := s.profilesSource.GetModFiles(ctx, mod)
	if err == nil && mod.ID == "p1" {
		files = append(files, domain.DownloadableFile{ID: "p1-f2", Name: "Alt", FileName: "p1-alt.zip", Category: "MAIN", Size: 64})
	}
	return files, err
}

func (s *swapFilesSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	if fileID == "p1-f2" {
		return s.alt.URL + "/p1-f2", nil
	}
	return s.profilesSource.GetDownloadURL(ctx, mod, fileID)
}

func TestFlowProfileApply_SameVersionFileSwapIsAReinstall(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	svc.RegisterSource(newSwapFilesSource(t))
	p, err := config.LoadProfile(svc.ConfigDir(), game.ID, activeProfile)
	require.NoError(t, err)
	for i := range p.Mods {
		if p.Mods[i].ModID == "p1" {
			p.Mods[i].FileIDs = []string{"p1-f2"}
		}
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))

	id, raw := planFlow(t, s, game, "profile_apply", `{"profile":"`+activeProfile+`"}`)
	var resp struct {
		Plan struct {
			NoChanges bool `json:"no_changes"`
			ToInstall []struct {
				Ref    domain.ModReference `json:"ref"`
				Reason string              `json:"reason"`
			} `json:"to_install"`
		} `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.False(t, resp.Plan.NoChanges)
	require.Len(t, resp.Plan.ToInstall, 1)
	assert.Equal(t, "p1", resp.Plan.ToInstall[0].Ref.ModID)
	assert.Equal(t, "files_changed", resp.Plan.ToInstall[0].Reason)

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State)
	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "p1", game.ID, activeProfile)
	require.NoError(t, err)
	assert.Equal(t, []string{"p1-f2"}, row.FileIDs)
	_, err = os.Lstat(filepath.Join(game.ModPath, profileModFile("p1")))
	assert.True(t, os.IsNotExist(err), "the old file's member is retired")
	_, err = os.Lstat(filepath.Join(game.ModPath, "Mods", "p1-alt.pak"))
	assert.NoError(t, err, "the new file's member is deployed")

	_, raw = planFlow(t, s, game, "profile_apply", `{"profile":"`+activeProfile+`"}`)
	require.NoError(t, json.Unmarshal(raw, &resp))
	assert.True(t, resp.Plan.NoChanges, "converged")
}

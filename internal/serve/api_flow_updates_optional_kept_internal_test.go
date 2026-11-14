package serve

// #643 over /api/v1: the web updates kind runs the same selection as the
// CLI, so a same-version update that supersedes the old main (fileA ->
// fileC) while the source still lists fileA as MAIN keeps the unchanged
// optional (fileB) - in the record and in the deployed tree.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// optionalKeptModID is the three-file mod the test updates.
const optionalKeptModID = "opt1"

// optionalKeptFile is the game-directory path fileID deploys.
func optionalKeptFile(fileID string) string { return optionalKeptModID + "-" + fileID + ".esp" }

// optionalKeptSource lists fileA (the old main, still MAIN), fileB (an
// optional) and fileC (the new primary main), all at 1.0, and its update
// check reports fileA superseded by fileC at the same version.
type optionalKeptSource struct {
	fixtureSource
	server *httptest.Server
}

func newOptionalKeptSource(t *testing.T) *optionalKeptSource {
	t.Helper()
	s := &optionalKeptSource{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileID := strings.TrimPrefix(r.URL.Path, "/")
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipWith(optionalKeptFile(fileID), fileID))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (*optionalKeptSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	if modID != optionalKeptModID {
		return nil, domain.ErrModNotFound
	}
	return &domain.Mod{ID: optionalKeptModID, SourceID: fixtureSourceID, Name: "Optional Kept", Version: "1.0", GameID: "g1"}, nil
}

func (*optionalKeptSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if mod.ID != optionalKeptModID {
		return nil, domain.ErrModNotFound
	}
	return []domain.DownloadableFile{
		{ID: "fileA", Name: "A", FileName: "a.zip", Version: "1.0", Category: "MAIN", Size: 32},
		{ID: "fileB", Name: "B", FileName: "b.zip", Version: "1.0", Category: "OPTIONAL", Size: 32},
		{ID: "fileC", Name: "C", FileName: "c.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32},
	}, nil
}

func (s *optionalKeptSource) GetDownloadURL(_ context.Context, _ *domain.Mod, fileID string) (string, error) {
	return s.server.URL + "/" + fileID, nil
}

func (*optionalKeptSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var out []domain.Update
	for _, im := range installed {
		if im.ID == optionalKeptModID && slices.Contains(im.FileIDs, "fileA") {
			out = append(out, domain.Update{InstalledMod: im, NewVersion: "1.0", FileIDReplacements: map[string]string{"fileA": "fileC"}})
		}
	}
	return out, nil
}

func TestFlowUpdates_SameVersionKeepsTheUnchangedOptional(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	svc.RegisterSource(newOptionalKeptSource(t))
	ctx := t.Context()

	// Install the mod from {fileA, fileB} the way a profile pins it.
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default", domain.ModReference{
		SourceID: fixtureSourceID, ModID: optionalKeptModID, Version: "1.0", FileIDs: []string{"fileA", "fileB"},
	}))
	ap, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	_, err = svc.ApplyProfileApply(ctx, game, ap, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, "fileA", deployedContent(t, game, optionalKeptFile("fileA")))
	require.Equal(t, "fileB", deployedContent(t, game, optionalKeptFile("fileB")))

	j := runFlow(t, s, game, "updates", `{"mods":["`+fixtureSourceID+`:`+optionalKeptModID+`"]}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	row, err := svc.GetInstalledMod(ctx, fixtureSourceID, optionalKeptModID, game.ID, "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"fileC", "fileB"}, row.FileIDs, "the superseded main is replaced; the unchanged optional keeps itself")
	profile, err := svc.NewProfileManager().Get(ctx, game.ID, "default")
	require.NoError(t, err)
	ref := profile.FindRef(fixtureSourceID, optionalKeptModID)
	require.NotNil(t, ref)
	assert.ElementsMatch(t, []string{"fileC", "fileB"}, ref.FileIDs)

	_, err = os.Lstat(filepath.Join(game.ModPath, optionalKeptFile("fileA")))
	assert.True(t, os.IsNotExist(err), "the superseded main is retired")
	assert.Equal(t, "fileB", deployedContent(t, game, optionalKeptFile("fileB")), "the unchanged optional stays deployed")
	assert.Equal(t, "fileC", deployedContent(t, game, optionalKeptFile("fileC")), "the new main is deployed")
}

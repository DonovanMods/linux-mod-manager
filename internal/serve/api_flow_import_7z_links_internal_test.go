package serve

// #658: the web UI's archive import refuses a .7z holding a symbolic link
// at plan time, as `lmm import` does, with nothing written.

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIFlow_ImportArchive_RefusesA7zWithASymlinkMember(t *testing.T) {
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	s, svc, game := newFlowFixtureServer(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "mymod.esp"), []byte("mod bytes"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(src, "link.txt")))
	archive := filepath.Join(dir, "MyMod-1.0.7z")
	cmd := exec.Command("7z", "a", "-snl", archive, ".")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building fixture .7z: %s", out)
	data, err := os.ReadFile(archive)
	require.NoError(t, err)
	before, err := os.ReadDir(game.ModPath)
	require.NoError(t, err)
	modsBefore, err := svc.GetInstalledMods(t.Context(), game.ID, "default")
	require.NoError(t, err)

	rec := postUpload(t, s, "MyMod-1.0.7z", data)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	id := decodeUpload(t, rec.Body.Bytes()).UploadID

	rec = doAPI(s, http.MethodPost, scoped("/api/v1/plans/import_archive", game), archivePlanBody(id))
	assert.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "link.txt")
	assert.Contains(t, rec.Body.String(), "is a link")

	after, err := os.ReadDir(game.ModPath)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after), "nothing deployed")
	mods, err := svc.GetInstalledMods(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Len(t, mods, len(modsBefore), "nothing recorded")
}

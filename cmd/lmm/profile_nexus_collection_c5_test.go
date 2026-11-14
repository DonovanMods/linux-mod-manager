package main

// #610 C5: the CLI's plan for a NexusMods collection with an exact file
// list, a patch and bundled files - each listed before anything is
// written - and the code gate on a patch to a DLL.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// c5PatchBytes is a BSDIFF40 header and nothing it could apply: the plan
// reads only its format and its sha256.
var c5PatchBytes = append([]byte("BSDIFF40"), make([]byte, 24)...)

// nexusCollectionServiceWithTrees is nexusCollectionServiceFor with the
// recipe archive also holding the files fill writes beside collection.json.
func nexusCollectionServiceWithTrees(t *testing.T, recipe string, fill func(dir string)) (*core.Service, *domain.Game) {
	t.Helper()
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.json"), []byte(recipe), 0o644))
	fill(dir)
	archive := filepath.Join(t.TempDir(), "recipe.7z")
	cmd := exec.Command("7z", "a", "-bd", archive, ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	body, err := os.ReadFile(archive)
	require.NoError(t, err)
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(cdn.Close)
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: cdn.Client()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&nexusCollectionDouble{archiveURL: cdn.URL + "/recipe.7z"})
	game := &domain.Game{ID: "stardew", Name: "Stardew Valley", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"nexusmods": "stardewvalley"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

func c5Recipe(patched string) string {
	return `{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact",
	 "FileList": [{"Path": "Ten\\a.json", "MD5": "0cc175b9c0f1b6a831c399e269772661"}, {"Path": "Ten/b.json", "MD5": "92eb5ffee6ae2fec3ad71c777531578f"}]},
	{"Name": "Twelve", "Version": "2.0", "SourceType": "nexus", "ModID": "12", "FileID": "120", "UpdatePolicy": "exact",
	 "Patches": {"` + patched + `": "CABDDFC4"}},
	{"Name": "Marker", "Version": "1.0.0", "SourceType": "bundle", "UpdatePolicy": "exact", "FileExpression": "Bundled - Marker (v1.0.0)"}]}`
}

func c5Fill(t *testing.T, patched string) func(dir string) {
	return func(dir string) {
		for rel, data := range map[string][]byte{
			"bundled/Bundled - Marker (v1.0.0)/[CP] Marker/manifest.json": []byte(`{"Name": "Marker"}`),
			"patches/Twelve/" + patched + ".diff":                         c5PatchBytes,
		} {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, data, 0o644))
		}
	}
}

func TestDoNexusCollectionImport_FileListPatchesAndBundles(t *testing.T) {
	svc, game := nexusCollectionServiceWithTrees(t, c5Recipe(`Twelve\\content.json`), c5Fill(t, "Twelve/content.json"))
	setNexusFlags(t, "abcdef", false)
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "requires: bundled, file_list, patches")
	assert.Contains(t, stdout, "⇣ Ten 1.0 (exact file list of 2)")
	assert.Contains(t, stdout, "⇣ Twelve 2.0 (old version file; 1 patch(es))")
	assert.Contains(t, stdout, "1 bundled in the collection, will be installed")
	assert.Contains(t, stdout, "▣ Marker 1.0.0 (bundled in the collection)")
	assert.Contains(t, stdout, "Exact file lists and patches")
	assert.Contains(t, stdout, "installs exactly 2 listed file(s)")
	assert.Contains(t, stdout, `patches Twelve\content.json (baseline CRC32 CABDDFC4)`)
	assert.Contains(t, stdout, "Files the collection's author bundled")
	assert.NotContains(t, stdout, "cannot import")
	assert.Contains(t, stdout, "✓ Imported profile: Three Mods")

	// A free account downloads nothing, and the bundled item - which the
	// collection itself carries - installs all the same.
	data, err := os.ReadFile(filepath.Join(game.ModPath, "[CP] Marker", "manifest.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"Name": "Marker"}`, string(data))
}

func TestDoNexusCollectionImport_APatchToCodeNeedsAllowAnyOverrides(t *testing.T) {
	svc, game := nexusCollectionServiceWithTrees(t, c5Recipe(`Twelve\\Twelve.dll`), c5Fill(t, "Twelve/Twelve.dll"))
	setNexusFlags(t, "abcdef", false)
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrCodePatch)
	assert.Contains(t, stdout, `patches Twelve\Twelve.dll (baseline CRC32 CABDDFC4) - code the system runs (.dll)`)
	assert.Contains(t, stdout, "1 patch(es) change code: the import is refused unless you re-run with --allow-any-overrides")
	_, err = svc.NewProfileManager().Get(context.Background(), game.ID, "Three Mods")
	assert.Error(t, err, "nothing was written")

	old := profileImportAnyOverrides
	profileImportAnyOverrides = true
	t.Cleanup(func() { profileImportAnyOverrides = old })
	stdout, _, err = captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "1 patch(es) change code and will be applied (--allow-any-overrides)")
}

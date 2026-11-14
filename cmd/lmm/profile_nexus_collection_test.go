package main

// #607: `lmm profile import --nexus-collection <ref>` - the CLI's half of a
// NexusMods collection import. The source is a double (cmd/lmm may not
// import a concrete source): it resolves a canned list, links a recipe a
// test server serves, and reports a free account, so the plan marks every
// item a manual download and the apply downloads nothing.

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nexusCollectionDouble is a source with one collection, abcdef rev 3.
type nexusCollectionDouble struct {
	goldenSearchSource
	archiveURL string
}

func (*nexusCollectionDouble) ID() string   { return "nexusmods" }
func (*nexusCollectionDouble) Name() string { return "NexusMods" }

func (*nexusCollectionDouble) ResolveGameCollection(_ context.Context, gameSourceID, _ string) (source.Collection, error) {
	return source.Collection{ID: "abcdef", Name: "Three Mods", Revision: 3, Author: "curator",
		URL: "https://www.nexusmods.com/games/" + gameSourceID + "/collections/abcdef", Handle: "/h",
		Items: []source.CollectionItem{
			{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Ten", FileCategory: "MAIN",
				URL: "https://www.nexusmods.com/" + gameSourceID + "/mods/10?tab=files&file_id=100"},
			{Kind: source.CollectionItemModFile, ModID: "12", FileID: "120", Name: "Twelve", FileCategory: "OLD_VERSION"},
		}}, nil
}

func (d *nexusCollectionDouble) ManifestArchiveURL(context.Context, source.Collection) (string, error) {
	return d.archiveURL, nil
}

func (*nexusCollectionDouble) ParseManifest(root fs.FS) (source.CollectionManifest, error) {
	data, err := fs.ReadFile(root, "collection.json")
	if err != nil {
		return source.CollectionManifest{}, err
	}
	var m source.CollectionManifest
	return m, json.Unmarshal(data, &m)
}

func (*nexusCollectionDouble) AccountTier(context.Context) (source.Tier, error) {
	return source.TierFree, nil
}

const nexusDoubleRecipe = `{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact"},
	{"Name": "Twelve", "Version": "2.0", "SourceType": "nexus", "ModID": "12", "FileID": "120", "UpdatePolicy": "latest", "Optional": true}],
	"Tools": [{"Name": "SSEEdit", "Exe": "Data\\SSEEdit.exe"}]}`

// nexusCollectionService is a Service with the double registered and a
// Skyrim game mapped to it.
func nexusCollectionService(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.json"), []byte(nexusDoubleRecipe), 0o644))
	archive := filepath.Join(t.TempDir(), "recipe.7z")
	cmd := exec.Command("7z", "a", "-bd", archive, "collection.json")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	body, err := os.ReadFile(archive)
	require.NoError(t, err)
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(cdn.Close)

	configDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: cdn.Client()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&nexusCollectionDouble{archiveURL: cdn.URL + "/recipe.7z"})
	game := &domain.Game{ID: "skyrimse", Name: "Skyrim Special Edition", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game, configDir
}

func setNexusFlags(t *testing.T, ref string, withOptional bool) {
	t.Helper()
	oldRef, oldOpt := profileImportNexus, profileImportWithOptional
	profileImportNexus, profileImportWithOptional = ref, withOptional
	t.Cleanup(func() { profileImportNexus, profileImportWithOptional = oldRef, oldOpt })
}

func TestDoNexusCollectionImport_FreeAccountPlansAndSavesThePending(t *testing.T) {
	svc, game, _ := nexusCollectionService(t)
	setNexusFlags(t, "abcdef", false)
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "Collection: Three Mods (version 3)")
	assert.Contains(t, stdout, "2 item(s) at revision 3: 1 manual download, 1 optional skipped")
	assert.Contains(t, stdout, "⇣ Ten 1.0")
	assert.Contains(t, stdout, "https://www.nexusmods.com/skyrimspecialedition/mods/10?tab=files&file_id=100", "a manual download links its file's page")
	assert.Contains(t, stdout, "○ Twelve 2.0 (optional; old version file)")
	assert.Contains(t, stdout, "--with-optional installs them")
	assert.Contains(t, stdout, "SSEEdit: Data\\SSEEdit.exe")
	assert.Contains(t, stdout, "manual downloads: not installed or deployed")
	assert.NotContains(t, stdout, "Download and install mods?", "a free account is never asked to download")
	assert.Contains(t, stdout, "✓ Imported profile: Three Mods")

	saved, err := svc.NewProfileManager().Get(context.Background(), game.ID, "Three Mods")
	require.NoError(t, err)
	require.Len(t, saved.Mods, 1)
	assert.Equal(t, "nexusmods:abcdef", saved.Collection.ID)
}

func TestDoNexusCollectionImport_WithOptional(t *testing.T) {
	svc, game, _ := nexusCollectionService(t)
	setNexusFlags(t, "abcdef", true)
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "⇣ Twelve 2.0")
	assert.Contains(t, stdout, "curator asks for the latest file")
	saved, err := svc.NewProfileManager().Get(context.Background(), game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Len(t, saved.Mods, 2)
}

func TestDoNexusCollectionImport_JSON(t *testing.T) {
	svc, game, _ := nexusCollectionService(t)
	setNexusFlags(t, "abcdef", false)
	out := runJSONCommand(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	var got core.CollectionImportResult
	decodeStrict(t, out, &got)
	require.NotNil(t, got.Collection.Source)
	assert.True(t, got.Collection.Source.ManualDownload)
	assertJSONCLIGolden(t, "profile_import_nexus_collection", out)
}

func TestRunProfileImport_NexusCollectionFlags(t *testing.T) {
	setNexusFlags(t, "abcdef", false)
	err := runProfileImport(profileImportCmd, []string{"some.yaml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot also take a file path")

	setNexusFlags(t, "", true)
	err = runProfileImport(profileImportCmd, []string{"some.yaml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "use it with --nexus-collection")
}

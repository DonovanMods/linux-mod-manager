package main

// #634: a mod whose files each carry their own FOMOD installer, through the
// CLI - each file's question is labelled and answered under its own key,
// and `lmm mod fomod --file` (or a pick at a terminal) changes one file's.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cliPatchConfig = `<config><moduleName>Patch Installer</moduleName>
<installSteps order="Explicit"><installStep name="Patches"><optionalFileGroups order="Explicit">
<group name="Which" type="SelectExactlyOne"><plugins order="Explicit">
<plugin name="USSEP"><files><file source="p\ussep.esp" destination="bugs_patch.esp"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
<plugin name="Vanilla"><files><file source="p\vanilla.esp" destination="bugs_patch.esp"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep></installSteps></config>`

// setupPerFileInstall serves mod "bugs" as two installer files and selects
// both, as `lmm install --file main,patch` does.
func setupPerFileInstall(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, src := setupFomodInstall(t)
	src.AddMod(&domain.Mod{ID: "bugs", SourceID: "test-src", Name: "Scrambled Bugs", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{
			{ID: "main", Name: "Main", FileName: "main.zip", IsPrimary: true},
			{ID: "patch", Name: "Patches", FileName: "patch.zip"},
		})
	src.AddDownload("main", cliFomodZip(t))
	path := filepath.Join(t.TempDir(), "patch.zip")
	createTestArchive(t, path, map[string]string{
		"fomod/ModuleConfig.xml": cliPatchConfig,
		"p/ussep.esp":            "USSEP",
		"p/vanilla.esp":          "VANILLA",
	})
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	src.AddDownload("patch", b)
	installModID, installFileID = "bugs", "main,patch"
	return svc, game
}

func TestDoInstall_PerFileQuestionsAreLabelledAndKeyedByFile(t *testing.T) {
	svc, game := setupPerFileInstall(t)

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 2)
	stderr := captureStderr(t, func() { reportError(err) })
	assert.Contains(t, stderr, "Scrambled Bugs - Main (test-src:bugs#main): CLI Installer")
	assert.Contains(t, stderr, "Scrambled Bugs - Patches (test-src:bugs#patch): Patch Installer")

	// --fomod-choices keyed by file answers each installer.
	choices := filepath.Join(t.TempDir(), "choices.json")
	require.NoError(t, os.WriteFile(choices, []byte(`{
		"test-src:bugs#main": [{"name":"Size","groups":[{"name":"Texture","choices":[{"name":"2K","idx":0}]}]}],
		"test-src:bugs#patch": [{"name":"Patches","groups":[{"name":"Which","choices":[{"name":"Vanilla","idx":1}]}]}]
	}`), 0o644))
	fomodChoicesPath = choices
	out, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err, out)
	assert.Equal(t, "2K", deployedFile(t, game, "textures/t.dds"))
	assert.Equal(t, "VANILLA", deployedFile(t, game, "bugs_patch.esp"))

	p, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	ref := p.FindRef("test-src", "bugs")
	require.NotNil(t, ref)
	assert.Len(t, ref.FomodFiles, 2)
	assert.True(t, ref.Fomod.IsZero())
}

// TestDoInstall_PerFileWalkAsksEachFileUnderItsName: at a terminal, each
// file's installer is walked in turn, headed by its file.
func TestDoInstall_PerFileWalkAsksEachFileUnderItsName(t *testing.T) {
	svc, game := setupPerFileInstall(t)
	terminalInput("1", "2") // 2K for the main file; Vanilla for the patch

	out, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err, out)
	assert.Contains(t, out, "Scrambled Bugs - Main: CLI Installer")
	assert.Contains(t, out, "Scrambled Bugs - Patches: Patch Installer")
	assert.Equal(t, "VANILLA", deployedFile(t, game, "bugs_patch.esp"))
}

func TestDoModFomod_PerFilePicksWhichInstaller(t *testing.T) {
	svc, game := setupPerFileInstall(t)
	fomodDefaults = true
	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	require.Equal(t, "USSEP", deployedFile(t, game, "bugs_patch.esp"))
	fomodDefaults = false
	oldSource, oldFile := modSource, modFomodFile
	modSource = "test-src"
	t.Cleanup(func() { modSource, modFomodFile = oldSource, oldFile })

	// mod show names the per-file installers.
	out, err := captureStdoutErr(t, func() error { return doModShow(context.Background(), svc, game, "bugs") })
	require.NoError(t, err, out)
	assert.Contains(t, out, "one per file (2 files)")

	// --file asks only that file's installer.
	modFomodFile = "patch"
	_, err = captureStdoutErr(t, func() error { return doModFomod(context.Background(), svc, game, "bugs") })
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	assert.Equal(t, "patch", q.Mods[0].FileID)

	// At a terminal, without --file, the user picks one from a list.
	modFomodFile = ""
	terminalInput("2", "2") // the Patches file; then Vanilla
	out, err = captureStdoutErr(t, func() error { return doModFomod(context.Background(), svc, game, "bugs") })
	require.NoError(t, err, out)
	assert.Contains(t, out, "Scrambled Bugs has 2 installers, one per file:")
	assert.Contains(t, out, "2) Patches (file patch)")
	assert.NotContains(t, out, "CLI Installer", "the other file's installer is not asked")
	assert.Equal(t, "VANILLA", deployedFile(t, game, "bugs_patch.esp"))
	assert.Equal(t, "2K", deployedFile(t, game, "textures/t.dds"), "the main file kept its choices")

	// A file that is not one of its installers is refused.
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	modFomodFile = "nope"
	_, err = captureStdoutErr(t, func() error { return doModFomod(context.Background(), svc, game, "bugs") })
	require.Error(t, err)
	assert.Contains(t, err.Error(), `file "nope" is not one of its installers (main, patch)`)
}

func TestParseFomodChoicesFile_PerFileKeys(t *testing.T) {
	keyed, single, err := parseFomodChoicesFile([]byte(`{"nexusmods:1#10":[],"nexusmods:1#11":{"config_sha256":"ab","steps":[]}}`))
	require.NoError(t, err)
	assert.Nil(t, single)
	assert.Equal(t, "ab", keyed["nexusmods:1#11"].ConfigSHA256)
	b, err := json.Marshal(keyed)
	require.NoError(t, err)
	assert.Contains(t, string(b), "nexusmods:1#10")
}

package main

// #647: `lmm install`'s picker asks for a version first when the mod's files
// span more than one, then offers only that version's files - the web
// picker's shape (#637). A --file list naming files of two versions is
// refused by core, with the versions and their files in the message and in
// the --json envelope's details.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoVersionFiles is a mod with a main file and a patch at 2.0 and at 1.0,
// in the order a source lists them (the newest first).
func twoVersionFiles() []domain.DownloadableFile {
	return []domain.DownloadableFile{
		{ID: "main2", Name: "Main", FileName: "main-2.0.esp", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 10},
		{ID: "patch2", Name: "Patch", FileName: "patch-2.0.esp", Version: "2.0", Category: "OPTIONAL", Size: 10},
		{ID: "main1", Name: "Main", FileName: "main-1.0.esp", Version: "1.0", Category: "MAIN", Size: 10},
		{ID: "patch1", Name: "Patch", FileName: "patch-1.0.esp", Version: "1.0", Category: "OPTIONAL", Size: 10},
	}
}

// setPickerFlags puts the install flags in the interactive shape: no
// --file, no --yes.
func setPickerFlags(t *testing.T) {
	t.Helper()
	oldFile, oldYes := installFileID, installYes
	installFileID, installYes = "", false
	t.Cleanup(func() { installFileID, installYes = oldFile, oldYes })
}

func selectedIDs(sel []*domain.DownloadableFile) []string {
	ids := make([]string, len(sel))
	for i, f := range sel {
		ids[i] = f.ID
	}
	return ids
}

func TestSelectInstallFiles_AsksForVersionFirst(t *testing.T) {
	setPickerFlags(t)

	var selected []*domain.DownloadableFile
	out := captureStdout(t, func() error {
		var err error
		// Version 2 in the list (1.0), then both of its files.
		selected, err = selectInstallFilesFrom(strings.NewReader("2\n1,2\n"), twoVersionFiles(), nil)
		return err
	})

	assert.Equal(t, []string{"main1", "patch1"}, selectedIDs(selected))
	versions, files, ok := strings.Cut(out, "Available files:")
	require.True(t, ok, out)
	assert.Contains(t, versions, "Available versions:")
	assert.Contains(t, versions, "[1] 2.0 (2 files) <- default", "the primary file's version is the default")
	assert.Contains(t, versions, "[2] 1.0 (2 files)")
	assert.Contains(t, files, "main-1.0.esp")
	assert.NotContains(t, files, "main-2.0.esp", "only the chosen version's files are offered")
	assert.NotContains(t, files, "patch-2.0.esp")
}

// TestSelectInstallFiles_VersionDefaultIsThePrimarysVersion: Enter at both
// prompts installs what it always did, the primary file.
func TestSelectInstallFiles_VersionDefaultIsThePrimarysVersion(t *testing.T) {
	setPickerFlags(t)
	var selected []*domain.DownloadableFile
	captureStdout(t, func() error {
		var err error
		selected, err = selectInstallFilesFrom(strings.NewReader("\n\n"), twoVersionFiles(), nil)
		return err
	})
	assert.Equal(t, []string{"main2"}, selectedIDs(selected))
}

// TestSelectInstallFiles_VersionWithOneFileNeedsNoFilePrompt: a version
// with a single file is that file.
func TestSelectInstallFiles_VersionWithOneFileNeedsNoFilePrompt(t *testing.T) {
	setPickerFlags(t)
	files := append(twoVersionFiles()[:2], domain.DownloadableFile{ID: "old", FileName: "main-0.9.esp", Version: "0.9", Category: "OLD_VERSION"})
	var selected []*domain.DownloadableFile
	out := captureStdout(t, func() error {
		var err error
		selected, err = selectInstallFilesFrom(strings.NewReader("2\n"), files, nil)
		return err
	})
	assert.Equal(t, []string{"old"}, selectedIDs(selected))
	assert.Contains(t, out, "[2] 0.9 (1 file)")
	assert.NotContains(t, out, "Available files:")
}

// TestSelectInstallFiles_VersionPromptTakesOneVersion: naming two versions
// is not a choice; the prompt says so and asks again.
func TestSelectInstallFiles_VersionPromptTakesOneVersion(t *testing.T) {
	setPickerFlags(t)
	var selected []*domain.DownloadableFile
	out := captureStdout(t, func() error {
		var err error
		selected, err = selectInstallFilesFrom(strings.NewReader("1,2\n2\n1\n"), twoVersionFiles(), nil)
		return err
	})
	assert.Contains(t, out, "Invalid selection: pick one version")
	assert.Equal(t, []string{"main1"}, selectedIDs(selected))
}

// TestSelectInstallFiles_NoVersionIsAVersionOfItsOwn mirrors the web
// picker: files a source gives no version are listed under "(no version)".
func TestSelectInstallFiles_NoVersionIsAVersionOfItsOwn(t *testing.T) {
	setPickerFlags(t)
	files := []domain.DownloadableFile{
		{ID: "v", FileName: "main-2.0.esp", Version: "2.0", IsPrimary: true},
		{ID: "bare", FileName: "extras.esp"},
	}
	var selected []*domain.DownloadableFile
	out := captureStdout(t, func() error {
		var err error
		selected, err = selectInstallFilesFrom(strings.NewReader("2\n"), files, nil)
		return err
	})
	assert.Contains(t, out, "[2] (no version) (1 file)")
	assert.Equal(t, []string{"bare"}, selectedIDs(selected))
}

// TestSelectInstallFiles_YesSkipsTheVersionStep: --yes installs the
// primary file without asking anything, as before.
func TestSelectInstallFiles_YesSkipsTheVersionStep(t *testing.T) {
	setPickerFlags(t)
	installYes = true
	selected, err := selectInstallFilesFrom(poisonReader{t}, twoVersionFiles(), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"main2"}, selectedIDs(selected))
}

// TestSelectInstallFiles_JSONRefusesTheVersionPrompt: under --json the
// version prompt is a confirmation like any other.
func TestSelectInstallFiles_JSONRefusesTheVersionPrompt(t *testing.T) {
	setPickerFlags(t)
	withJSONOutput(t)
	selected, err := selectInstallFilesFrom(poisonReader{t}, twoVersionFiles(), nil)
	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	assert.Nil(t, selected)
}

// setupTwoVersionInstall serves mod1 with twoVersionFiles.
func setupTwoVersionInstall(t *testing.T) (*core.Service, *domain.Game, *fakeInstallSource) {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"}, twoVersionFiles())
	for _, f := range twoVersionFiles() {
		src.AddDownload(f.ID, []byte(f.ID+" content"))
	}
	return svc, game, src
}

// TestDoInstall_PickerInstallsOneVersion drives `lmm install` at a
// terminal: version 1.0, both of its files. The row records 1.0 and holds
// exactly those files.
func TestDoInstall_PickerInstallsOneVersion(t *testing.T) {
	svc, game, _ := setupTwoVersionInstall(t)
	installYes = false
	stdinLines(t, "2", "1,2")

	out, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err, out)
	assert.Contains(t, out, "Available versions:")

	row, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", row.Version)
	assert.ElementsMatch(t, []string{"main1", "patch1"}, row.FileIDs)
}

// TestDoInstall_FileFlagMixedVersionsRefused: `--file main2,patch1` names
// files of 2.0 and 1.0. The install is refused with core's message, a
// non-zero exit, nothing downloaded, and the --json envelope carries the
// code and each version's files.
func TestDoInstall_FileFlagMixedVersionsRefused(t *testing.T) {
	svc, game, src := setupTwoVersionInstall(t)
	installFileID = "main2,patch1"

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	var mixed *core.MixedVersionSelectionError
	require.True(t, errors.As(err, &mixed), "got %v", err)
	assert.Equal(t, "these files belong to versions 2.0 (main-2.0.esp) and 1.0 (patch-1.0.esp); pick one version", err.Error())
	assert.Equal(t, exitError, exitCodeFor(err))
	assert.Zero(t, src.served.Load(), "nothing is downloaded")
	_, dbErr := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	assert.ErrorIs(t, dbErr, domain.ErrModNotFound)

	stderr := captureStderr(t, func() { reportError(err) })
	assert.Contains(t, stderr, "Error: these files belong to versions 2.0 (main-2.0.esp) and 1.0 (patch-1.0.esp); pick one version")
}

// TestReportError_JSON_MixedVersionSelectionError pins the refusal's
// --json envelope.
func TestReportError_JSON_MixedVersionSelectionError(t *testing.T) {
	withJSONOutput(t)
	err := &core.MixedVersionSelectionError{Versions: []core.MixedVersion{
		{Version: "2.0", Files: []core.MixedVersionFile{{ID: "main2", FileName: "main-2.0.esp"}}},
		{Version: "1.0", Files: []core.MixedVersionFile{{ID: "patch1", FileName: "patch-1.0.esp"}, {ID: "x1"}}},
	}}
	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"these files belong to versions 2.0 (main-2.0.esp) and 1.0 (patch-1.0.esp, x1); pick one version\",\n"+
		"  \"details\": {\n"+
		"    \"code\": \"mixed_version_files\",\n"+
		"    \"versions\": [\n"+
		"      {\n"+
		"        \"version\": \"2.0\",\n"+
		"        \"files\": [\n"+
		"          {\n"+
		"            \"id\": \"main2\",\n"+
		"            \"file_name\": \"main-2.0.esp\"\n"+
		"          }\n"+
		"        ]\n"+
		"      },\n"+
		"      {\n"+
		"        \"version\": \"1.0\",\n"+
		"        \"files\": [\n"+
		"          {\n"+
		"            \"id\": \"patch1\",\n"+
		"            \"file_name\": \"patch-1.0.esp\"\n"+
		"          },\n"+
		"          {\n"+
		"            \"id\": \"x1\"\n"+
		"          }\n"+
		"        ]\n"+
		"      }\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
}

// stdinLines feeds lines to os.Stdin for the rest of the test.
func stdinLines(t *testing.T, lines ...string) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(strings.Join(lines, "\n") + "\n")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; _ = r.Close() })
}

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for `lmm install --id <id> -s <source> --from-file <archive>` (#535).

// setupInstallFromFileTest is setupDoInstallTest with Mod One - its 2.0
// build (file "new-1", primary) and a Fabric build ("new-2", 2.0f) - on a
// source that refuses every download, and the --from-file flags reset
// around the test.
func setupInstallFromFileTest(t *testing.T) (*core.Service, *domain.Game, *fakeInstallSource, string) {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1",
		SourceURL: "https://example.test/mods/mod1"},
		[]domain.DownloadableFile{
			{ID: "new-1", FileName: "mod1-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true},
			{ID: "new-2", FileName: "mod1-fabric-2.0.zip", Version: "2.0f", Category: "MAIN"},
		})
	src.downloadURLErr = &source.ManualDownloadError{Reason: "the author has turned off API downloads"}

	oldFrom, oldAccept, oldGame := installFromFile, installAcceptMismatch, gameID
	t.Cleanup(func() { installFromFile, installAcceptMismatch, gameID = oldFrom, oldAccept, oldGame })
	installFromFile, installAcceptMismatch, gameID = "", false, ""
	return svc, game, src, t.TempDir()
}

func writeInstallArchive(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	createTestArchive(t, path, map[string]string{"mod1.esp": "mod1-content"})
	return path
}

func TestJSONGolden_InstallFromFileResult(t *testing.T) {
	svc, game, _, dir := setupInstallFromFileTest(t)
	installFromFile = writeInstallArchive(t, dir, "mod1-2.0 (1).zip")
	installFileID = "new-1"
	withJSONOutput(t)

	out := captureStdout(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	assertJSONCLIGolden(t, "install_from_file_result", out)
}

func TestDoInstall_FromFile_Installs(t *testing.T) {
	svc, game, _, dir := setupInstallFromFileTest(t)
	installFromFile = writeInstallArchive(t, dir, "mod1-2.0.zip")

	out := captureStdout(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	assert.Contains(t, out, "Installing from file: "+installFromFile)
	assert.Contains(t, out, "Matches the file lmm would install: mod1-2.0.zip (file new-1)")
	assert.Contains(t, out, "✓ Installed: Mod One 2.0")
	assert.Contains(t, out, "Added to profile: default")

	row, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.Equal(t, []string{"new-1"}, row.FileIDs)
}

func TestDoInstall_FromFile_MismatchRefusedUnlessAccepted(t *testing.T) {
	svc, game, _, dir := setupInstallFromFileTest(t)
	installFromFile = writeInstallArchive(t, dir, "mod1-fabric-2.0.zip")

	var err error
	stderr, _ := captureStderrErr(t, func() error {
		captureStdout(t, func() error { err = doInstall(context.Background(), svc, game, nil); return nil })
		return nil
	})
	var mismatch *core.ArchiveMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Contains(t, err.Error(), "install from it anyway with --accept-mismatch")
	assert.Contains(t, stderr, "Warning: mod1-fabric-2.0.zip is the source's file mod1-fabric-2.0.zip, not the file lmm would install (mod1-2.0.zip)")
	_, gerr := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.ErrorIs(t, gerr, domain.ErrModNotFound)

	installAcceptMismatch = true
	captureStdout(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	row, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0f", row.Version)
	assert.Equal(t, []string{"new-2"}, row.FileIDs)
}

// Installing from a file installs that one mod: each dependency it needs
// that is not installed is printed as the command that installs it.
func TestDoInstall_FromFile_PrintsACommandPerUnmetDependency(t *testing.T) {
	svc, game, src, dir := setupInstallFromFileTest(t)
	src.mods["mod1"].Dependencies = []domain.ModReference{{SourceID: "test-src", ModID: "lib1"}}
	src.AddMod(&domain.Mod{ID: "lib1", SourceID: "test-src", Name: "Lib One", Version: "1.0", GameID: "g1"}, nil)
	installFromFile = writeInstallArchive(t, dir, "mod1-2.0.zip")
	gameID = "g1"

	out := captureStdout(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	assert.Contains(t, out, "lmm install --id lib1 -s test-src -g g1")
	assert.Contains(t, out, "Lib One")
}

// A failed manual-download install ends with the command that finishes it
// from the downloaded file, every ID filled in.
func TestDoInstall_ManualDownloadFailure_PrintsTheFromFileCommand(t *testing.T) {
	svc, game, _, _ := setupInstallFromFileTest(t)
	installProfile = "default"

	var err error
	captureStdout(t, func() error { err = doInstall(context.Background(), svc, game, nil); return nil })
	require.Error(t, err)

	out, _ := captureStderrErr(t, func() error { reportError(err); return nil })
	assert.Contains(t, out, "Download it manually from: https://example.test/mods/mod1")
	assert.Contains(t, out, "lmm install --id mod1 -s test-src -p default --file new-1 --from-file <downloaded-file>")
	assert.NotContains(t, out, "lmm import", "the install's own command, not the generic import")
}

func TestCheckInstallFromFileFlags(t *testing.T) {
	_, _, _, _ = setupInstallFromFileTest(t)

	installAcceptMismatch = true
	require.ErrorContains(t, checkInstallFromFileFlags(nil), "--accept-mismatch only applies with --from-file")
	installAcceptMismatch = false

	installFromFile = "x.zip"
	require.ErrorContains(t, checkInstallFromFileFlags([]string{"query"}), "no search query")
	installModID = ""
	require.ErrorContains(t, checkInstallFromFileFlags(nil), "--id")
	installModID = "mod1"
	installFileID = "a,b"
	require.ErrorContains(t, checkInstallFromFileFlags(nil), "one file")
	installFileID = "a"
	require.NoError(t, checkInstallFromFileFlags(nil))
}

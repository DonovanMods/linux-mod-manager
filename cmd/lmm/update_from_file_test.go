package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for `lmm update <mod-id> --from-file <archive>` (#530).

// setupUpdateFromFileTest is setupDoUpdateTest with an installed Mod One
// 1.0 (file "old-1") whose source advertises 2.0 as file "new-1"
// (mod1-2.0.zip), and the --from-file flags reset around the test.
func setupUpdateFromFileTest(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	svc, game, src := setupDoUpdateTest(t)
	seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{
			{ID: "old-1", FileName: "mod1-1.0.zip", Version: "1.0"},
			{ID: "new-1", FileName: "mod1-2.0.zip", Version: "2.0"},
			{ID: "new-2", FileName: "mod1-fabric-2.0.zip", Version: "2.0"},
		})
	src.replacements["mod1"] = map[string]string{"old-1": "new-1"}

	oldFrom, oldVersion, oldAccept := updateFromFile, updateVersion, updateAcceptMismatch
	t.Cleanup(func() { updateFromFile, updateVersion, updateAcceptMismatch = oldFrom, oldVersion, oldAccept })
	updateVersion, updateAcceptMismatch = "", false

	dir := t.TempDir()
	return svc, game, dir
}

func writeFromFileArchive(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	createTestArchive(t, path, map[string]string{"mod1-new.esp": "new-content"})
	return path
}

func TestJSONGolden_UpdateFromFilePlan(t *testing.T) {
	svc, game, dir := setupUpdateFromFileTest(t)
	updateFromFile = writeFromFileArchive(t, dir, "mod1-2.0 (1).zip")
	updateDryRun = true
	withJSONOutput(t)

	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, []string{"mod1"}) })
	assertJSONCLIGolden(t, "update_from_file_plan", out, dir, "/GOLDEN/downloads")

	row, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", row.Version, "a dry run changes nothing")
}

func TestDoUpdate_FromFile_Applies(t *testing.T) {
	svc, game, dir := setupUpdateFromFileTest(t)
	updateFromFile = writeFromFileArchive(t, dir, "mod1-2.0.zip")

	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, []string{"mod1"}) })
	assert.Contains(t, out, "Updating Mod One 1.0 → 2.0 from mod1-2.0.zip")
	assert.Contains(t, out, "Matches the advertised update: mod1-2.0.zip (file new-1)")
	assert.Contains(t, out, "✓ Updated: Mod One 1.0 → 2.0")
	assert.Contains(t, out, "Previous version preserved for rollback")

	row, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.Equal(t, []string{"new-1"}, row.FileIDs)
	assert.Equal(t, "1.0", row.PreviousVersion)
}

func TestDoUpdate_FromFile_MismatchRefusedUnlessAccepted(t *testing.T) {
	svc, game, dir := setupUpdateFromFileTest(t)
	updateFromFile = writeFromFileArchive(t, dir, "mod1-fabric-2.0.zip")

	var err error
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() error {
			err = doUpdate(context.Background(), svc, game, []string{"mod1"})
			return nil
		})
	})
	var mismatch *core.ArchiveMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Contains(t, err.Error(), "--accept-mismatch")
	assert.Contains(t, stderr, "Warning: mod1-fabric-2.0.zip is the source's file mod1-fabric-2.0.zip, not the update the source advertised (mod1-2.0.zip)")

	updateAcceptMismatch = true
	_ = captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, []string{"mod1"}) })
	row, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"new-2"}, row.FileIDs)
}

func TestDoUpdate_FromFile_FlagCombinations(t *testing.T) {
	svc, game, dir := setupUpdateFromFileTest(t)

	updateVersion = "2.0"
	require.ErrorContains(t, doUpdate(context.Background(), svc, game, []string{"mod1"}), "only apply with --from-file")
	updateVersion = ""

	updateFromFile = writeFromFileArchive(t, dir, "mod1-2.0.zip")
	require.ErrorContains(t, doUpdate(context.Background(), svc, game, nil), "give its mod ID")
	updateAll = true
	require.ErrorContains(t, doUpdate(context.Background(), svc, game, []string{"mod1"}), "cannot be combined with --all")
}

// #530's two typed refusals reach --json's envelope as data.
func TestReportError_JSON_ArchiveMismatchError(t *testing.T) {
	withJSONOutput(t)
	err := &core.ArchiveMismatchError{
		ArchiveName: "mod1-fabric-2.0.zip",
		Advertised:  core.ArchiveFileRef{ID: "new-1", FileName: "mod1-2.0.zip", Version: "2.0"},
		Matched:     &core.ArchiveFileRef{ID: "new-2", FileName: "mod1-fabric-2.0.zip", Version: "2.0"},
	}
	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"mod1-fabric-2.0.zip is the source's file mod1-fabric-2.0.zip, not the update the source advertised (mod1-2.0.zip) - it may be another flavor or loader's build; update from it anyway with --accept-mismatch\",\n"+
		"  \"details\": {\n"+
		"    \"archive_name\": \"mod1-fabric-2.0.zip\",\n"+
		"    \"advertised\": {\n"+
		"      \"id\": \"new-1\",\n"+
		"      \"file_name\": \"mod1-2.0.zip\",\n"+
		"      \"version\": \"2.0\"\n"+
		"    },\n"+
		"    \"matched\": {\n"+
		"      \"id\": \"new-2\",\n"+
		"      \"file_name\": \"mod1-fabric-2.0.zip\",\n"+
		"      \"version\": \"2.0\"\n"+
		"    }\n"+
		"  }\n"+
		"}\n", out)
}

func TestReportError_JSON_ArchiveVersionRequiredError(t *testing.T) {
	withJSONOutput(t)
	err := &core.ArchiveVersionRequiredError{ArchiveName: "Auctionator-339-1-g23f0261.zip", SourceID: "curseforge", ModID: "8939586"}
	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"cannot tell which version Auctionator-339-1-g23f0261.zip is: it is no file the source lists and its name carries no version - give it with --version\",\n"+
		"  \"details\": {\n"+
		"    \"archive_name\": \"Auctionator-339-1-g23f0261.zip\",\n"+
		"    \"source_id\": \"curseforge\",\n"+
		"    \"mod_id\": \"8939586\",\n"+
		"    \"version_required\": true\n"+
		"  }\n"+
		"}\n", out)
}

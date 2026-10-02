package core_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for #514: a mod whose file the source will not serve (a CurseForge
// author's third-party opt-out) is downloaded by hand and imported. Before
// the fix the import stored no checksum, and `verify --fix` could only fill
// one by re-downloading - which the source refuses - so the NO CHECKSUM
// warning could never clear.

// fileMD5 is the hex md5 of the file at path: what a download records for
// the archive it fetched.
func fileMD5(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// storedChecksum reads fileID's recorded checksum for a mod in profile
// "default", failing the test if the file has no row at all.
func storedChecksum(t *testing.T, svc *core.Service, gameID, sourceID, modID, fileID string) string {
	t.Helper()
	files, err := svc.GetFilesWithChecksums(context.Background(), gameID, "default")
	require.NoError(t, err)
	for _, f := range files {
		if f.SourceID == sourceID && f.ModID == modID && f.FileID == fileID {
			return f.Checksum
		}
	}
	t.Fatalf("no file row for %s/%s file %s in %+v", sourceID, modID, fileID, files)
	return ""
}

// An archive import resolved against the source records the archive's own
// md5 for the resolved file - the value a download of the same file records.
func TestImportArchive_WithID_RecordsTheArchiveChecksum(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	src.files = []domain.DownloadableFile{{ID: "55", FileName: "mymod.zip", Version: "2.0", IsPrimary: true}}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createImportTestZip(t, archivePath, map[string]string{"mymod.esp": "data"})

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "999", Force: true}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"55"}, result.FileIDs)

	assert.Equal(t, fileMD5(t, archivePath), storedChecksum(t, svc, "g1", "acme-source", "999", "55"),
		"the import has the archive in hand, so it records the archive's md5 - never NO CHECKSUM")

	verified, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	assert.Zero(t, verified.Warnings, "a freshly imported mod must verify clean: %+v", verified.Findings)
}

// A copy-mode adoption whose plan resolved a source file records the
// adopted file's md5: the cache entry holds exactly those bytes, as it would
// after a download.
func TestApplyAdopt_MatchedFile_RecordsTheFileChecksum(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "AcmeMod", GameID: "g1"}}
	src.files = []domain.DownloadableFile{{ID: "77", FileName: "AcmeMod-1.0.zip", Version: "1.0"}}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	path := writeLooseMod(t, game, "AcmeMod-1.0.zip", "payload")
	want := fileMD5(t, path)

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)
	result, err := svc.ApplyAdopt(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Adopted)

	assert.Equal(t, want, storedChecksum(t, svc, "g1", "acme-source", "42", "77"))
}

const manualReason = "mod author has disabled third-party downloads; visit CurseForge website to download manually"

// manualOnlySource serves a file list but refuses every download the way
// CurseForge does for an author's opt-out, counting the attempts.
type manualOnlySource struct {
	*mockSource
	urlCalls atomic.Int32
}

func (s *manualOnlySource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	s.urlCalls.Add(1)
	return "", &source.ManualDownloadError{Reason: manualReason}
}

// seedManualOnlyMod seeds a mod whose file "1" has no checksum. complete
// writes its cache entry the way an import does - content plus the file's
// completion marker with its member manifest; otherwise the entry holds the
// content alone, with no marker vouching for it.
func seedManualOnlyMod(t *testing.T, manualFlag, complete bool) (*core.Service, *domain.Game, *manualOnlySource) {
	t.Helper()
	svc, game := newFixTestGame(t)
	src := &manualOnlySource{mockSource: newMockSource("msrc")}
	svc.RegisterSource(src)

	mod := domain.Mod{ID: "8939586", SourceID: "msrc", Name: "Auctionator", Version: "1.0", GameID: game.ID,
		SourceURL: "https://example.test/mods/auctionator"}
	ctx := context.Background()
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "msrc", mod.ID, "1.0", "Auctionator/Auctionator.toc", []byte("## Title: Auctionator")))
	if complete {
		require.NoError(t, svc.MarkImportedFileCompleteForTest(ctx, game, &mod, "1"))
	}
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: mod, ProfileName: "default", Enabled: true, FileIDs: []string{"1"},
		UpdatePolicy: domain.UpdateNotify, ManualDownload: manualFlag,
	}))
	return svc, game, src
}

// The user's case: the row is not flagged manual, the source refuses the
// re-download, and the cache entry is complete - so --fix fills the
// checksum from the cached files instead of failing.
func TestVerifyFix_NoChecksum_SourceRefusesDownload_FillsFromCache(t *testing.T) {
	svc, game, src := seedManualOnlyMod(t, false, true)

	sink, rec := core.RecordEvents()
	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{Fix: true}, sink)
	require.NoError(t, err)
	events := verifyEvents(*rec)

	assert.Equal(t, int32(1), src.urlCalls.Load(), "the re-download is tried first, and refused")
	assert.Zero(t, result.Warnings)
	// Repair carries the sub-line's sentence on the row itself: an "ok" row
	// would otherwise look untouched in the result document (#517).
	assert.Equal(t, []core.VerifyFinding{{ModID: "8939586", ModName: "Auctionator", FileID: "1", Status: "ok",
		Repair: "Checksum filled from the cached files (the source won't serve this file)"}}, result.Findings)
	assert.NotEmpty(t, storedChecksum(t, svc, game.ID, "msrc", "8939586", "1"))

	var row *core.VerifyEvent
	for i := range events {
		if events[i].Kind == core.VerifyEvFinding {
			row = &events[i]
		}
	}
	require.NotNil(t, row)
	assert.True(t, row.ChecksumPopulated)
	details := repairDetails(events)
	require.Len(t, details, 1)
	assert.Equal(t, "Checksum filled from the cached files (the source won't serve this file)", details[0].Detail)
	assert.True(t, details[0].Fixed)

	again, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Zero(t, again.Warnings, "the fill is durable: the next verify is clean")
}

// A row already flagged manual-download is filled from its cache without
// asking the source at all.
func TestVerifyFix_NoChecksum_ManualDownloadMod_FillsWithoutDownloading(t *testing.T) {
	svc, game, src := seedManualOnlyMod(t, true, true)

	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)

	assert.Zero(t, src.urlCalls.Load(), "a manual-download mod is never re-downloaded")
	assert.Zero(t, result.Warnings)
	assert.Equal(t, "ok", findingFor(t, result, "8939586").Status)
	assert.NotEmpty(t, storedChecksum(t, svc, game.ID, "msrc", "8939586", "1"))
}

// Without a complete cache entry there is nothing to fill from, so the
// warning stands - with one "getting download URL:" and the mod's page.
func TestVerifyFix_NoChecksum_SourceRefusesDownload_IncompleteCacheKeepsWarningWithPage(t *testing.T) {
	svc, game, _ := seedManualOnlyMod(t, false, false)

	sink, rec := core.RecordEvents()
	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{Fix: true}, sink)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Warnings)
	f := findingFor(t, result, "8939586")
	assert.Equal(t, "no_checksum", f.Status)
	assert.Equal(t, "getting download URL: "+manualReason, f.Note)
	assert.Equal(t, "https://example.test/mods/auctionator", f.ModURL)
	assert.Equal(t, "msrc", f.SourceID, "the page's source rides with it, so a frontend can name the link (#517)")
	assert.Empty(t, storedChecksum(t, svc, game.ID, "msrc", "8939586", "1"))

	var lines []string
	for _, d := range repairDetails(verifyEvents(*rec)) {
		lines = append(lines, d.Detail)
	}
	assert.Equal(t, []string{
		"Re-download to populate checksum failed: getting download URL: " + manualReason,
		"Download it manually from: https://example.test/mods/auctionator",
	}, lines)
	for _, l := range lines {
		assert.LessOrEqual(t, strings.Count(l, "getting download URL"), 1, "no repeated prefix: %q", l)
	}
}

// The MISSING repair names the page too when its download fails.
func TestVerifyFix_Missing_DownloadFailure_ShowsThePage(t *testing.T) {
	svc, game, _ := seedManualOnlyMod(t, false, false)
	require.NoError(t, svc.GetGameCache(game).Delete(game.ID, "msrc", "8939586", "1.0"))
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "msrc", "8939586", game.ID, "default", "1", "sum"))

	sink, rec := core.RecordEvents()
	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{Fix: true}, sink)
	require.NoError(t, err)

	f := findingFor(t, result, "8939586")
	assert.Equal(t, "missing", f.Status)
	assert.Equal(t, "https://example.test/mods/auctionator", f.ModURL)
	assert.Equal(t, "msrc", f.SourceID)
	details := repairDetails(verifyEvents(*rec))
	require.Len(t, details, 2)
	assert.Equal(t, "Download it manually from: https://example.test/mods/auctionator", details[1].Detail)
}

// The paths below rewrite an existing row through the full-row save, which
// re-keys installed_mod_files and so drops every stored checksum: each one
// left a verified mod reading NO CHECKSUM - for a manual-only file, for good.
// Each now carries the row's checksums across (#514).

func TestApplyAdoptBackfill_KeepsTheRowsChecksums(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["77"] = &domain.Mod{ID: "77", SourceID: "acme-source", Name: "Needs Backfill", Author: "Jane Modder", Version: "1.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedSyncInstalledMod(t, svc, game, "acme-source", "77", "Needs Backfill", "1.0", "default", true, []string{"f1"})
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "acme-source", "77", "g1", "default", "f1", "sum"))

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)
	result, err := svc.ApplyAdoptBackfill(context.Background(), game, plan, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Backfilled)

	assert.Equal(t, "sum", storedChecksum(t, svc, "g1", "acme-source", "77", "f1"))
}

func TestApplyRelinkMod_MetadataEdit_KeepsTheRowsChecksums(t *testing.T) {
	svc, game := newFixTestGame(t)
	ctx := context.Background()
	seedVerifyMod(t, svc, game, "rsrc", "mod1", "Mod One", "1.0", []string{"1"}, true)
	require.NoError(t, svc.SaveFileChecksum(ctx, "rsrc", "mod1", game.ID, "default", "1", "sum"))

	plan, err := svc.PlanRelinkMod(ctx, game, "default", "rsrc", "mod1", "", "")
	require.NoError(t, err)
	_, err = svc.ApplyRelinkMod(ctx, game, plan, core.RelinkOptions{Name: "Renamed"}, nil)
	require.NoError(t, err)

	assert.Equal(t, "sum", storedChecksum(t, svc, game.ID, "rsrc", "mod1", "1"))
}

func TestApplyProfileSwitch_RowCopiedFromAnotherProfile_KeepsItsChecksums(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))

	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "1", "1.0", "plugin.esp", []byte("data")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "1", SourceID: "src", Name: "Test Mod", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, FileIDs: []string{"f1"},
	}))
	require.NoError(t, svc.SaveFileChecksum(ctx, "src", "1", "g1", "default", "f1", "sum"))
	seedProfileWithMod(t, svc, "g1", "target", "src", "1", "1.0")

	plan, err := svc.PlanProfileSwitch(ctx, game, "target")
	require.NoError(t, err)
	require.Len(t, plan.ToEnable, 1)
	require.Equal(t, "default", plan.ToEnable[0].ProfileName, "precondition: the row lives under the other profile")
	_, err = svc.ApplyProfileSwitch(ctx, game, plan, nil)
	require.NoError(t, err)

	files, err := svc.GetFilesWithChecksums(ctx, "g1", "target")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "sum", files[0].Checksum)
}

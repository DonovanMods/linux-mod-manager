package core_test

// #425: a warning only a DOWNLOAD can raise - #424's "BepInEx found in
// <path>; declare it with `lmm game edit <id> --loader bepinex`", #358's
// "layout lmm cannot place" - reached the user only by luck of which verb
// they typed. Every flow's download wrapper kept DownloadEvents and dropped
// everything else (install's included), and a directory source's ingest
// passed no sink at all. The warning now rides every flow's own stream,
// under the flow's scope, as a WarningEvent with Phase DownloadWarning.

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// downloadWarnings is the download-time warnings among events.
func downloadWarnings(events []core.Event) []core.WarningEvent {
	var out []core.WarningEvent
	for _, e := range events {
		if w, ok := e.(core.WarningEvent); ok && w.Phase == core.DownloadWarning {
			out = append(out, w)
		}
	}
	return out
}

// undeclaredBepInExFixture is the #424 notice's fixture: BepInEx installed,
// not declared, and a plugin-folder archive on the source.
func undeclaredBepInExFixture(t *testing.T) *bepinexDownloadFixture {
	t.Helper()
	fixture := newBepInExDownloadFixture(t, map[string]string{
		"Jotunn/Jotunn.dll": "assembly",
		"Jotunn/Jotunn.xml": "<doc/>",
	}, false)
	bepinexInstall(t, fixture.game.InstallPath, "5.4.23.5", domain.LoaderBootstrapProton, time.Time{})
	_, err := fixture.svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), fixture.game.ID)
	require.NoError(t, err)
	return fixture
}

func TestDownloadWarning_ReachesTheInstallFlow(t *testing.T) {
	fixture := undeclaredBepInExFixture(t)
	ctx := context.Background()

	plan, err := fixture.svc.PlanInstall(ctx, fixture.game, "default", "bepinex-repo", fixture.mod.ID, false)
	require.NoError(t, err)
	sink, events := core.RecordEvents()
	_, err = fixture.svc.ApplyInstall(ctx, fixture.game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err)

	got := downloadWarnings(*events)
	require.Len(t, got, 1, "the install flow's download wrapper dropped it")
	assert.Equal(t, bepinexUndeclaredNoticeText(fixture.game), got[0].Message)
	assert.Equal(t, core.OpInstall, got[0].Op, "under the flow's own scope")
	assert.Equal(t, fixture.mod.Name, got[0].ModName)
	assert.Equal(t, "download_warning", got[0].Phase.String())
}

// TestDownloadWarning_ReachesTheDeployFlow: a deploy that has to fetch a
// mod whose cache entry is gone is a download too, and says so under its
// own op.
func TestDownloadWarning_ReachesTheDeployFlow(t *testing.T) {
	fixture := undeclaredBepInExFixture(t)
	ctx := context.Background()

	plan, err := fixture.svc.PlanInstall(ctx, fixture.game, "default", "bepinex-repo", fixture.mod.ID, false)
	require.NoError(t, err)
	_, err = fixture.svc.ApplyInstall(ctx, fixture.game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	gameCache := fixture.svc.GetGameCache(fixture.game)
	require.NoError(t, gameCache.Delete(fixture.game.ID, fixture.mod.SourceID, fixture.mod.ID, fixture.mod.Version))

	sink, events := core.RecordEvents()
	_, err = fixture.svc.DeployProfile(ctx, fixture.game, "default", core.DeployOptions{}, sink)
	require.NoError(t, err)

	got := downloadWarnings(*events)
	require.Len(t, got, 1, "the deploy flow's download wrapper dropped it")
	assert.Equal(t, bepinexUndeclaredNoticeText(fixture.game), got[0].Message)
	assert.Equal(t, core.OpDeploy, got[0].Op)
}

// TestDownloadWarning_ReachesTheSinkFromADirectorySource: a local or
// directory source's archive is ingested, not downloaded, and that branch
// passed no sink - the notice reached only the log, which the CLI keeps
// off by default.
func TestDownloadWarning_ReachesTheSinkFromADirectorySource(t *testing.T) {
	svc := newFlowsTestService(t)
	root := t.TempDir()
	bepinexInstall(t, root, "5.4.23.5", domain.LoaderBootstrapProton, time.Time{})
	game := &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: root, ModPath: root,
		LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{"my-mods": ""},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	modsDir := t.TempDir()
	writeZip(t, filepath.Join(modsDir, "Jotunn.zip"), map[string]string{"Jotunn/Jotunn.dll": "assembly"})
	src, err := custom.New(custom.SourceDefinition{
		ID: "my-mods", Name: "My Mods", Type: custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: modsDir},
	})
	require.NoError(t, err)
	svc.RegisterSource(src)

	ctx := context.Background()
	res, err := src.Search(ctx, source.SearchQuery{Query: "jotunn", GameID: game.ID, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	files, err := src.GetModFiles(ctx, &res.Mods[0])
	require.NoError(t, err)
	require.Len(t, files, 1)

	sink, events := core.RecordEvents()
	_, err = svc.DownloadModForTest(ctx, "my-mods", game, &res.Mods[0], &files[0], sink)
	require.NoError(t, err)

	got := downloadWarnings(*events)
	require.Len(t, got, 1)
	assert.Equal(t, bepinexUndeclaredNoticeText(game), got[0].Message)
}

// TestDownloadWarning_ReachesAVerifyFixRedownload: verify --fix re-fetches
// a mod whose cache entry is gone, and its own vocabulary carries the
// notice as a sub-line under the repair.
func TestDownloadWarning_ReachesAVerifyFixRedownload(t *testing.T) {
	fixture := undeclaredBepInExFixture(t)
	ctx := context.Background()

	plan, err := fixture.svc.PlanInstall(ctx, fixture.game, "default", "bepinex-repo", fixture.mod.ID, false)
	require.NoError(t, err)
	_, err = fixture.svc.ApplyInstall(ctx, fixture.game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	gameCache := fixture.svc.GetGameCache(fixture.game)
	require.NoError(t, gameCache.Delete(fixture.game.ID, fixture.mod.SourceID, fixture.mod.ID, fixture.mod.Version))

	var details []string
	_, err = fixture.svc.VerifyReport(ctx, fixture.game, "default", core.VerifyOptions{Fix: true, Force: true}, func(e core.Event) {
		if ev, ok := e.(core.VerifyEvent); ok && ev.Kind == core.VerifyEvRepairDetail {
			details = append(details, ev.Detail)
		}
	})
	require.NoError(t, err)
	assert.Contains(t, details, "Warning: "+bepinexUndeclaredNoticeText(fixture.game))
}

// writeZip writes a zip of members at path.
func writeZip(t *testing.T, path string, members map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	for name, body := range members {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workshopAppID is the app id the fixture game maps steamworkshop to. The
// per-source game id for this source IS the decimal Steam app id.
const workshopAppID = "1133870"

// workshopDetailsFixture serves GetPublishedFileDetails for the fake
// steamcmd's item ids, with an empty file_url so every one of them takes
// the Fetcher path rather than a direct download. It is the ONLY Steam
// endpoint these tests know about, and it is an httptest server: nothing
// here may ever reach Valve.
func workshopDetailsFixture(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		id := "3000000001"
		for _, candidate := range []string{"3000000001", "3000000002", "3000000003"} {
			if strings.Contains(string(body), candidate) {
				id = candidate
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"response":{"result":1,"resultcount":1,"publishedfiledetails":[
			{"publishedfileid":%q,"result":1,"creator":"76561198000000000",
			 "title":"Workshop Item %s","description":"A Workshop item.",
			 "file_size":"240","file_url":"","hcontent_file":"7987119735124793734",
			 "time_created":1700000000,"time_updated":1764767935,
			 "lifetime_subscriptions":1,"tags":[]}]}}`, id, id)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setupWorkshopInstallTest builds a service holding the REAL Steam Workshop
// source pointed at a fixture API, with the fake steamcmd on PATH, and
// resets install's flag globals to install one Workshop item
// non-interactively - `lmm install steamworkshop:<fileid>` end to end.
func setupWorkshopInstallTest(t *testing.T, fileID string) (*core.Service, *domain.Game) {
	t.Helper()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "STEAM_ROOT"} {
		t.Setenv(key, t.TempDir())
	}
	testutil.FakeSteamcmdOnPath(t)

	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	svc.RegisterSource(steamworkshop.New(testutil.WorkshopOptions(t, workshopDetailsFixture(t).URL)))

	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"steamworkshop": workshopAppID},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	oldSource, oldProfile, oldVersion, oldModID, oldFileID := installSource, installProfile, installVersion, installModID, installFileID
	oldYes, oldShowArchived, oldSkipVerify, oldForce, oldNoDeps := installYes, installShowArchived, skipVerify, installForce, installNoDeps
	oldVerbose, oldNoColor, oldNoHooks := verbose, noColor, noHooks
	installSource, installProfile, installVersion, installFileID = "steamworkshop", "", "", ""
	installModID = fileID
	installYes, installShowArchived, skipVerify, installForce, installNoDeps = true, false, false, false, false
	verbose, noColor, noHooks = false, true, false
	t.Cleanup(func() {
		installSource, installProfile, installVersion, installModID, installFileID = oldSource, oldProfile, oldVersion, oldModID, oldFileID
		installYes, installShowArchived, skipVerify, installForce, installNoDeps = oldYes, oldShowArchived, oldSkipVerify, oldForce, oldNoDeps
		verbose, noColor, noHooks = oldVerbose, oldNoColor, oldNoHooks
	})
	return svc, game
}

// TestDoInstall_Workshop_AnonymousDownloadBecomesAnOrdinaryMod is Tier 3's
// whole point: once the bytes are staged, an lmm-downloaded Workshop item
// is an ORDINARY mod. It goes through cache -> linker -> mod_path with no
// Workshop-specific code, and its row is External: false - the opposite of
// the Tier-1 item lmm merely tracks.
func TestDoInstall_Workshop_AnonymousDownloadBecomesAnOrdinaryMod(t *testing.T) {
	svc, game := setupWorkshopInstallTest(t, "3000000001")

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)

	installed, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3000000001", "g1", "default")
	require.NoError(t, err)
	assert.False(t, installed.External, "an item lmm downloaded is lmm-managed, not external")
	assert.True(t, installed.Deployed)
	assert.Empty(t, installed.ExternalPath)

	_, statErr := os.Lstat(filepath.Join(game.ModPath, "mod.txt"))
	assert.NoError(t, statErr, "the item's files are deployed into the game directory")
}

// TestDoInstall_Workshop_AnonymousRefusalTellsTheUserToUseTier1 pins the
// ruling's fallback: when the publisher does not allow anonymous
// downloads, lmm says so and names the one route that does work.
func TestDoInstall_Workshop_AnonymousRefusalTellsTheUserToUseTier1(t *testing.T) {
	svc, game := setupWorkshopInstallTest(t, "3000000002")

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrWorkshopAnonymousRefused))
	assert.Contains(t, err.Error(), "lmm import --workshop")

	var typed *core.WorkshopFetchError
	require.True(t, errors.As(err, &typed))
	assert.Equal(t, workshopAppID, typed.AppID)
	assert.Equal(t, "steamcmd", typed.Tool)

	_, getErr := svc.GetInstalledMod(context.Background(), "steamworkshop", "3000000002", "g1", "default")
	assert.Error(t, getErr, "a refused download must leave no installed row behind")
}

// TestDoInstall_Workshop_AccessDeniedReportsAnUnavailableItem is the third
// outcome: Steam will not serve the item at all.
func TestDoInstall_Workshop_AccessDeniedReportsAnUnavailableItem(t *testing.T) {
	svc, game := setupWorkshopInstallTest(t, "3000000003")

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrWorkshopItemUnavailable))
}

// TestDoInstall_Workshop_MissingSteamcmdIsAnActionableRefusal pins the
// runtime probe at the CLI: steamcmd is never vendored and never
// auto-installed, so a machine without it gets the install hint rather
// than a subprocess error.
func TestDoInstall_Workshop_MissingSteamcmdIsAnActionableRefusal(t *testing.T) {
	svc, game := setupWorkshopInstallTest(t, "3000000001")
	t.Setenv("PATH", t.TempDir())

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrExternalToolMissing))
	assert.Contains(t, err.Error(), "SteamCMD")
}

// TestDoInstall_Workshop_TheFetchesProgressReachesTheTerminal is the
// design's "a silent multi-gigabyte download is the worst possible UX"
// rule, in the place a twenty-minute silence is felt hardest. The CLI's
// install closure switches on explicit phases, so the fetch's three had to
// be named there or they were simply dropped - lmm printed nothing at all
// for the entire duration of a steamcmd run.
func TestDoInstall_Workshop_TheFetchesProgressReachesTheTerminal(t *testing.T) {
	svc, game := setupWorkshopInstallTest(t, "3000000001")

	out, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)

	assert.Contains(t, out, "fetching Workshop item 3000000001 with steamcmd",
		"the fetch announces itself")
	assert.Contains(t, out, "78.90", "the tool's own progress lines reach the terminal")
	assert.Contains(t, out, "Workshop item 3000000001 downloaded", "and it says when it finished")
}

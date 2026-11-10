package serve_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two mods of #587's scenarios, both with an update: one on the auto
// policy, one on notify - the control the pass must leave alone.
const (
	e2eAutoModID   = "amulet"
	e2eNotifyModID = "necklace"
)

// newE2EFixtureWithAnAutoUpdate seeds both mods at 1.0, deployed, against
// a source offering 2.0. gate, when set, holds the automatic job's download
// open; probe, when set, is the Service's GameProbe; tweak, when set,
// changes the source before anything uses it.
func newE2EFixtureWithAnAutoUpdate(t *testing.T, gate <-chan struct{}, probe *core.GameProbe, tweak func(*e2eSearchSource)) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)

	src := newE2ESearchSource(t, "fake")
	src.updatable = true
	src.urlGate = gate
	if tweak != nil {
		tweak(src)
	}
	seeds := []struct {
		id, name string
		policy   domain.UpdatePolicy
	}{
		{e2eAutoModID, "Auto Amulet", domain.UpdateAuto},
		{e2eNotifyModID, "Notify Necklace", domain.UpdateNotify},
	}
	for _, seed := range seeds {
		src.addMod(e2eSearchSourceMod{
			mod: domain.Mod{ID: seed.id, SourceID: "fake", Name: seed.name, Version: "2.0"},
			files: []domain.DownloadableFile{
				{ID: seed.id + "-2.0", Name: "Main 2.0", FileName: seed.id + "-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 128},
				{ID: seed.id + "-1.0", Name: "Main 1.0", FileName: seed.id + "-1.0.zip", Version: "1.0", Category: "MAIN", Size: 96},
			},
			members: map[string]string{seed.id + "-1.0": "Mods/" + seed.id + ".pak", seed.id + "-2.0": "Mods/" + seed.id + ".pak"},
		})
	}

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)
	if probe != nil {
		svc.SetGameProbe(probe)
	}

	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{src.ID(): ""},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	pm := svc.NewProfileManager()
	_, err = pm.Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))

	for _, seed := range seeds {
		fileID := seed.id + "-1.0"
		require.NoError(t, svc.GetGameCache(game).Store(game.ID, "fake", seed.id, "1.0",
			"Mods/"+seed.id+".pak", []byte("payload for "+seed.id+"/"+fileID)))
		require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
			Mod:          domain.Mod{ID: seed.id, SourceID: "fake", Name: seed.name, Version: "1.0", GameID: game.ID},
			ProfileName:  "default",
			UpdatePolicy: seed.policy,
			Enabled:      true,
			FileIDs:      []string{fileID},
		}))
		require.NoError(t, pm.AddMod(t.Context(), game.ID, "default", domain.ModReference{
			SourceID: "fake", ModID: seed.id, Version: "1.0", FileIDs: []string{fileID},
		}))
	}
	_, err = svc.DeployProfile(t.Context(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default",
		BrowserErrors: browserErrors,
	}
}

// e2eInstalledVersion is modID's installed version in the fixture profile.
func e2eInstalledVersion(t *testing.T, f e2eFixture, modID string) string {
	t.Helper()
	im, err := f.Svc.GetInstalledMod(t.Context(), "fake", modID, f.Game.ID, "default")
	require.NoError(t, err)
	return im.Version
}

// TestE2E_AutoUpdateShowsItsJobThenLeavesTheCard is #587 as the owner saw
// it: opening Mission Control with an auto-policy update no longer leaves
// it listed for a hand install. The card says the server is applying it -
// the download is held open so the page can be looked at mid-job - and,
// once the job is done, no longer lists it; the notify row stays.
func TestE2E_AutoUpdateShowsItsJobThenLeavesTheCard(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	f := newE2EFixtureWithAnAutoUpdate(t, gate, nil, nil)

	var applying, live, tray, card string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-testid="updates-auto"][data-state="applying"]`, chromedp.ByQuery),
		chromedp.Text(`.card--updates [data-testid="updates-auto"]`, &applying, chromedp.ByQuery),
		pollUntil(`(document.querySelector('.library__live')?.textContent ?? '').includes('Updating automatically')`),
		chromedp.Text(`.library__live`, &live, chromedp.ByQuery),
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitVisible(`.tray__kind[data-origin="auto"]`, chromedp.ByQuery),
		chromedp.Text(`.tray__kind[data-origin="auto"]`, &tray, chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
		pollUntil(`!(document.querySelector('.card--updates')?.textContent ?? '').includes('Auto Amulet')`),
		waitGone(`[data-testid="updates-auto"]`),
		chromedp.Text(`.card--updates`, &card, chromedp.ByQuery),
	)

	assert.Contains(t, live, "Updating automatically", "the running job names itself as automatic")
	assert.Equal(t, "Updating automatically", tray, "the tray row says no one clicked for it, in words (mutationLabel)")
	assert.Contains(t, applying, "Applying 1 automatic update")
	assert.Contains(t, card, "Notify Necklace", "a notify update stays on the card for the user")
	assert.Equal(t, "2.0", e2eInstalledVersion(t, f, e2eAutoModID))
	assert.Equal(t, "1.0", e2eInstalledVersion(t, f, e2eNotifyModID))
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_AutoUpdateWaitsForTheGameToClose is #587 decision 2 on screen:
// with the game running (a launch unit the probe reports as active), the
// card says what it is waiting for, and nothing is applied.
func TestE2E_AutoUpdateWaitsForTheGameToClose(t *testing.T) {
	// The game's own process, pid 500, in its active launch unit. Its path
	// is read off the game once the fixture has made it, so the probe asks
	// for it lazily.
	var installPath string
	f := newE2EFixtureWithAnAutoUpdate(t, nil, &core.GameProbe{
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("lmm-launch-g1-0a1b2c3d.service loaded active running lmm: Fixture Game\n"), nil
		},
		UnitPIDs: func(context.Context, string) ([]int, error) { return []int{500}, nil },
		Processes: func() ([]core.ProcessInfo, error) {
			exe := installPath + "/game.bin"
			return []core.ProcessInfo{{PID: 500, Exe: exe, Cmdline: []string{exe}}}, nil
		},
	}, nil)

	installPath = f.Game.InstallPath
	var waiting, card string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-testid="updates-auto"][data-state="waiting_for_game"]`, chromedp.ByQuery),
		chromedp.Text(`.card--updates [data-testid="updates-auto"]`, &waiting, chromedp.ByQuery),
		chromedp.Text(`.card--updates`, &card, chromedp.ByQuery),
	)

	assert.Contains(t, waiting, "1 automatic update waiting for Fixture Game to close")
	assert.Contains(t, card, "Auto Amulet", "a postponed update stays listed")
	assert.Equal(t, "1.0", e2eInstalledVersion(t, f, e2eAutoModID))
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_AutoUpdateFailureIsShownNotHidden: an automatic update that
// fails inside a batch which itself succeeds is held back, so it would
// stay on the card with no word of why and the bell would stay silent. The
// card says it failed and why, links to the job, and the bell counts it.
func TestE2E_AutoUpdateFailureIsShownNotHidden(t *testing.T) {
	f := newE2EFixtureWithAnAutoUpdate(t, nil, nil, func(src *e2eSearchSource) {
		src.urlErrs = map[string]error{e2eAutoModID: errors.New("the source is down")}
	})

	var note, badge string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-testid="updates-auto-failed"]`, chromedp.ByQuery),
		chromedp.Text(`.card--updates [data-testid="updates-auto-failed"]`, &note, chromedp.ByQuery),
		chromedp.WaitVisible(`.activity-bell__count--failed`, chromedp.ByQuery),
		chromedp.Text(`.activity-bell__count--failed`, &badge, chromedp.ByQuery),
		clickWhenSettled(`.card--updates [data-testid="updates-auto-failed"] button`),
		chromedp.WaitVisible(`.tray__kind[data-origin="auto"]`, chromedp.ByQuery),
	)

	assert.Contains(t, note, "Automatic update failed for Auto Amulet 2.0")
	assert.Contains(t, note, "the source is down")
	assert.Contains(t, note, "Check for updates to retry.")
	assert.Equal(t, "1", badge, "the bell counts an automatic job with a failed item")
	assert.Equal(t, "1.0", e2eInstalledVersion(t, f, e2eAutoModID))
	assert.Empty(t, f.BrowserErrors())
}

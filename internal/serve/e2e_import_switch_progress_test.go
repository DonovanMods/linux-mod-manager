package serve_test

// #669: an import's switch to its profile (#666) is a fresh progress step
// with an indeterminate bar, so the bar never goes back (#664). That has to
// hold for a progress view that was NOT on screen when the switch began -
// the tray closed and the progress toast dismissed - and is opened during
// the switch: what the view shows is decided from the frame in front of
// it, never from a frame an earlier render happened to see.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_ImportSwitchProgress_AViewOpenedDuringTheSwitchNeverGoesBack(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	// Flaky fails during the import and is fetched again by the switch,
	// whose download is held half-sent; Better Boots' download holds the
	// import itself until the progress views are put away.
	f.Src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "flaky", SourceID: "fake", Name: "Flaky Mod", Version: "1.0"},
		files:   []domain.DownloadableFile{{ID: "fl1", Name: "Main", FileName: "flaky.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 4096}},
		members: map[string]string{"fl1": "Mods/flaky.pak"},
	})
	if f.Src.urlErrs == nil {
		f.Src.urlErrs = map[string]error{}
	}
	f.Src.urlErrs["flaky"] = errors.New("the download server is down")
	importGate, switchGate := make(chan struct{}), make(chan struct{})
	f.Src.bodyGates = map[string]chan struct{}{e2eSearchInstallModID: importGate, "flaky": switchGate}
	t.Cleanup(func() {
		closeOnce(importGate)
		closeOnce(switchGate)
	})

	doc := "name: SwitchProg\ngame_id: " + f.Game.ID + "\nmods:\n" +
		"  - {source_id: fake, mod_id: flaky, version: \"1.0\"}\n" +
		"  - {source_id: fake, mod_id: " + e2eSearchInstallModID + ", version: \"2.0\"}\n"
	path := filepath.Join(t.TempDir(), "switchprog.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))

	// 1. The import runs and stops in Better Boots' download; its progress
	// toast is dismissed and the tray stays closed, so no progress view is
	// on screen.
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openImportCollectionDialog(),
		chromedp.SetUploadFiles(`.modal[data-kind="import-collection"] input[type="file"]`, []string{path}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] [data-testid="import-switch"]`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('.modal[data-kind="profile_import"] label.form-check'))
				.find((l) => l.textContent.includes("Download and install"))?.querySelector("input").click();
		`, nil),
		settleEffects(),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		chromedp.WaitVisible(`.toast--running`, chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error {
			// Flaky Mod's failed fetch, then Better Boots' held one.
			deadline := time.Now().Add(15 * time.Second)
			for f.Src.urlRequests.Load() < 2 {
				if time.Now().After(deadline) {
					return errors.New("the import never reached Better Boots' download")
				}
				time.Sleep(20 * time.Millisecond)
			}
			return nil
		}),
		clickWhenSettled(`.toast--running .toast__dismiss`),
		pollUntil(`document.querySelector(".toast--running") === null && document.querySelector(".tray") === null`),
	)

	// 2. The import finishes unseen and switches; the switch's download of
	// Flaky Mod is held half-sent.
	f.Src.recovered.Store(true)
	close(importGate)
	require.Eventually(t, func() bool { return f.Src.urlRequests.Load() >= 3 },
		15*time.Second, 20*time.Millisecond, "the switch fetches Flaky Mod again")

	// 3. The tray, opened now, shows the switch's step - never a bar sent
	// back from where the import left it.
	var indeterminate bool
	var width, label string
	f.runInBrowser(t,
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitVisible(`.tray__row`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.tray__row[data-state="running"]')?.textContent.includes("Downloading") === true`),
		chromedp.Evaluate(`document.querySelector('.tray__row[data-state="running"] .job-progress__bar--indeterminate') !== null`, &indeterminate),
		chromedp.Evaluate(`document.querySelector('.tray__row[data-state="running"] .job-progress__bar [style]')?.style.width ?? ""`, &width),
		textContent(`.tray__row[data-state="running"]`, &label),
	)
	assert.True(t, indeterminate, "the switch's download is the switch step's indeterminate bar (width %q; %s)", width, label)
	assert.Empty(t, width, "not a determinate bar sent back from where the import left it")

	close(switchGate)
	f.runInBrowser(t, awaitJobsSettled())
	live, err := f.Svc.NewProfileManager().GetDefault(t.Context(), f.Game.ID)
	require.NoError(t, err)
	assert.Equal(t, "SwitchProg", live.Name, "the import switched")
	assert.Empty(t, f.BrowserErrors())
}

// closeOnce closes ch unless it is closed already.
func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

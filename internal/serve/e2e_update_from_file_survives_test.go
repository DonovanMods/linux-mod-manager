package serve_test

// #531: "Update from file…" opened from a failed update closed itself a few
// seconds later. Driven in a real browser with the browser's OWN file chooser
// intercepted (Page.setInterceptFileChooserDialog), because the claim is about
// what the user's open dialogs survive: the chooser (carried by the page's one
// file input, which must still be the live element when the file lands) and
// the confirm modal that the upload opens.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// dialogSurvivalWait is the one deliberate long wait: well past every timer
// the page owns (the 4s success release, the 3s unread re-read, the 8s toast,
// the 15s SSE heartbeat is deliberately not waited out).
const dialogSurvivalWait = 12 * time.Second

// fileChooser is an intercepted native file chooser.
type fileChooser struct {
	mu     sync.Mutex
	opened []cdp.BackendNodeID
}

func (c *fileChooser) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.opened)
}

func (c *fileChooser) last() cdp.BackendNodeID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opened[len(c.opened)-1]
}

// interceptFileChooser hands every native chooser to the test instead of
// opening one, recording the input it belongs to. The listener is attached to
// the browser's own long-lived context, not to the action's: an action's
// context ends with its run, and the chooser opens in a later one.
func interceptFileChooser(browserCtx context.Context, c *fileChooser) chromedp.Action {
	chromedp.ListenTarget(browserCtx, func(ev any) {
		if opened, ok := ev.(*page.EventFileChooserOpened); ok {
			c.mu.Lock()
			c.opened = append(c.opened, opened.BackendNodeID)
			c.mu.Unlock()
		}
	})
	return page.SetInterceptFileChooserDialog(true)
}

// chooseFile answers the intercepted chooser with archive, as the OS dialog
// would: onto the input the chooser belongs to, which fires its change event.
func chooseFile(c *fileChooser, archive string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		return dom.SetFileInputFiles([]string{archive}).WithBackendNodeID(c.last()).Do(ctx)
	})
}

// tagFileInput remembers the page's file input on window, so a later check can
// tell whether it is still the element in the document.
const tagFileInput = `window.__chosenInput = document.querySelector(` + "`" + e2eFromFileInput + "`" + `); true`

// fileInputUnchanged reports that the input a chooser was opened on is still
// the one in the document - replaced or unmounted, it would swallow the file.
const fileInputUnchanged = `(() => {
	const now = document.querySelector(` + "`" + e2eFromFileInput + "`" + `);
	return !!window.__chosenInput && window.__chosenInput.isConnected && now === window.__chosenInput;
})()`

// holdsFor polls js on a timer for d and fails if it is ever falsy - the "stays
// open" assertion. It polls rather than sleeps once, so the moment a dialog
// goes is the moment it is reported.
func holdsFor(t *testing.T, ctx context.Context, d time.Duration, what, js string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var ok bool
		require.NoError(t, chromedp.Run(ctx, chromedp.Evaluate(js, &ok)))
		require.True(t, ok, "%s: gone before the user acted", what)
		time.Sleep(e2ePollInterval * 4)
	}
}

// updateEntryPoint is one place "Update from file…" is offered on a failed
// update: how to reach it, and its button.
type updateEntryPoint struct {
	name   string
	reach  func(f e2eSearchFixture) chromedp.Action
	button string
}

func updateEntryPoints() []updateEntryPoint {
	return []updateEntryPoint{
		{
			name: "slide-over job readout",
			reach: func(f e2eSearchFixture) chromedp.Action {
				return chromedp.Tasks{
					chromedp.Navigate(f.SlideOverPath("fake", "manualup")),
					chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
					chromedp.WaitVisible(`.slide-over__actions`, chromedp.ByQuery),
					failManualUpdate(),
				}
			},
			button: `.slide-over .job-progress [data-testid="download-page"] [data-action="update-from-file"]`,
		},
		{
			name: "library toolbar Update all readout",
			reach: func(f e2eSearchFixture) chromedp.Action {
				return chromedp.Tasks{
					chromedp.Navigate(f.HomePath()),
					chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
					pollUntil(`document.querySelector('.library__toolbar [data-action="update-all"]')?.textContent.includes("1")`),
					clickWhenSettled(`.library__toolbar [data-action="update-all"]`),
					chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
					clickWhenSettled(`.modal [data-action="confirm"]`),
					waitGone(`.modal`),
					chromedp.WaitVisible(`.library__toolbar .job-progress [data-testid="download-page"] [data-action="update-from-file"]`, chromedp.ByQuery),
				}
			},
			button: `.library__toolbar .job-progress [data-testid="download-page"] [data-action="update-from-file"]`,
		},
		{
			name: "updates card Update all readout",
			reach: func(f e2eSearchFixture) chromedp.Action {
				return chromedp.Tasks{
					chromedp.Navigate(f.HomePath()),
					chromedp.WaitVisible(`.card--updates [data-action="update-all"]`, chromedp.ByQuery),
					clickWhenSettled(`.card--updates [data-action="update-all"]`),
					chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
					clickWhenSettled(`.modal [data-action="confirm"]`),
					waitGone(`.modal`),
					chromedp.WaitVisible(`.card--updates .job-progress [data-testid="download-page"] [data-action="update-from-file"]`, chromedp.ByQuery),
				}
			},
			button: `.card--updates .job-progress [data-testid="download-page"] [data-action="update-from-file"]`,
		},
		{
			name: "activity tray row",
			reach: func(f e2eSearchFixture) chromedp.Action {
				return chromedp.Tasks{
					chromedp.Navigate(f.SlideOverPath("fake", "manualup")),
					chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
					chromedp.WaitVisible(`.slide-over__actions`, chromedp.ByQuery),
					failManualUpdate(),
					// The slide-over's scrim covers the bell: leave it, as a user
					// goes to the tray, and open the tray on the home route.
					chromedp.Navigate(f.HomePath()),
					chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
					clickWhenSettled(`.activity-bell__trigger`),
					chromedp.WaitVisible(`.tray__row`, chromedp.ByQuery),
					clickWhenSettled(`.tray__row .tray__summary[aria-expanded="false"]`),
					chromedp.WaitVisible(`.tray [data-testid="download-page"] [data-action="update-from-file"]`, chromedp.ByQuery),
				}
			},
			button: `.tray [data-testid="download-page"] [data-action="update-from-file"]`,
		},
	}
}

// survivesEveryTimer opens "Update from file…" from button on f's page (reached
// by reach), then holds the browser's chooser and, after the file lands, the
// confirm modal for dialogSurvivalWait each, and completes the update.
func survivesEveryTimer(t *testing.T, f e2eSearchFixture, reach chromedp.Action, button string) {
	t.Helper()
	archive := fromFileArchive(t, "ManualUp-2.0.zip", "v2 bytes")
	chooser := &fileChooser{}

	f.runInBrowser(t,
		interceptFileChooser(f.Ctx, chooser),
		reach,
		chromedp.Evaluate(tagFileInput, nil),
	)
	// A click is a coordinate: a readout that is still re-laying-out under a
	// loaded runner can take it, so it is repeated until the chooser opens.
	// It never opens a second one - the loop stops at the first.
	for attempt := 0; chooser.count() == 0; attempt++ {
		require.Less(t, attempt, 20, "the click opens the browser's file chooser")
		f.runInBrowser(t, clickWhenSettled(button))
		for deadline := time.Now().Add(2 * time.Second); chooser.count() == 0 && time.Now().Before(deadline); {
			time.Sleep(e2ePollInterval)
		}
	}

	// The chooser is carried by the page's one file input, and the way out
	// the user clicked it from is the thing they are still working from:
	// neither may be taken down while the dialog is open.
	holdsFor(t, f.Ctx, dialogSurvivalWait, "the file input the chooser belongs to", fileInputUnchanged)
	holdsFor(t, f.Ctx, e2ePollInterval, "the download way out the chooser was opened from",
		fmt.Sprintf(`document.querySelector(%q) !== null`, button))

	const confirmModal = `.modal[data-kind="update_from_archive"] .plan[data-match="advertised"]`
	f.runInBrowser(t,
		chooseFile(chooser, archive),
		chromedp.WaitVisible(confirmModal, chromedp.ByQuery),
	)
	holdsFor(t, f.Ctx, dialogSurvivalWait, "the confirm modal",
		fmt.Sprintf(`document.querySelector(%q) !== null`, confirmModal))

	f.runInBrowser(t,
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
	)
	require.Eventually(t, func() bool { return manualUpRow(t, f).Version == "2.0" }, e2eTimeout, 50*time.Millisecond)
	assert.Equal(t, 1, chooser.count())
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

func TestE2E_UpdateFromFile_DialogsSurviveEveryTimer(t *testing.T) {
	for _, ep := range updateEntryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			f := newE2EFixtureWithManualUpdate(t)
			survivesEveryTimer(t, f, ep.reach(f), ep.button)
		})
	}
}

// newE2EFixtureWithAPartialUpdate is the manual-update fixture plus "Okup",
// which updates without help: Update all applies it and fails Manual Up, so
// the batch job succeeds with one applied and one failed.
func newE2EFixtureWithAPartialUpdate(t *testing.T) e2eSearchFixture {
	t.Helper()
	f := newE2EFixtureWithManualUpdate(t)
	const file = "Mods/okup.pak"
	f.Src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: "okup", SourceID: "fake", Name: "Okup", Version: "2.0"},
		files: []domain.DownloadableFile{
			{ID: "o1", Name: "Main 1.0", FileName: "Okup-1.0.zip", Version: "1.0", Category: "MAIN", Size: 16},
			{ID: "o2", Name: "Main 2.0", FileName: "Okup-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 16},
		},
		members: map[string]string{"o1": file, "o2": file},
	})
	mod := domain.Mod{ID: "okup", SourceID: "fake", Name: "Okup", Version: "1.0", GameID: f.Game.ID}
	seedInstalledMod(t, f.Svc, f.Game, mod, true, map[string][]byte{file: []byte("v1 bytes")})
	row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "okup", f.Game.ID, "default")
	require.NoError(t, err)
	row.FileIDs = []string{"o1"}
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), row))
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "okup", Version: "1.0", FileIDs: []string{"o1"}}))
	_, err = f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return f
}

// TestE2E_UpdateFromFile_PartialUpdateAllKeepsItsFailureList is the owner's
// path to #531: Mission Control's "Update all (2)" applies what it can, and
// the view that lists what it could not - each with "Open on <source>" and
// "Update from file…" - must stay until the user acts, not vanish a few
// seconds after the batch ends. The batch job is "succeeded" (one applied),
// which is what the release timer used to key on.
func TestE2E_UpdateFromFile_PartialUpdateAllKeepsItsFailureList(t *testing.T) {
	f := newE2EFixtureWithAPartialUpdate(t)
	const button = `.card--updates .job-progress [data-testid="download-page"] [data-action="update-from-file"]`
	reach := chromedp.Tasks{
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-action="update-all"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.card--updates [data-action="update-all"]').textContent.includes("(2)")`),
		clickWhenSettled(`.card--updates [data-action="update-all"]`),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(`.card--updates .job-progress[data-state="succeeded"].job-progress--mixed `+
			`[data-testid="download-page"] [data-action="update-from-file"]`, chromedp.ByQuery),
	}
	survivesEveryTimer(t, f, reach, button)

	row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "okup", f.Game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version, "the batch applied the update that needed no help")
}

// TestE2E_UpdateFromFile_PartialUpdateAllToastOutlastsItsTimer: Update all is
// started and the page moves on before it ends, so the outcome arrives as a
// toast. A batch with a failed item is a failure toast - it is the only trace
// of the failed update left on screen - and stays past the 8s a success
// toast would have been dismissed after.
func TestE2E_UpdateFromFile_PartialUpdateAllToastOutlastsItsTimer(t *testing.T) {
	f := newE2EFixtureWithAPartialUpdate(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-action="update-all"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.card--updates [data-action="update-all"]').textContent.includes("(2)")`),
		clickWhenSettled(`.card--updates [data-action="update-all"]`),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		// Confirm and leave in one turn: the job's answer cannot have arrived,
		// so the card that started it is gone before the batch ends.
		chromedp.Evaluate(fmt.Sprintf(`(async () => {
			document.querySelector('.modal [data-action="confirm"]').click();
			const { navigate } = await import("/static/app/router.js");
			navigate(%q);
			return true;
		})()`, "/g/"+f.Game.ID+"/"+f.Profile+"/mod/fake/okup"), nil, awaitPromise),
		chromedp.WaitVisible(`.toast.toast--failure`, chromedp.ByQuery),
	)
	var detail string
	f.runInBrowser(t, textContent(`.toast.toast--failure .toast__detail`, &detail))
	assert.Equal(t, "1 applied / 1 failed", detail)

	holdsFor(t, f.Ctx, 10*time.Second, "the failure toast", `document.querySelector('.toast.toast--failure') !== null`)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

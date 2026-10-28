package serve_test

// #537: the activity bell's "I have seen these failures" acknowledgement is
// app-level state, not a property of whichever bell happens to be mounted.
// Mission Control's top bar and the away bar (full mod page, search, setup)
// each mount their own bell, so an acknowledgement kept in the component
// resurrected every old failure as unread on the next navigation or reload.
//
// Every wait below is on something the page itself reports - the bell's own
// count, a job readout scoped by its data-kind (#534) - and never on a sleep.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newE2EFixtureWithDrillInModsAndFailingDeploy is the drill-in fixture (a
// full mod page that reads cleanly) whose every deploy fails: a before_all
// hook that exits non-zero is a hard stop, so each Apply is one failed job.
func newE2EFixtureWithDrillInModsAndFailingDeploy(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixtureWithDrillInMods(t)

	script := filepath.Join(t.TempDir(), "failing-before-all")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\necho 'the mod directory is not writable' >&2\nexit 1\n"), 0o755))
	f.Game.Hooks.Install.BeforeAll = script
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	return f
}

// bellCountIs waits until the bell's badge reads want ("" for no badge).
func bellCountIs(want string) chromedp.Action {
	return pollUntil(fmt.Sprintf(
		`(document.querySelector(".activity-bell__count")?.textContent ?? "").trim() === %q`, want))
}

// failADeploy drives one deploy through the confirm modal and waits for the
// bell to say n failures are unread (n == "" waits for the failure only) - the bell is fed by the multiplexed
// stream, so that is the moment the job's failure has reached every bell.
func failADeploy(n string) chromedp.Tasks {
	return chromedp.Tasks{
		// A failed deploy's readout stays on the control until dismissed, and
		// the control is not a Deploy button while it does.
		dismissAFailedDeploy(),
		clickWhenSettled(`[data-action="deploy"]`),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.job-progress[data-kind="deploy"][data-state="failed"]') !== null`),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if n == "" {
				return nil // the caller reads the count itself
			}
			return bellCountIs(n).Do(ctx)
		}),
	}
}

// dismissAFailedDeploy clears a kept failed-deploy readout, when there is one.
func dismissAFailedDeploy() chromedp.Action {
	return chromedp.Tasks{
		chromedp.Evaluate(`document.querySelector('.job-progress[data-kind="deploy"] .job-progress__dismiss')?.click()`, nil),
		pollUntil(`document.querySelector('[data-action="deploy"]') !== null`),
	}
}

// openAndCloseTheBell acknowledges: opening the tray is the act.
func openAndCloseTheBell() chromedp.Tasks {
	return chromedp.Tasks{
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitVisible(`.tray__row[data-state="failed"]`, chromedp.ByQuery),
		clickWhenSettled(`.activity-bell__trigger`),
		pollUntil(`document.querySelector(".tray") === null`),
	}
}

// navigatedTo moves the SPA client-side (a remount, not a reload) and waits
// for marker - the destination's own element - so the bell read that
// follows is of the NEW page's bell.
func navigatedTo(path, marker string) chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.Evaluate(clientNavigateJS(path), nil),
		pollUntil(fmt.Sprintf(`document.querySelector(%q) !== null`, marker)),
	}
}

func TestE2E_ActivityBell_AcknowledgementSurvivesNavigation(t *testing.T) {
	f := newE2EFixtureWithDrillInModsAndFailingDeploy(t)

	var onHome, afterModPage, afterSearch, afterSetup, backHome string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		failADeploy("1"),
		textContent(`.activity-bell__count`, &onHome),
		openAndCloseTheBell(),
		bellCountIs(""),

		navigatedTo(f.ContextPath()+"/mod/fake/a", `.app-bar--away .activity-bell`),
		chromedp.WaitVisible(`.mod-page__title`, chromedp.ByQuery),
		chromedp.Evaluate(`(document.querySelector(".activity-bell__count")?.textContent ?? "").trim()`, &afterModPage),

		navigatedTo(f.ContextPath()+"/search?q=alpha", `.search-page`),
		chromedp.Evaluate(`(document.querySelector(".activity-bell__count")?.textContent ?? "").trim()`, &afterSearch),

		navigatedTo(f.ContextPath()+"/setup", `[data-testid="setup-page"]`),
		chromedp.Evaluate(`(document.querySelector(".activity-bell__count")?.textContent ?? "").trim()`, &afterSetup),

		navigatedTo(f.ContextPath(), `.mission-control`),
		chromedp.Evaluate(`(document.querySelector(".activity-bell__count")?.textContent ?? "").trim()`, &backHome),
	)

	assert.Equal(t, "1", onHome, "a failed job raises the badge")
	assert.Empty(t, afterModPage, "the acknowledged failure stays acknowledged on the full mod page's own bell")
	assert.Empty(t, afterSearch, "...and on the search page's")
	assert.Empty(t, afterSetup, "...and on setup's")
	assert.Empty(t, backHome, "...and back on Mission Control's, which remounted too")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ActivityBell_AcknowledgementSurvivesAReload(t *testing.T) {
	f := newE2EFixtureWithDrillInModsAndFailingDeploy(t)

	var afterReload string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		failADeploy("1"),
		openAndCloseTheBell(),
		bellCountIs(""),

		chromedp.Reload(),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		// A reload re-seeds the bell from the stream's snapshot, which says
		// nothing until it arrives; a NEW failure queues behind that snapshot
		// on the same stream, so once it is on screen the old one has been
		// weighed too. The badge then counts the new one alone.
		failADeploy(""),
		textContent(`.activity-bell__count`, &afterReload),
	)

	assert.Equal(t, "1", afterReload,
		"after a reload the earlier failure is still acknowledged - only the new one counts")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ActivityBell_ANewFailureRaisesTheBadgeAgain(t *testing.T) {
	f := newE2EFixtureWithDrillInModsAndFailingDeploy(t)

	var again string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		failADeploy("1"),
		openAndCloseTheBell(),
		bellCountIs(""),
		failADeploy("1"),
		textContent(`.activity-bell__count`, &again),
	)

	assert.Equal(t, "1", again, "a failure that finishes after the acknowledgement raises the badge")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ActivityBell_OnlyFailuresAfterTheAcknowledgementCount(t *testing.T) {
	f := newE2EFixtureWithDrillInModsAndFailingDeploy(t)

	var third string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		failADeploy("1"),
		failADeploy("2"),
		openAndCloseTheBell(),
		bellCountIs(""),
		failADeploy("1"),
		textContent(`.activity-bell__count`, &third),
	)

	assert.Equal(t, "1", third, "two failures acknowledged, a third arrives: the badge shows only the new one")
	assert.Empty(t, f.BrowserErrors())
}

// The watermark's storage contract, on the module itself: persisted in
// localStorage, read back by a fresh store, and an in-memory value when
// storage cannot be used at all.
func TestE2E_ActivityAck_PersistsAndDegradesWithoutStorage(t *testing.T) {
	f := newE2EFixture(t)

	var got map[string]any
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { initialState, writeActivityAck } = await import("/static/app/store.js");
			localStorage.clear();
			const fresh = initialState().activityAckedAt;
			writeActivityAck(1234567);
			const persisted = initialState().activityAckedAt;

			// Storage that refuses everything: reads and writes must not throw.
			const realGet = Storage.prototype.getItem, realSet = Storage.prototype.setItem;
			Storage.prototype.getItem = () => { throw new Error("denied"); };
			Storage.prototype.setItem = () => { throw new Error("denied"); };
			let threw = false, blocked;
			try { writeActivityAck(99); blocked = initialState().activityAckedAt; } catch { threw = true; }
			Storage.prototype.getItem = realGet; Storage.prototype.setItem = realSet;
			localStorage.setItem("lmm.activity.acknowledgedAt", "not a number");
			const garbage = initialState().activityAckedAt;
			localStorage.clear();
			return { fresh, persisted, threw, blocked, garbage };
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	assert.EqualValues(t, 0, got["fresh"])
	assert.EqualValues(t, 1234567, got["persisted"], "a fresh store reads what an earlier page view acknowledged")
	assert.Equal(t, false, got["threw"], "unavailable storage is not an error")
	assert.EqualValues(t, 0, got["blocked"], "unreadable storage reads as never acknowledged")
	assert.EqualValues(t, 0, got["garbage"], "a corrupt value is never acknowledged")
	assert.Empty(t, f.BrowserErrors())
}

package serve_test

import (
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordTerminalLabelsJS starts recording, from the moment it runs, every
// distinct label the finished readout inside container paints - one entry
// per change, in order. A MutationObserver rather than a poll: the bug this
// guards against (issue 364) was a label that was wrong for a single frame,
// which a poll samples past and an observer cannot.
func recordTerminalLabelsJS(container string) string {
	return fmt.Sprintf(`(() => {
	window.__terminalLabels = [];
	let last = null;
	const sample = () => {
		const el = document.querySelector(%q + ' .job-progress:not([data-state="running"]):not([data-state="queued"]) .job-progress__text');
		if (!el) return;
		const text = el.textContent.trim();
		if (text === last) return;
		last = text;
		window.__terminalLabels.push(text);
	};
	sample();
	new MutationObserver(sample).observe(document.body,
		{subtree: true, childList: true, attributes: true, characterData: true});
})()`, container)
}

// TestE2E_JobTerminalLabelIsTheTallyOnItsFirstFrame is issue 364: the
// control that started an update batch used to render its terminal state
// the instant the job summary said "succeeded", while the outcome tally
// was a second read of the job's result - so it said "Done" for a frame or
// more before correcting itself to "1 applied / 1 skipped" (the flake #344
// worked around by waiting the label out). The tally is now on the job
// summary itself, so the FIRST terminal label is already the right one:
// every label the readout ever painted is recorded from before the job
// starts, and "Done" must never be among them.
func TestE2E_JobTerminalLabelIsTheTallyOnItsFirstFrame(t *testing.T) {
	f := newE2EFixtureWithALockedAndAnUnlockedUpdate(t)

	var labels []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates`, chromedp.ByQuery),
		chromedp.Click(`.card--updates input[aria-label="Select Better Boots for update"]`, chromedp.ByQuery),
		chromedp.Click(`.card--updates input[aria-label="Select Great Gloves for update"]`, chromedp.ByQuery),
		chromedp.Click(`.card--updates [data-action="update-selected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		chromedp.Evaluate(recordTerminalLabelsJS(".card--updates"), nil),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		pollUntil(`window.__terminalLabels.length > 0`),
		chromedp.Evaluate(`window.__terminalLabels`, &labels),
	)

	require.NotEmpty(t, labels)
	assert.Equal(t, "1 applied / 1 skipped", labels[0],
		"the first terminal frame must already carry the tally (all labels: %q)", labels)
	assert.NotContains(t, labels, "Done",
		"the readout must never paint a bare Done over a batch with an outcome to report")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_JobTerminalLabelOnAnUnmountingCardIsNeverWrong is the regression
// the first attempt at issue 364 caused, held off: Mission Control's profile
// card disappears the moment its apply leaves nothing to say, so a readout
// that WAITED for a second read before rendering its outcome showed none at
// all. The tally now arrives with the summary, so nothing waits. A profile
// apply has no batch tally, so any terminal label the card manages to paint
// before it unmounts is the plain success word; the job must succeed and the
// card must go.
func TestE2E_JobTerminalLabelOnAnUnmountingCardIsNeverWrong(t *testing.T) {
	f := newE2EFixtureWithAnUnappliedProfile(t)

	var labels []string
	var failed bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--profile`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="apply-profile"]`),
		chromedp.WaitVisible(`.modal[data-kind="profile_apply"] .plan`, chromedp.ByQuery),
		chromedp.Evaluate(recordTerminalLabelsJS(".card--profile"), nil),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		waitGone(`.card--profile`),
		chromedp.Evaluate(`window.__terminalLabels`, &labels),
		chromedp.Evaluate(`document.querySelector('.job-progress[data-state="failed"]') !== null`, &failed),
	)

	assert.False(t, failed)
	for _, label := range labels {
		assert.Equal(t, "Done", label, "a profile apply has no tally; its card says Done or nothing (all labels: %q)", labels)
	}
	assert.Empty(t, f.BrowserErrors())
}

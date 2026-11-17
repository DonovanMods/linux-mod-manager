package serve_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runningReadoutStep is one state the running readout painted: its text,
// and the bar's fill width in percent (-1 for an indeterminate bar).
type runningReadoutStep struct {
	Text  string  `json:"text"`
	Width float64 `json:"width"`
}

// recordRunningReadoutJS starts recording, from the moment it runs, every
// distinct state the RUNNING readout inside container paints - its text and
// its bar's width - one entry per change, in order. An observer rather than
// a poll, for the reason recordTerminalLabelsJS gives: an update's steps
// after its download each last a few milliseconds here, which a poll would
// sample straight past.
func recordRunningReadoutJS(container string) string {
	return fmt.Sprintf(`(() => {
	window.__runningSteps = [];
	let last = null;
	const sample = () => {
		const el = document.querySelector(%q + ' .job-progress[data-state="running"]');
		if (!el) return;
		const text = el.querySelector('.job-progress__text')?.textContent.trim() ?? '';
		const bar = el.querySelector('.job-progress__bar');
		const fill = el.querySelector('.job-progress__fill');
		const width = bar?.classList.contains('job-progress__bar--indeterminate')
			? -1 : parseFloat(fill?.style.width ?? '');
		const key = text + '|' + width;
		if (key === last) return;
		last = key;
		window.__runningSteps.push({text, width: Number.isFinite(width) ? width : -1});
	};
	sample();
	new MutationObserver(sample).observe(document.body,
		{subtree: true, childList: true, attributes: true, characterData: true});
})()`, container)
}

// TestE2E_UpdateReadoutNamesEachStepAfterTheDownload is issue 664: an update's
// inline readout said "Download done · <mod>" and then nothing more while the
// update extracted, replaced the deployed files and recorded the new version
// - and its bar sat where the download left it. Every step after the
// download is now named for the mod, the bar moves forward on each, and the
// readout ends on the batch's outcome.
func TestE2E_UpdateReadoutNamesEachStepAfterTheDownload(t *testing.T) {
	// The download is held until the readout is on screen: a job this
	// small otherwise finishes before the control learns its job id, and
	// there is no running readout to watch.
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	f := newE2EFixtureWithALockedAndAnUnlockedUpdateGated(t, gate)

	var steps []runningReadoutStep
	var terminal []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates`, chromedp.ByQuery),
		clickWhenSettled(`.card--updates input[aria-label="Select Great Gloves for update"]`),
		clickWhenSettled(`.card--updates [data-action="update-selected"]`),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		chromedp.Evaluate(recordRunningReadoutJS(".card--updates"), nil),
		chromedp.Evaluate(recordTerminalLabelsJS(".card--updates"), nil),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		chromedp.WaitVisible(`.card--updates .job-progress[data-state="running"]`, chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
		pollUntil(`window.__terminalLabels.length > 0`),
		chromedp.Evaluate(`window.__runningSteps`, &steps),
		chromedp.Evaluate(`window.__terminalLabels`, &terminal),
	)

	t.Logf("readout states: %s", describeSteps(steps))
	texts := make([]string, len(steps))
	for i, s := range steps {
		texts[i] = s.Text
	}
	at := func(text string) int {
		t.Helper()
		i := slices.Index(texts, text)
		require.GreaterOrEqual(t, i, 0, "the readout must show %q (all: %s)", text, describeSteps(steps))
		return i
	}
	extracting := at("Extracting · Great Gloves")
	installing := at("Installing · Great Gloves")
	recording := at("Recording · Great Gloves")
	updated := at("Updated · Great Gloves")
	assert.Less(t, extracting, installing, "%s", describeSteps(steps))
	assert.Less(t, installing, recording, "%s", describeSteps(steps))
	assert.Less(t, recording, updated, "%s", describeSteps(steps))

	// The bar is determinate on every step after the download, and moves
	// forward on each: it no longer sits where the download left it.
	prev := -1.0
	for _, i := range []int{extracting, installing, recording, updated} {
		assert.GreaterOrEqual(t, steps[i].Width, 0.0, "%q has a determinate bar (%s)", steps[i].Text, describeSteps(steps))
		assert.Greater(t, steps[i].Width, prev, "%q moves the bar on (%s)", steps[i].Text, describeSteps(steps))
		prev = steps[i].Width
	}
	assert.Equal(t, 100.0, steps[updated].Width, "the updated mod fills its share of the bar")

	require.NotEmpty(t, terminal)
	assert.Equal(t, "1 applied / 0 failed", terminal[0], "and the readout ends on the batch's outcome")
	assert.Empty(t, f.BrowserErrors())
}

// describeSteps renders recorded readout states for a failure message.
func describeSteps(steps []runningReadoutStep) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		parts[i] = strconv.Quote(s.Text) + "@" + strconv.FormatFloat(s.Width, 'f', -1, 64)
	}
	return strings.Join(parts, ", ")
}

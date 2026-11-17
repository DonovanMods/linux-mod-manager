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

// TestE2E_UpdateStepFraction_NeverGoesBack is #664's review, run against
// progress.js itself in the browser: the bar an update's readout, tray row
// and toast draw (progressFraction / updateStepFraction).
//
//   - A mod with two files is acquired one file after the other, each
//     downloaded then extracted: the second file's download is placed after
//     the first file's extraction, from the frame's file position, rather
//     than back at the start.
//   - A frame with no file position whose step would place it earlier than
//     the item has already reached (the backstop) holds the bar where it is.
//   - An update's frame that is not a step (a warning, a note, a Workshop
//     fetch's heartbeat) holds the bar too, rather than sending it to the
//     item's end or to indeterminate; with nothing to hold yet it is the
//     item's start.
//   - No flow says "Download done" after "Extracting": every download-done
//     phase reads "Downloaded".
func TestE2E_UpdateStepFraction_NeverGoesBack(t *testing.T) {
	f := newE2EFixture(t)

	var got struct {
		TwoFiles  []float64         `json:"twoFiles"`
		Backstop  []float64         `json:"backstop"`
		Held      []float64         `json:"held"`
		Fresh     float64           `json:"fresh"`
		Unplaced  *float64          `json:"unplaced"`
		Positions []float64         `json:"positions"`
		Labels    map[string]string `json:"labels"`
		ItemSteps []bool            `json:"itemSteps"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { progressFraction, humanizePhase, isUpdateItemStep } = await import("/static/app/progress.js");
			const at = (index, total) => ({ op: "update", phase: "update_batch_item", index, total });
			const step = (job, phase, extra = {}) => ({ job_id: job, op: "update", type: "step", phase, ...extra });
			const dl = (job, percent, file_index, file_total) =>
				({ job_id: job, op: "update", type: "download", phase: "update_downloading", percent, file_index, file_total });
			const ex = (job, file_index, file_total) => step(job, "archive_extracting", { file_index, file_total });
			const run = (frames, position) => frames.map((fr) => progressFraction(fr, position));

			const two = "j-two-files";
			const twoFiles = run([
				step(two, "update_batch_item", { index: 1, total: 1 }),
				dl(two, 50, 1, 2), dl(two, 100, 1, 2), ex(two, 1, 2),
				dl(two, 0, 2, 2), dl(two, 50, 2, 2), dl(two, 100, 2, 2), ex(two, 2, 2),
				step(two, "update_download_done"), step(two, "update_installing"),
				step(two, "update_recording"), step(two, "update_mod_updated"),
			], at(1, 1));

			const back = "j-backstop";
			const backstop = run([dl(back, 100), step(back, "archive_extracting"), dl(back, 10)], at(1, 1));

			const hold = "j-hold";
			const held = run([
				step(hold, "update_installing"),
				step(hold, "update_warning"),
				step(hold, "update_note"),
				step(hold, "workshop_fetch_progress"),
				{ job_id: hold, op: "update", type: "warning", phase: "download_warning" },
			], at(2, 3));

			const fresh = progressFraction(step("j-fresh", "update_warning"), at(2, 3));
			const unplaced = progressFraction(step("j-unplaced", "update_warning"), undefined);

			// The batch: three mods, each at its own share.
			const batch = "j-batch";
			const positions = [1, 2, 3].flatMap((i) => run([
				step(batch, "update_batch_item", { index: i, total: 3 }),
				dl(batch, 50, 1, 1), step(batch, "update_installing"), step(batch, "update_mod_updated"),
			], at(i, 3)));

			const labels = {};
			for (const p of ["update_download_done", "deploy_download_done", "switch_download_done",
				"import_download_done", "install_download_done", "install_dep_download_done"]) {
				labels[p] = humanizePhase(p);
			}
			// The toast names an item's own step, never a warning or the
			// batch's bracket (which names the item itself).
			const itemSteps = [
				isUpdateItemStep(step("j", "update_installing")),
				isUpdateItemStep(step("j", "update_warning")),
				isUpdateItemStep(step("j", "update_batch_item", { index: 1, total: 2 })),
				isUpdateItemStep({ op: "install", phase: "install_deploying" }),
			];
			return { twoFiles, backstop, held, fresh, unplaced, positions, labels, itemSteps };
		})()`, &got, awaitPromise),
	)

	nonDecreasing := func(name string, values []float64) {
		t.Helper()
		for i := 1; i < len(values); i++ {
			assert.GreaterOrEqual(t, values[i], values[i-1], "%s: step %d went back (%v)", name, i, values)
		}
	}
	require.Len(t, got.TwoFiles, 12)
	nonDecreasing("two files", got.TwoFiles)
	assert.Greater(t, got.TwoFiles[4], got.TwoFiles[1], "the second file's download starts after the first file's (%v)", got.TwoFiles)
	assert.Greater(t, got.TwoFiles[7], got.TwoFiles[3], "and its extraction after the first file's (%v)", got.TwoFiles)
	assert.Equal(t, 1.0, got.TwoFiles[11])

	require.Len(t, got.Backstop, 3)
	nonDecreasing("backstop", got.Backstop)

	require.Len(t, got.Held, 5)
	for i, v := range got.Held[1:] {
		assert.Equal(t, got.Held[0], v, "a non-step frame %d holds the bar where the step left it (%v)", i+1, got.Held)
	}
	assert.InDelta(t, 1.0/3, got.Fresh, 1e-9, "with nothing held, the item's start")
	assert.Nil(t, got.Unplaced, "with nothing held and no batch position, the bar cannot say")

	require.Len(t, got.Positions, 12)
	nonDecreasing("batch", got.Positions)
	assert.InDelta(t, 1.0/3, got.Positions[3], 1e-9, "the first mod fills its third")
	assert.InDelta(t, 1.0/3, got.Positions[4], 1e-9, "the second starts where the first ended")
	assert.Equal(t, 1.0, got.Positions[11])

	assert.Equal(t, []bool{true, false, false, false}, got.ItemSteps)

	for phase, label := range got.Labels {
		want := "Downloaded"
		if phase == "install_dep_download_done" {
			want = "Dependency downloaded"
		}
		assert.Equal(t, want, label, "%s", phase)
	}
	assert.Empty(t, f.BrowserErrors())
}

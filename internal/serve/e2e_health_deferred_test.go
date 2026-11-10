package serve_test

import (
	"context"
	"sync"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_HealthWaitsForTheAutoUpdateJob is #588 as the owner saw it:
// Mission Control opened with an automatic update (#587) running. While
// its job is mid-update - held at the download, holding the mutation slot
// - the Health card says it is waiting for the job and lists no finding;
// once the job is done, the line goes and the real check (clean) leaves no
// Health card at all.
func TestE2E_HealthWaitsForTheAutoUpdateJob(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	f := newE2EFixtureWithAnAutoUpdate(t, gate, nil, nil)
	// Checksums for the seeded files, so the real check is clean and its
	// absence of a Health card means something.
	for _, modID := range []string{e2eAutoModID, e2eNotifyModID} {
		require.NoError(t, f.Svc.SaveFileChecksum(t.Context(), "fake", modID, f.Game.ID, "default", modID+"-1.0", "seeded"))
	}

	var line string
	var rows int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-testid="updates-auto"][data-state="applying"]`, chromedp.ByQuery),
		// The job holds the slot now. A fresh page load hydrates health
		// under it, as the owner's did.
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--health [data-testid="health-deferred"]`, chromedp.ByQuery),
		chromedp.Text(`.card--health [data-testid="health-deferred"]`, &line, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('.card--health .card__row--finding').length`, &rows),
		chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
		waitGone(`[data-testid="health-deferred"]`),
		waitGone(`.card--health`),
	)

	assert.Equal(t, "Checking after the current job finishes…", line)
	assert.Zero(t, rows, "a check deferred behind the job lists no finding")
	assert.Equal(t, "2.0", e2eInstalledVersion(t, f, e2eAutoModID))
	assert.Empty(t, f.BrowserErrors())
}

package serve_test

// #601: a download the source refuses for the ACCOUNT (a free NexusMods key)
// rather than the mod. Driven in a real browser because the claim is what a
// user reads and clicks: the headline says the account is why, and the way
// out - "Open on <source>" and "Install from file…" - is the manual
// download's, unchanged.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const e2eAccountReason = "NexusMods serves downloads through its API to Premium accounts only, and this API key's account is not Premium: download the file from the mod's page instead"

// newE2EFixtureWithAccountRestrictedInstall is the manual-install fixture
// with MapUtils refused for the account instead of by its author.
func newE2EFixtureWithAccountRestrictedInstall(t *testing.T) e2eSearchFixture {
	t.Helper()
	f := newE2EFixtureWithManualInstall(t)
	f.Src.urlErrs["maputils"] = &source.AccountRestrictedError{Reason: e2eAccountReason}
	return f
}

func TestE2E_AccountRestricted_InstallFailureSaysTheAccountAndOffersTheFile(t *testing.T) {
	f := newE2EFixtureWithAccountRestrictedInstall(t)
	row := searchResultRow("fake", "maputils")

	var view installFailureView
	var link *e2eLink
	f.runInBrowser(t,
		failSearchInstall(f),
		awaitSourceNamed(row+" .job-progress"),
		chromedp.Evaluate(installFailureJS, &view),
		linkIn(row+" .job-progress", &link),
	)
	assert.Equal(t, "Your "+e2eSourceName+" account isn't Premium, so "+e2eSourceName+" won't let lmm download "+e2eMapUtilsName+".",
		view.Headline, "the account is why, and the headline says so")
	assert.Contains(t, view.Hint, "MapUtils-1.2.24-classic.zip", "the manual download's own hint")
	assert.Contains(t, view.Raw, "Premium accounts only", "the engine's text is kept, collapsed")
	assert.False(t, view.RawOpen)
	assert.Equal(t, "Install from file…", view.FromFile, "the way out is the manual download's")
	assertModPageLink(t, link, e2eMapUtilsPage, e2eMapUtilsName)

	// And nothing is recorded against the mod: the refusal was the account's.
	recorded, err := f.Svc.ModDetail(t.Context(), f.Game, "default", "fake", "maputils")
	require.NoError(t, err)
	assert.False(t, recorded.Mod.ManualOnly)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

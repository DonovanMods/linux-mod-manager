package serve_test

// #517: a finished verify --fix job says what it repaired and what still
// fails. Driven through the real engine in a real browser - the claim is
// about the rows a person reads and the link they click, both of which only
// exist once the SPA has executed.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const (
	e2eRepairCachedPage = "https://example.test/mods/cached-mod"
	e2eRepairStuckPage  = "https://example.test/mods/stuck-mod"

	e2eFilledFromCache = "Checksum filled from the cached files (the source won't serve this file)"
)

// newE2EFixtureWithRepairableMods installs two mods through the real
// installer, then breaks each the way #514 describes. "Cached Mod" loses its
// checksum but keeps a complete cache entry, so --fix fills it from the
// cache; "Stuck Mod" loses the cache entry itself, so --fix must download -
// and its source refuses, leaving a warning that points at the mod's page.
func newE2EFixtureWithRepairableMods(t *testing.T) e2eSearchFixture {
	t.Helper()
	f := newE2EFixtureWithSearchableMods(t)
	ctx := t.Context()

	for _, m := range []struct{ id, name, file, member, page string }{
		{"cached", "Cached Mod", "cf1", "Mods/cached.pak", e2eRepairCachedPage},
		{"stuck", "Stuck Mod", "sf1", "Mods/stuck.pak", e2eRepairStuckPage},
	} {
		f.Src.addMod(e2eSearchSourceMod{
			mod:     domain.Mod{ID: m.id, SourceID: "fake", Name: m.name, Version: "1.0", SourceURL: m.page},
			files:   []domain.DownloadableFile{{ID: m.file, Name: "Main", FileName: m.id + ".zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32}},
			members: map[string]string{m.file: m.member},
		})
		plan, err := f.Svc.PlanInstall(ctx, f.Game, "default", "fake", m.id, false)
		require.NoError(t, err)
		_, err = f.Svc.ApplyInstall(ctx, f.Game, plan, core.InstallOptions{}, nil)
		require.NoError(t, err)
	}
	require.NoError(t, f.Svc.SaveFileChecksum(ctx, "fake", "cached", f.Game.ID, "default", "cf1", ""))
	require.NoError(t, f.Svc.GetGameCache(f.Game).Delete(f.Game.ID, "fake", "stuck", "1.0"))
	refusal := &source.ManualDownloadError{Reason: "the author has turned off API downloads"}
	f.Src.urlErrs = map[string]error{"cached": refusal, "stuck": refusal}
	return f
}

// e2eVerifyResultRow is one rendered row of the result view.
type e2eVerifyResultRow struct {
	Mod    string `json:"mod"`
	Group  string `json:"group"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

// repairAllFromHealthCard runs the Health card's Repair all and waits for the
// finished job's result view inside scope.
func repairAllFromHealthCard(f e2eSearchFixture) []chromedp.Action {
	return []chromedp.Action{
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.card--health [data-action="repair-all"]`),
		chromedp.WaitVisible(`.modal[data-kind="verify_fix"] .plan--verify-fix`, chromedp.ByQuery),
		chromedp.Click(`.modal[data-kind="verify_fix"] [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal[data-kind="verify_fix"]`),
	}
}

// resultRowsJS reads the result view inside scope as rows plus its landmark.
func resultRowsJS(scope string) string {
	return `(() => {
		const root = document.querySelector(` + jsString(scope+` [data-testid="verify-fix-result"]`) + `);
		if (!root) return null;
		const rows = [...root.querySelectorAll("li[data-mod]")].map((li) => ({
			mod: li.getAttribute("data-mod"),
			group: li.closest("[data-group]").getAttribute("data-group"),
			status: li.querySelector(".verify-result__status")?.textContent.trim() ?? "",
			text: li.textContent.replace(/\s+/g, " ").trim(),
		}));
		return {
			label: root.getAttribute("aria-label"),
			headings: [...root.querySelectorAll("h4")].map((h) => h.textContent.replace(/\s+/g, " ").trim()),
			rows,
		};
	})()`
}

type e2eVerifyResultView struct {
	Label    string               `json:"label"`
	Headings []string             `json:"headings"`
	Rows     []e2eVerifyResultRow `json:"rows"`
}

func assertRepairResultRows(t *testing.T, got *e2eVerifyResultView) {
	t.Helper()
	require.NotNil(t, got, "the finished job must render its findings")
	assert.Equal(t, "Repair results", got.Label, "a labelled region")
	// What still needs attention leads.
	// The third repair is the dangling deployment Stuck Mod's missing cache
	// entry left behind: --fix sweeps it, and says so.
	assert.Equal(t, []string{"Still needs attention (1)", "Repaired (2)"}, got.Headings)

	byMod := map[string]e2eVerifyResultRow{}
	for _, r := range got.Rows {
		byMod[r.Mod] = r
	}
	require.Len(t, got.Rows, 3, "only repaired and still-failing rows are listed, never the healthy ones: %+v", got.Rows)
	assert.Equal(t, "repaired", byMod[""].Group, "a repair with no mod of its own is still listed")
	assert.Contains(t, byMod[""].Text, "dangling link into lmm cache")

	stuck := byMod["stuck"]
	assert.Equal(t, "attention", stuck.Group)
	assert.Equal(t, "Still failing", stuck.Status, "status is text, not colour alone")
	assert.Contains(t, stuck.Text, "Stuck Mod")
	assert.Contains(t, stuck.Text, "the author has turned off API downloads", "the failure's own reason")

	cached := byMod["cached"]
	assert.Equal(t, "repaired", cached.Group)
	assert.Equal(t, "Repaired", cached.Status)
	assert.Contains(t, cached.Text, "Cached Mod")
	assert.Contains(t, cached.Text, e2eFilledFromCache)
	assert.Equal(t, "attention", got.Rows[0].Group, "attention rows come first in document order")
}

func TestE2E_VerifyFixResult_InlineOnTheHealthCard(t *testing.T) {
	f := newE2EFixtureWithRepairableMods(t)

	var view *e2eVerifyResultView
	var link, cachedLink *e2eLink
	f.runInBrowser(t, repairAllFromHealthCard(f)...)
	f.runInBrowser(t,
		pollUntil(`document.querySelector('.card--health [data-testid="verify-fix-result"] a.mod-page-link')?.textContent.includes("E2E Search Source")`),
		chromedp.Evaluate(resultRowsJS(".card--health"), &view),
		linkIn(`.card--health [data-mod="stuck"]`, &link),
		linkIn(`.card--health [data-mod="cached"]`, &cachedLink),
	)
	assertRepairResultRows(t, view)
	assertModPageLink(t, link, e2eRepairStuckPage, "Stuck Mod")
	assert.Nil(t, cachedLink, "a repaired row has nowhere to send anyone")
	assert.Empty(t, f.BrowserErrors())

	// The repair itself really happened: the checksum is back.
	files, err := f.Svc.GetFilesWithChecksums(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)
	for _, file := range files {
		if file.ModID == "cached" {
			assert.NotEmpty(t, file.Checksum)
		}
	}
}

func TestE2E_VerifyFixResult_InTheActivityTray(t *testing.T) {
	f := newE2EFixtureWithRepairableMods(t)

	var view *e2eVerifyResultView
	var link *e2eLink
	var events string
	f.runInBrowser(t, repairAllFromHealthCard(f)...)
	f.runInBrowser(t,
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitVisible(`.tray__row[data-state="succeeded"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.tray__row [data-testid="verify-fix-result"] a.mod-page-link')?.textContent.includes("E2E Search Source")`),
		chromedp.Evaluate(resultRowsJS(".tray__row"), &view),
		linkIn(`.tray__row [data-mod="stuck"]`, &link),
		// Expanded, the job's own stream reads the repair's sub-lines out
		// the way it does every other kind's.
		chromedp.Click(`.tray__row[data-state="succeeded"] .tray__summary`, chromedp.ByQuery),
		pollUntil(`document.querySelector(".tray__events")?.textContent.includes("Download it manually from")`),
		textContent(`.tray__events`, &events),
	)
	assertRepairResultRows(t, view)
	assertModPageLink(t, link, e2eRepairStuckPage, "Stuck Mod")
	assert.Contains(t, events, e2eFilledFromCache)
	assert.Contains(t, events, "Download it manually from: "+e2eRepairStuckPage)
	assert.Empty(t, f.BrowserErrors())
}

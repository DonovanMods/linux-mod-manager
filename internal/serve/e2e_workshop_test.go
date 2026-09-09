package serve_test

// Browser scenarios for issue 269's Tier-1 web surfaces. These are the only
// tests that EXECUTE the SPA, so they are the only ones that can see a Steam
// badge that never renders, an action that stays clickable on a mod lmm must
// not touch, or a plan renderer whose module path 404s.
//
// Skipped when no Chrome/Chromium is on PATH, the 7z pattern the rest of
// this suite follows.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	e2eWorkshopSourceID = "steamworkshop"
	e2eWorkshopFileID   = "3617086610"
)

// e2eWorkshopSource is the fake Steam Workshop source the browser scenarios
// run against: it answers ModDetail's live read (so the slide-over and the
// mod page resolve without a network error) plus the two optional
// capabilities the adopt flow uses.
type e2eWorkshopSource struct {
	*fakeSource
	scan source.WorkshopScan
}

func (s *e2eWorkshopSource) ScanWorkshopItems(context.Context, string) (source.WorkshopScan, error) {
	return s.scan, nil
}

func (s *e2eWorkshopSource) DescribeMods(_ context.Context, _ string, ids []string, _ bool) ([]source.ModDescription, error) {
	out := make([]source.ModDescription, 0, len(ids))
	for _, id := range ids {
		out = append(out, source.ModDescription{
			ModID: id,
			Mod: domain.Mod{
				ID: id, SourceID: e2eWorkshopSourceID, Name: "Sample Workshop Item",
				Author: "76561198000000000",
			},
		})
	}
	return out, nil
}

// newE2EWorkshopFixture seeds one game mapped to the fake workshop source,
// with one subscribed item ALREADY TRACKED as an external mod - the state
// every rendering assertion below is about.
func newE2EWorkshopFixture(t *testing.T) e2eFixture {
	t.Helper()
	src := &e2eWorkshopSource{fakeSource: newFakeSource(e2eWorkshopSourceID)}
	src.addMod(fakeSourceMod{Mod: domain.Mod{
		ID: e2eWorkshopFileID, SourceID: e2eWorkshopSourceID,
		Name: "Sample Workshop Item", Version: "7987119735124793734",
		Author: "76561198000000000",
	}})

	steamDir := t.TempDir()
	src.scan = source.WorkshopScan{
		Roots: []string{"/steam"},
		Items: []domain.WorkshopItem{{
			FileID: e2eWorkshopFileID, Path: steamDir,
			Manifest: "7987119735124793734", TimeUpdated: 1764767935,
		}},
	}

	f := newE2EFixtureFromSource(t, src.fakeSource)
	// The registry holds the bare fakeSource after the helper above; replace
	// it with the workshop-capable wrapper so the plan kind can find it.
	f.Svc.RegisterSource(src)
	f.Game.SourceIDs[e2eWorkshopSourceID] = "1133870"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID: e2eWorkshopFileID, SourceID: e2eWorkshopSourceID,
			Name: "Sample Workshop Item", Version: "7987119735124793734",
			Author: "76561198000000000", GameID: f.Game.ID,
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		External:     true,
		ExternalPath: steamDir,
	}))
	return f
}

// TestE2E_Workshop_LibraryRowCarriesTheSteamBadge proves the badge renders
// at all - a fact only a browser can establish, since it is derived in
// modrows.js and rendered by library.js.
func TestE2E_Workshop_LibraryRowCarriesTheSteamBadge(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	var badges, count string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row"))
				.find((r) => r.textContent.includes("Sample Workshop Item"))
				.querySelector("td.col--badges").textContent;
		`, &badges),
		chromedp.Evaluate(`document.querySelector(".library__toolbar").textContent;`, &count),
	)
	assert.Contains(t, badges, "Steam", "an external row is badged as Steam-owned")
	assert.Contains(t, count, "1 tracked by Steam")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_ModPanelHidesTheActionsLmmCannotPerform is the assertion
// the design's §2 table turns into a UI promise: deploy/enable/disable and
// the update action are ABSENT for an external mod, not present-and-refused,
// and uninstall is reworded to what it actually does.
func TestE2E_Workshop_ModPanelHidesTheActionsLmmCannotPerform(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	var actions, managed string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath(e2eWorkshopSourceID, e2eWorkshopFileID)),
		chromedp.WaitVisible(`[data-testid="managed-by-steam"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".slide-over__actions").textContent;`, &actions),
		chromedp.Evaluate(`document.querySelector('[data-testid="managed-by-steam"]').textContent;`, &managed),
	)
	assert.NotContains(t, actions, "Disable", "lmm cannot disable a Steam Workshop item")
	assert.NotContains(t, actions, "Enable")
	assert.NotContains(t, actions, "Update")
	assert.Contains(t, actions, "Stop tracking", "uninstall says what it really does")
	assert.Contains(t, managed, "Steam owns its files")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_FullModPageHidesRelinkAndRollback covers the other
// drill-in surface: the same rules, the two actions only that page offers.
func TestE2E_Workshop_FullModPageHidesRelinkAndRollback(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	var page string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath(e2eWorkshopSourceID, e2eWorkshopFileID)),
		chromedp.WaitVisible(`[data-testid="managed-by-steam"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".mod-page").textContent;`, &page),
	)
	assert.Contains(t, page, "Managed by Steam")
	assert.NotContains(t, page, "Re-link…", "there is no link to move")
	assert.NotContains(t, page, "Roll back", "lmm never held a previous copy")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_AdoptPlanModalRendersItsOwnPreview drives the plan
// endpoint from the browser and asserts plan_workshop_adopt.js actually
// rendered - which also proves its module path resolves, the one class of
// break nothing but a browser catches.
func TestE2E_Workshop_AdoptPlanModalRendersItsOwnPreview(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	// A second, untracked item, so the plan has something to preview.
	src, err := f.Svc.GetSource(e2eWorkshopSourceID)
	require.NoError(t, err)
	ws, ok := src.(*e2eWorkshopSource)
	require.True(t, ok)
	ws.scan.Items = append(ws.scan.Items, domain.WorkshopItem{
		FileID: "3512001122", Path: t.TempDir(),
		Manifest: "1122334455667788990", TimeUpdated: 1758000000,
	})

	var modal string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			window.__lmmOpenPlan({
				kind: "workshop_adopt",
				origin: "e2e-workshop",
				title: "Track Steam Workshop items",
				confirmLabel: "Track",
				options: {},
			});
		`, nil),
		chromedp.WaitVisible(`.plan--workshop-adopt`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".plan--workshop-adopt").textContent;`, &modal),
	)
	assert.Contains(t, modal, "never moves, copies or deletes their files")
	assert.Contains(t, modal, "2 subscribed items across 1 Steam library")
	assert.Contains(t, modal, "To track (1)")
	assert.Contains(t, modal, "2025-09-16", "the revision date, never the content id")
	assert.NotContains(t, modal, "1122334455667788990")
	assert.Empty(t, f.BrowserErrors())
}

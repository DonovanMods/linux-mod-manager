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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	e2eWorkshopSourceID = "steamworkshop"
	e2eWorkshopFileID   = "3617086610"
	// e2eWorkshopManifest is Steam's 19-digit content id for the tracked
	// item - what domain.Mod.Version holds for an external mod, and what
	// the approval note's version DISPLAY rule says no human-facing
	// surface may print as a version. Every scenario below asserts its
	// ABSENCE, so it lives here rather than as a literal per test.
	e2eWorkshopManifest = "7987119735124793734"
	// e2eWorkshopTimeUpdated is the item's Steam revision time, and
	// e2eWorkshopRevisionDate the date every surface shows in the
	// manifest's place. The scan and the tracked row carry the same
	// instant, because they are the same fact read two ways.
	e2eWorkshopTimeUpdated  = 1764767935
	e2eWorkshopRevisionDate = "2025-12-03"
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
				// The real source answers a SourceURL here, and the
				// collection plan's per-item list links to it (W2 review,
				// Important 3) - core never builds one itself.
				SourceURL: "https://steamcommunity.com/sharedfiles/filedetails/?id=" + id,
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
		Name: "Sample Workshop Item", Version: e2eWorkshopManifest,
		Author:    "76561198000000000",
		UpdatedAt: time.Unix(e2eWorkshopTimeUpdated, 0).UTC(),
	}})

	steamDir := t.TempDir()
	src.scan = source.WorkshopScan{
		Roots: []string{"/steam"},
		Items: []domain.WorkshopItem{{
			FileID: e2eWorkshopFileID, Path: steamDir,
			Manifest: e2eWorkshopManifest, TimeUpdated: e2eWorkshopTimeUpdated,
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
			Name: "Sample Workshop Item", Version: e2eWorkshopManifest,
			Author: "76561198000000000", GameID: f.Game.ID,
			// The revision date every human-facing surface shows in the
			// manifest's place (issue 269's version DISPLAY rule). Without it
			// the row's updated_at is the zero time, displayVersion falls
			// back to an em dash, and no test can see a wrong date.
			UpdatedAt: time.Unix(e2eWorkshopTimeUpdated, 0).UTC(),
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		External:     true,
		ExternalPath: steamDir,
	}))
	// The profile ref ApplyWorkshopAdopt writes alongside the DB row: without
	// it the fixture is a state adopt never produces, and every profile-scoped
	// flow (deploy's plan among them) would simply not see the item.
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: e2eWorkshopSourceID, ModID: e2eWorkshopFileID, Version: e2eWorkshopManifest}))
	return f
}

// seedWorkshopManagedMod adds one ORDINARY, lmm-managed mod to the workshop
// fixture: an all-external profile has nothing to deploy and nothing to
// purge by construction, so a scenario about how the external row reads
// ALONGSIDE managed ones needs one of each.
func seedWorkshopManagedMod(t *testing.T, f e2eFixture) {
	t.Helper()
	src, err := f.Svc.GetSource(e2eWorkshopSourceID)
	require.NoError(t, err)
	ws, ok := src.(*e2eWorkshopSource)
	require.True(t, ok)
	ws.addMod(fakeSourceMod{Mod: domain.Mod{
		ID: "managed-1", SourceID: e2eWorkshopSourceID, Name: "Managed Mod", Version: "1.0",
	}})
	seedInstalledMod(t, f.Svc, f.Game, domain.Mod{
		ID: "managed-1", SourceID: e2eWorkshopSourceID, Name: "Managed Mod",
		Version: "1.0", GameID: f.Game.ID,
	}, true, map[string][]byte{"managed.pak": []byte("managed")})
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: e2eWorkshopSourceID, ModID: "managed-1", Version: "1.0"}))
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

// TestE2E_Workshop_ModPanelOffersNoAutoUpdatePolicy is the same rule applied
// to the settings block: "auto" is refused server-side, so offering it could
// only produce an inline error - and this panel's own ManagedBySteam
// comment states the principle it follows, "not shown at all rather than
// shown-and-refused".
func TestE2E_Workshop_ModPanelOffersNoAutoUpdatePolicy(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	var options []string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath(e2eWorkshopSourceID, e2eWorkshopFileID)),
		chromedp.WaitVisible(`[data-testid="managed-by-steam"]`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".slide-over__settings select option"))
				.map((o) => o.value);
		`, &options),
	)
	assert.Equal(t, []string{"notify", "pinned"}, options,
		"the two policies that mean something for an item Steam owns")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_StopTrackingConfirmSaysOnlyWhatItReallyDoes closes the
// gap between that button and the modal it opens: the confirmation used to
// read "Not currently deployed - nothing to remove" (contradicting
// Deployed: true, which the design makes load-bearing) and "The cached
// download is deleted too", and still offered "Keep the cached download" -
// so the body and the button disagreed on the same screen.
func TestE2E_Workshop_StopTrackingConfirmSaysOnlyWhatItReallyDoes(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	var body string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath(e2eWorkshopSourceID, e2eWorkshopFileID)),
		chromedp.WaitVisible(`[data-testid="managed-by-steam"]`, chromedp.ByQuery),
		chromedp.Click(`.slide-over__actions .button--danger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="uninstall"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="uninstall"]`, &body),
	)
	assert.Contains(t, body, core.UninstallExternalNote)
	assert.NotContains(t, body, "The cached download is deleted too",
		"there is no cache entry for a Steam-owned item")
	assert.NotContains(t, body, "Keep the cached download",
		"an option that can do nothing is not offered")
	assert.NotContains(t, body, "Not currently deployed",
		"it IS deployed - by Steam, which is the whole point")
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

	// N2: the same rule the slide-over already keeps for the SAME mod. This
	// page builds its own settingsRow literal rather than a modrows.js row,
	// so it never carried `external` and went on offering an "Auto" update
	// policy SetModUpdatePolicy refuses server-side - the present-and-refused
	// shape this page's own ManagedBySteam block argues against.
	var options []string
	f.runInBrowser(t,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-page .slide-over__settings select option"))
				.map((o) => o.value);
		`, &options),
	)
	assert.Equal(t, []string{"notify", "pinned"}, options,
		"the two policies that mean something for an item Steam owns")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_DeployPlanShowsTheExternalRowAsUntouched is the SPA half
// of the dry-run/live agreement the CLI now keeps: an external row appears
// in the deploy plan classed "external", saying what lmm will NOT do with
// it. Before the fix plan_deploy.js fell through to its empty-link-list
// branch and read "no files to link", which is true of it and says nothing.
func TestE2E_Workshop_DeployPlanShowsTheExternalRowAsUntouched(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	seedWorkshopManagedMod(t, f)

	var body string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="deploy"]`),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="deploy"]`, &body),
	)
	assert.Contains(t, body, "Managed Mod")
	assert.Contains(t, body, "managed.pak")
	assert.Contains(t, body, "Sample Workshop Item")
	assert.Contains(t, body, "tracked from Steam — not deployed")
	assert.NotContains(t, body, "no files to link",
		"the external row must say why, not merely that there is nothing")
	assert.NotContains(t, body, e2eWorkshopManifest,
		"the version DISPLAY rule: no human surface prints Steam's content id as a version")
	assert.Contains(t, body, "1.0", "a managed row still shows its own version")

	// `lmm deploy --mod <external>` is refused, so the "Only this mod"
	// narrowing must not offer one.
	var targets []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="deploy"]`),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('.modal select[name="deploy-mod"] option'))
				.map((o) => o.textContent);
		`, &targets),
	)
	assert.Equal(t, []string{"The whole profile", "Managed Mod"}, targets)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_PurgePreviewNamesWhatItLeavesAlone renders design §2's
// reason for PurgePlan.External existing at all: "so the preview says what
// it will not touch". Core populated it and plan_purge.js never read it, so
// the user saw a shorter mod count with no explanation for the difference.
func TestE2E_Workshop_PurgePreviewNamesWhatItLeavesAlone(t *testing.T) {
	f := newE2EWorkshopFixture(t)
	seedWorkshopManagedMod(t, f)

	var body string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
		chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="purge-profile"][data-profile="default"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="purge"] .plan`, chromedp.ByQuery),
		textContent(`[data-testid="purge-external"]`, &body),
	)
	assert.Contains(t, body, "Left alone — tracked from Steam (1)")
	assert.Contains(t, body, "Sample Workshop Item")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_AddModsMenuOmitsTheEntryWithoutTheSource is the other
// half of that entry point: POST /api/v1/plans/workshop_adopt answers 400
// for a game with no steamworkshop mapping, and an entry that can only fail
// is exactly what this menu's "land on EXISTING flows" rule argues against.
func TestE2E_Workshop_AddModsMenuOmitsTheEntryWithoutTheSource(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	var menu string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Click(`[data-action="add-mods"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.add-mods-menu__menu`, chromedp.ByQuery),
		textContent(`.add-mods-menu__menu`, &menu),
	)
	assert.Contains(t, menu, "Adopt untracked mods…", "the existing entries stand")
	assert.NotContains(t, menu, "Track Steam Workshop items…")
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
		// Driven through the REAL control the SPA offers, not
		// window.__lmmOpenPlan - whose own doc comment says no control in
		// this application ever opens one, which made using it here the
		// proof of a missing entry point rather than a way around it.
		chromedp.Click(`[data-action="add-mods"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="track-workshop"]`, chromedp.ByQuery),
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

// TestE2E_Workshop_ModPanelMetaShowsTheRevisionDate is the slide-over's half
// of the approval note's version DISPLAY rule (docs/plans/2026-09-09-steam-
// workshop-design.md, "Approved with notes"): no human-facing surface prints
// the 19-digit manifest as a version - it shows the item's revision date, and
// only the labelled "Steam content id" block underneath names the manifest.
//
// The panel's meta line printed row.version, so the content id appeared
// TWICE on this screen: once unlabelled where a version goes, and once
// correctly labelled by ManagedBySteam. The unlabelled one is the violation.
func TestE2E_Workshop_ModPanelMetaShowsTheRevisionDate(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	var meta, managed string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath(e2eWorkshopSourceID, e2eWorkshopFileID)),
		chromedp.WaitVisible(`[data-testid="managed-by-steam"]`, chromedp.ByQuery),
		textContent(`.slide-over__meta`, &meta),
		textContent(`[data-testid="managed-by-steam"]`, &managed),
	)
	assert.Contains(t, meta, e2eWorkshopRevisionDate, "the meta line shows the revision date")
	assert.NotContains(t, meta, e2eWorkshopManifest,
		"the version DISPLAY rule: never the content id in the slot a version goes")
	assert.Contains(t, managed, "Steam content id: "+e2eWorkshopManifest,
		"the manifest still appears once, labelled, where the design put it")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_FullModPageMetaShowsTheRevisionDate is the same rule on
// the drill-in page the design names by hand: "lmm mod show and the mod page
// show the date and, beneath it, the manifest labelled as such". This page
// builds its own meta line off core.ModFilesReport.Mod rather than a library
// row, so it printed the content id where a version goes - twice on screen,
// exactly as the slide-over did.
func TestE2E_Workshop_FullModPageMetaShowsTheRevisionDate(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	var meta, managed string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath(e2eWorkshopSourceID, e2eWorkshopFileID)),
		chromedp.WaitVisible(`[data-testid="managed-by-steam"]`, chromedp.ByQuery),
		textContent(`.mod-page__meta`, &meta),
		textContent(`[data-testid="managed-by-steam"]`, &managed),
	)
	assert.Contains(t, meta, e2eWorkshopRevisionDate+" installed")
	assert.NotContains(t, meta, e2eWorkshopManifest,
		"the version DISPLAY rule: never the content id in the slot a version goes")
	assert.Contains(t, managed, "Steam content id: "+e2eWorkshopManifest,
		"the labelled block underneath is where the manifest belongs")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_StopTrackingConfirmOmitsTheContentID: the confirm modal's
// own summary line read "Uninstalling Sample Workshop Item
// 7987119735124793734 from profile default", printing Steam's content id in
// the slot a version goes. core.UninstallPlan carries no timestamp to show a
// date instead and the header already names the mod, so the span is simply
// omitted - exactly what plan_deploy.js now does for its external row.
func TestE2E_Workshop_StopTrackingConfirmOmitsTheContentID(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	var summary, body string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath(e2eWorkshopSourceID, e2eWorkshopFileID)),
		chromedp.WaitVisible(`[data-testid="managed-by-steam"]`, chromedp.ByQuery),
		chromedp.Click(`.slide-over__actions .button--danger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="uninstall"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="uninstall"] .plan__summary`, &summary),
		textContent(`.modal[data-kind="uninstall"]`, &body),
	)
	assert.Contains(t, summary, "Uninstalling Sample Workshop Item from profile default.")
	assert.NotContains(t, body, e2eWorkshopManifest,
		"the version DISPLAY rule: no human surface prints Steam's content id as a version")
	assert.Empty(t, f.BrowserErrors())
}

// e2eWorkshopNewManifest is the content id a Workshop UPDATE moves the item
// to - the target version, and as unreadable as the installed one.
const e2eWorkshopNewManifest = "8888888888888888888"

// setWorkshopCatalogVersion moves modID's CATALOG entry to version, which is
// the whole of what fakeSource.CheckUpdates compares against - so this is how
// a scenario gets an update to appear for an already-installed row.
func setWorkshopCatalogVersion(t *testing.T, f e2eFixture, modID, version string) {
	t.Helper()
	src, err := f.Svc.GetSource(e2eWorkshopSourceID)
	require.NoError(t, err)
	ws, ok := src.(*e2eWorkshopSource)
	require.True(t, ok)
	entry, ok := ws.mods[modID]
	require.True(t, ok, "no catalog entry for %s", modID)
	entry.Mod.Version = version
}

// cardRowJS reads one Updates-card row by the mod it names. The rows carry no
// stable selector of their own, and an EXTERNAL row deliberately has no
// checkbox to key off (unlike the locked-row scenarios, which select on
// aria-label), so this matches on the visible name instead.
func cardRowJS(name, expr string) string {
	return `(() => {
		const row = Array.from(document.querySelectorAll(".card--updates .card__row"))
			.find((r) => r.textContent.includes(` + strconvQuote(name) + `));
		return row ? (` + expr + `) : null;
	})()`
}

func strconvQuote(s string) string { return `"` + s + `"` }

// TestE2E_Workshop_UpdatesCardMarksTheExternalRowAndOffersNoTick is the
// headline Tier-1 surface, and it broke the version DISPLAY rule twice over:
// it rendered "Sample Workshop Item 7987119735124793734 →
// 8888888888888888888" - two content ids where a version pair goes - and
// offered the row as a checked, apply-able update with no mark at all, while
// core's own batch declines it (issue 324/269). That is the same defect as the
// deploy dry run: a preview promising what the flow will never do.
//
// A LOCKED row stays tickable with a marker because a lock is the user's own
// reversible choice; an external row can never be applied by lmm under any
// choice available in this UI, so it carries the mark and NO checkbox - it
// never enters the selection, the count, or the batch.
func TestE2E_Workshop_UpdatesCardMarksTheExternalRowAndOffersNoTick(t *testing.T) {
	f := newE2EWorkshopFixture(t)
	seedWorkshopManagedMod(t, f)
	setWorkshopCatalogVersion(t, f, e2eWorkshopFileID, e2eWorkshopNewManifest)
	setWorkshopCatalogVersion(t, f, "managed-1", "2.0")

	var externalRow, managedRow string
	var externalBoxes, managedBoxes int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates`, chromedp.ByQuery),
		chromedp.Evaluate(cardRowJS("Sample Workshop Item", "row.textContent"), &externalRow),
		chromedp.Evaluate(cardRowJS("Sample Workshop Item", `row.querySelectorAll("input[type=checkbox]").length`), &externalBoxes),
		chromedp.Evaluate(cardRowJS("Managed Mod", "row.textContent"), &managedRow),
		chromedp.Evaluate(cardRowJS("Managed Mod", `row.querySelectorAll("input[type=checkbox]").length`), &managedBoxes),
	)

	assert.Contains(t, externalRow, e2eWorkshopRevisionDate+" → newer",
		"the revision date and an honest target, never two content ids")
	assert.NotContains(t, externalRow, e2eWorkshopManifest)
	assert.NotContains(t, externalRow, e2eWorkshopNewManifest)
	assert.Contains(t, externalRow, "will be skipped — Steam applies this itself",
		"marked on the control the user meets first, the sibling of the lock mark")
	assert.Equal(t, 0, externalBoxes,
		"no checkbox: lmm can never apply this update, whatever the user picks")
	assert.Equal(t, 1, managedBoxes, "an ordinary row is unaffected")
	assert.Contains(t, managedRow, "1.0 → 2.0")

	// The apply count excludes it: ticking every box this card offers plans a
	// batch of ONE, and the external row is not in it.
	var title, modal string
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelectorAll(".card--updates .card__row input[type=checkbox]").forEach((cb) => cb.click());`, nil),
		chromedp.Click(`.card--updates [data-action="update-selected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="updates"] .modal__title`, &title),
		textContent(`.modal[data-kind="updates"] .plan`, &modal),
	)
	assert.Equal(t, "Update 1 mod", title, "one applicable update, not two")
	assert.Contains(t, modal, "Managed Mod")
	assert.NotContains(t, modal, "Sample Workshop Item")
	assert.NotContains(t, modal, e2eWorkshopManifest)
	assert.NotContains(t, modal, e2eWorkshopNewManifest)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_BatchUpdateExcludesTheExternalRow covers the library's
// OWN route into the same batch: the row checkboxes and the batch bar's
// Update. updatableSelectedRows() filtered on hasUpdate alone, so a selection
// of one external row enabled the button and planned a batch core would only
// decline.
func TestE2E_Workshop_BatchUpdateExcludesTheExternalRow(t *testing.T) {
	f := newE2EWorkshopFixture(t)
	seedWorkshopManagedMod(t, f)
	setWorkshopCatalogVersion(t, f, e2eWorkshopFileID, e2eWorkshopNewManifest)
	setWorkshopCatalogVersion(t, f, "managed-1", "2.0")

	selectRowJS := func(name string) string {
		return `Array.from(document.querySelectorAll(".mod-row"))
			.find((r) => r.textContent.includes("` + name + `"))
			.querySelector("td.col--select input").click();`
	}

	var externalOnlyDisabled bool
	var title string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(selectRowJS("Sample Workshop Item"), nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-action="batch-update"]').disabled`, &externalOnlyDisabled),
		chromedp.Evaluate(selectRowJS("Managed Mod"), nil),
		chromedp.Click(`[data-action="batch-update"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="updates"] .modal__title`, &title),
	)

	assert.True(t, externalOnlyDisabled,
		"a selection of external rows alone offers no update to apply")
	assert.Equal(t, "Update 1 mod", title,
		"the mixed selection plans only the row lmm can actually update")
	assert.Empty(t, f.BrowserErrors())
}

// --- #368: the detect list's own Workshop surfaces ---

// e2eWorkshopDetectFixture is a fake Steam root holding ONE uncurated app
// with a populated appworkshop manifest - the shape #368 is about: nothing
// in lmm's known-games list covers it, and the items Steam already
// downloaded are the only thing saying it is moddable.
type e2eWorkshopDetectFixture struct {
	AppID string
	Name  string
	Slug  string
}

func writeE2EWorkshopDetectFixture(t *testing.T, configDir string) e2eWorkshopDetectFixture {
	t.Helper()
	const (
		appID      = "1133870"
		name       = "E2E Workshop Game"
		installDir = "E2EWorkshopGame"
		slug       = "e2e-workshop-game"
	)
	steamRoot := t.TempDir()
	writeSteamAppManifest(t, steamRoot, appID, installDir, name)
	t.Setenv("STEAM_ROOT", steamRoot)

	workshop := filepath.Join(steamRoot, "steamapps", "workshop")
	require.NoError(t, os.MkdirAll(workshop, 0o755))
	acf := "\"AppWorkshop\"\n{\n\t\"appid\"\t\t\"" + appID + "\"\n\t\"WorkshopItemsInstalled\"\n\t{\n" +
		"\t\t\"" + e2eWorkshopFileID + "\"\n\t\t{\n\t\t\t\"manifest\"\t\t\"" + e2eWorkshopManifest + "\"\n\t\t}\n" +
		"\t\t\"3512001122\"\n\t\t{\n\t\t\t\"manifest\"\t\t\"1122334455667788990\"\n\t\t}\n\t}\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(workshop, "appworkshop_"+appID+".acf"), []byte(acf), 0o644))

	// An empty known-games override, so the embedded default list cannot
	// claim this app id and turn the row curated.
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "steam-games.yaml"), []byte("{}\n"), 0o644))
	return e2eWorkshopDetectFixture{AppID: appID, Name: name, Slug: slug}
}

// TestE2E_FirstRunDetect_WorkshopBearingUncuratedGameIsListedAndAddable is
// #368's web half, executed in a browser: the first-run detect list shows a
// game whose only claim to being moddable is its Steam Workshop items - it
// used to be filtered out entirely - says how many there are, and adds it
// through the existing "Add with details…" prefill path with no source to
// pick, because detection already mapped one.
func TestE2E_FirstRunDetect_WorkshopBearingUncuratedGameIsListedAndAddable(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	fixture := writeE2EWorkshopDetectFixture(t, f.Svc.ConfigDir())
	// AddGame validates every id in the prefilled map against the registry.
	f.Svc.RegisterSource(newFakeSource(e2eWorkshopSourceID))

	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="first-run-setup"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.setup-detect__row`, chromedp.ByQuery),
	)

	var rowCount int
	var rowText string
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelectorAll('.setup-detect__row').length`, &rowCount),
		textContent(`.setup-detect__row`, &rowText),
	)
	require.Equal(t, 1, rowCount)
	assert.Contains(t, rowText, fixture.Name)
	assert.Contains(t, rowText, "Steam Workshop: 2 items",
		"the count is what tells the user which row Workshop tracking is for")

	f.runInBrowser(t,
		chromedp.Click(`[data-action="add-with-details"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-add-prefilled-sources"]`, chromedp.ByQuery),
	)

	var note string
	f.runInBrowser(t, textContent(`[data-testid="setup-add-prefilled-sources"]`, &note))
	assert.Contains(t, note, e2eWorkshopSourceID+": "+fixture.AppID)

	var submitDisabled bool
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector('[data-action="add-game"]').disabled`, &submitDisabled))
	require.False(t, submitDisabled,
		"detection already mapped a source, so there is nothing left to pick")

	f.runInBrowser(t,
		chromedp.Click(`[data-action="add-game"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-hydrated="true"].mission-control`, chromedp.ByQuery),
	)

	got, err := f.Svc.GetGame(fixture.Slug)
	require.NoError(t, err)
	assert.Equal(t, fixture.Name, got.Name)
	assert.Equal(t, map[string]string{e2eWorkshopSourceID: fixture.AppID}, got.SourceIDs)
	assert.Empty(t, f.BrowserErrors())
}

// --- #348: the first-run Setup page's Workshop adopt card ---

// TestE2E_SetupAdopt_WorkshopCardTracksSubscribedItems is #348 (design Q3):
// the Setup page's Adopt section - where a user lands straight after
// first-run detect, asking "what do I already have?" - offers a second card
// for a game mapped to the steamworkshop source, and confirming it records
// the subscribed items as EXTERNAL mods. It is the same plan kind, renderer
// and confirm modal the library's Add mods ▾ menu opens: one flow, two entry
// points.
func TestE2E_SetupAdopt_WorkshopCardTracksSubscribedItems(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	// A second, untracked item: the fixture's first one is already tracked,
	// so without this the plan would have nothing to do and the assertion
	// below could not tell "adopted" from "was already there".
	const untrackedFileID = "3512001122"
	src, err := f.Svc.GetSource(e2eWorkshopSourceID)
	require.NoError(t, err)
	ws, ok := src.(*e2eWorkshopSource)
	require.True(t, ok)
	steamDir := t.TempDir()
	ws.scan.Items = append(ws.scan.Items, domain.WorkshopItem{
		FileID: untrackedFileID, Path: steamDir,
		Manifest: "1122334455667788990", TimeUpdated: 1758000000,
	})

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("adopt")),
		chromedp.WaitVisible(`[data-testid="setup-workshop-adopt"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="plan-workshop-adopt"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="workshop_adopt"] .plan--workshop-adopt`, chromedp.ByQuery),
	)

	var preview string
	f.runInBrowser(t, textContent(`.plan--workshop-adopt`, &preview))
	assert.Contains(t, preview, "To track (1)")
	assert.Contains(t, preview, "never moves, copies or deletes their files")

	f.runInBrowser(t,
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-workshop-adopt"] .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	installed, err := f.Svc.GetInstalledMods(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)
	var adopted *domain.InstalledMod
	for i, im := range installed {
		if im.ID == untrackedFileID {
			adopted = &installed[i]
		}
	}
	require.NotNil(t, adopted, "the subscribed item must now be a tracked installed mod")
	assert.True(t, adopted.External, "an adopted Workshop item is EXTERNAL - lmm tracks it, Steam owns it")
	assert.Equal(t, steamDir, adopted.ExternalPath)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SetupAdopt_WorkshopCardIsSeparatedFromTheOneAboveIt pins #368
// review Minor 6: SetupAdopt is the first place in the SPA to return TWO
// sibling .setup-section elements, and their container has no rule of its
// own, so the "Steam Workshop" heading sat flush against the previous card's
// action row (.plan__heading has no top margin). Only a browser can see
// that, which is why the visibility assertions above could not.
func TestE2E_SetupAdopt_WorkshopCardIsSeparatedFromTheOneAboveIt(t *testing.T) {
	f := newE2EWorkshopFixture(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("adopt")),
		chromedp.WaitVisible(`[data-testid="setup-workshop-adopt"]`, chromedp.ByQuery),
	)

	var gap float64
	f.runInBrowser(t, chromedp.Evaluate(`(() => {
		const first = document.querySelector('[data-testid="setup-adopt"]');
		const second = document.querySelector('[data-testid="setup-workshop-adopt"]');
		return second.getBoundingClientRect().top - first.getBoundingClientRect().bottom;
	})()`, &gap))

	assert.GreaterOrEqual(t, gap, 8.0,
		"the second Setup card must not butt against the first (got %.1fpx)", gap)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SetupAdopt_NoWorkshopCardWithoutTheMapping: the card is offered
// from one fact - the game maps the steamworkshop source - because the plan
// endpoint answers 400 for a game that does not, and a control that can
// only fail is worse than no control.
func TestE2E_SetupAdopt_NoWorkshopCardWithoutTheMapping(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("adopt")),
		chromedp.WaitVisible(`[data-testid="setup-adopt"]`, chromedp.ByQuery),
	)
	var cards int
	f.runInBrowser(t, chromedp.Evaluate(
		`document.querySelectorAll('[data-testid="setup-workshop-adopt"]').length`, &cards))
	assert.Equal(t, 0, cards)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Workshop_TheEnabledCheckboxIsDisabledOnAnExternalRow is issue
// 379. library.js gated the batch-update action and the row menu on
// isExternal, but not the library's own ENABLED checkbox - so on the
// primary screen every Steam Workshop row carried a checked, clickable box
// that started a job which could only fail:
//
//	POST /api/v1/mods/steamworkshop/<id>/disable -> {"job_id":"…"}
//	GET  /api/v1/jobs/<id>                       -> "state":"failed",
//	  "cannot disable Workshop item …: unsubscribe it in Steam, …"
//
// The core refusal is right; offering the control at all is not, and it
// contradicts `lmm import --workshop`'s own preamble, which says lmm
// cannot deploy, enable or update these. The pattern this follows is the
// full mod page's rollback button: disabled, with the real remedy as its
// title.
func TestE2E_Workshop_TheEnabledCheckboxIsDisabledOnAnExternalRow(t *testing.T) {
	f := newE2EWorkshopFixture(t)
	seedWorkshopManagedMod(t, f)

	const boxJS = `(() => {
		const row = Array.from(document.querySelectorAll(".mod-row"))
			.find((r) => r.textContent.includes(%q));
		if (!row) return null;
		const box = row.querySelector("td.col--enabled input[type=checkbox]");
		if (!box) return { present: false };
		return {
			present: true,
			disabled: box.disabled,
			title: box.title,
			label: box.getAttribute("aria-label"),
		};
	})()`

	type checkbox struct {
		Present  bool   `json:"present"`
		Disabled bool   `json:"disabled"`
		Title    string `json:"title"`
		Label    string `json:"label"`
	}

	var external, managed checkbox
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length >= 2`),
		chromedp.Evaluate(fmt.Sprintf(boxJS, "Sample Workshop Item"), &external),
		chromedp.Evaluate(fmt.Sprintf(boxJS, "Managed Mod"), &managed),
	)

	require.True(t, external.Present, "the external row still renders its enabled cell")
	assert.True(t, external.Disabled,
		"a Steam Workshop row's enabled checkbox must not start a job lmm can only refuse")
	assert.Contains(t, external.Title, "Steam",
		"and must say who does own the item's state")
	assert.NotContains(t, external.Label, "Disable",
		"the accessible name must not offer an action this control cannot perform")

	require.True(t, managed.Present)
	assert.False(t, managed.Disabled,
		"a mod lmm manages itself is untouched - the gate is on external rows only")

	// The batch bar reached the identical doomed job in two clicks: select
	// the Steam row, press Enable. Selecting ONLY external rows must leave
	// both buttons refused; a selection that still contains a managed row
	// keeps them live and drops the external one.
	var externalOnly, mixed struct {
		Enable  bool `json:"enable"`
		Disable bool `json:"disable"`
	}
	const selectRowJS = `(() => {
		const row = Array.from(document.querySelectorAll(".mod-row"))
			.find((r) => r.textContent.includes(%q));
		row.querySelector("td.col--select input[type=checkbox]").click();
		return true;
	})()`
	const batchStateJS = `({
		enable: document.querySelector('[data-action="batch-enable"]').disabled,
		disable: document.querySelector('[data-action="batch-disable"]').disabled,
	})`

	f.runInBrowser(t,
		chromedp.Evaluate(fmt.Sprintf(selectRowJS, "Sample Workshop Item"), nil),
		pollUntil(`document.querySelector('[data-action="batch-enable"]') !== null`),
		settleEffects(),
		chromedp.Evaluate(batchStateJS, &externalOnly),
		chromedp.Evaluate(fmt.Sprintf(selectRowJS, "Managed Mod"), nil),
		settleEffects(),
		chromedp.Evaluate(batchStateJS, &mixed),
	)

	assert.True(t, externalOnly.Enable, "a Steam-only selection cannot be enabled")
	assert.True(t, externalOnly.Disable, "nor disabled")
	assert.False(t, mixed.Enable, "a selection with a managed row still acts on that row")
	assert.False(t, mixed.Disable)

	assert.Empty(t, f.BrowserErrors())
}

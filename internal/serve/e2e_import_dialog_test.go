package serve_test

// #652: the collection import dialog draws every source the same way. The
// "Import collection…" modal (#650) and the profiles modal's import section
// share their forms; in both, each source is a section - its heading, its
// own full-width fields (a profile name of its own among them), its button,
// then its hint - and every text field and button in it shares one control
// height, type size and look, in either theme. Only the sources the game
// maps are offered.

import (
	"math"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// importDialogJS measures the import section inside the modal root names.
const importDialogJS = `((root) => {
	const scope = document.querySelector(root + " .profiles-import");
	if (!scope) return null;
	const visible = (el) => {
		const r = el.getBoundingClientRect();
		const s = getComputedStyle(el);
		return r.width > 0 && r.height > 0 && s.visibility !== "hidden" && s.display !== "none";
	};
	const name = (el) => el.tagName.toLowerCase() + (el.dataset.testid ? '[data-testid="' + el.dataset.testid + '"]' : "") +
		(el.type ? '[type="' + el.type + '"]' : "") + " " + (el.textContent || el.placeholder || "").trim().slice(0, 30);
	const fields = [...scope.querySelectorAll('input:not([type="checkbox"]):not([type="radio"]):not([type="file"]):not([type="hidden"])')]
		.filter(visible)
		.map((el) => {
			const s = getComputedStyle(el);
			const section = el.closest(".profiles-import__section");
			return {
				what: name(el),
				height: el.getBoundingClientRect().height,
				width: el.getBoundingClientRect().width,
				sectionWidth: section ? section.clientWidth : -1,
				fontSize: s.fontSize,
				background: s.backgroundColor,
				border: s.borderTopWidth + " " + s.borderTopStyle + " " + s.borderTopColor,
			};
		});
	const buttons = [...scope.querySelectorAll("button, label.button")]
		.filter(visible)
		.map((el) => ({ what: name(el), height: el.getBoundingClientRect().height, isButton: el.tagName === "BUTTON" }));
	const sections = [...scope.querySelectorAll(".profiles-import__section")].map((s) => {
		const order = [];
		for (const c of s.children) {
			if (!visible(c)) continue;
			let kind = "other:" + c.tagName.toLowerCase() + "." + c.className;
			if (/^H[1-6]$/.test(c.tagName)) kind = "heading";
			else if (c.tagName === "INPUT") kind = "field";
			else if (c.tagName === "BUTTON") kind = "button";
			else if (c.classList.contains("empty-state__hint")) kind = "hint";
			if (order[order.length - 1] !== kind) order.push(kind);
		}
		return {
			source: s.dataset.source || "",
			order,
			names: [...s.querySelectorAll("input")].filter((i) => i.placeholder === "Profile name (optional)").length,
		};
	});
	const mapHint = scope.querySelector('[data-testid="import-map-sources"]');
	return {
		fields,
		buttons,
		sections,
		mapHint: mapHint ? mapHint.textContent.trim() : "",
		mapHref: mapHint?.querySelector("a")?.getAttribute("href") ?? "",
	};
})`

type importDialogField struct {
	What         string  `json:"what"`
	Height       float64 `json:"height"`
	Width        float64 `json:"width"`
	SectionWidth float64 `json:"sectionWidth"`
	FontSize     string  `json:"fontSize"`
	Background   string  `json:"background"`
	Border       string  `json:"border"`
}

type importDialogButton struct {
	What     string  `json:"what"`
	Height   float64 `json:"height"`
	IsButton bool    `json:"isButton"`
}

type importDialogSection struct {
	Source string   `json:"source"`
	Order  []string `json:"order"`
	Names  int      `json:"names"`
}

type importDialogReport struct {
	Fields   []importDialogField   `json:"fields"`
	Buttons  []importDialogButton  `json:"buttons"`
	Sections []importDialogSection `json:"sections"`
	MapHint  string                `json:"mapHint"`
	MapHref  string                `json:"mapHref"`
}

// measureImportDialog reads importDialogJS for the modal root.
func measureImportDialog(root string, out *importDialogReport) chromedp.Action {
	return chromedp.Evaluate(importDialogJS+`(`+jsString(root)+`)`, out)
}

// newE2EImportDialogFixture is the NexusMods collection fixture with the
// Steam Workshop mapped as well, so every import source is offered.
func newE2EImportDialogFixture(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2ENexusCollectionFixture(t)
	f.Game.SourceIDs["steamworkshop"] = "1133870"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	return f
}

// openImportCollectionModal opens "Import collection…" from the profile
// picker and waits for its sections.
func openImportCollectionModal() chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		clickWhenSettled(`.profile-picker__menu [data-action="import-collection"]`),
		chromedp.WaitVisible(`.modal[data-kind="import-collection"] [data-testid="import-url"]`, chromedp.ByQuery),
		settleEffects(),
	}
}

// assertImportDialogLooksAlike holds one door's import section to #652's
// layout: every section the same shape, every field and button one size.
func assertImportDialogLooksAlike(t *testing.T, where string, got importDialogReport) {
	t.Helper()
	require.NotEmpty(t, got.Sections, "%s: no import sections", where)
	for _, s := range got.Sections {
		assert.Equal(t, []string{"heading", "field", "button", "hint"}, s.Order,
			"%s: the %q section is its heading, its own fields, its button, then its hint", where, s.Source)
		assert.Equal(t, 1, s.Names, "%s: the %q section has a profile name field of its own", where, s.Source)
	}

	require.NotEmpty(t, got.Fields, "%s: no text fields measured", where)
	first := got.Fields[0]
	for _, f := range got.Fields {
		assert.LessOrEqual(t, math.Abs(f.Height-first.Height), 0.5,
			"%s: %s is %.2fpx tall, %s is %.2fpx - every field shares the control height", where, f.What, f.Height, first.What, first.Height)
		assert.Equal(t, first.FontSize, f.FontSize, "%s: %s's type size", where, f.What)
		assert.Equal(t, first.Background, f.Background, "%s: %s's fill (a browser-default box differs)", where, f.What)
		assert.Equal(t, first.Border, f.Border, "%s: %s's border", where, f.What)
		assert.LessOrEqual(t, math.Abs(f.Width-f.SectionWidth), 1.0,
			"%s: %s is %.0fpx wide in a %.0fpx section - fields are full width", where, f.What, f.Width, f.SectionWidth)
	}

	require.NotEmpty(t, got.Buttons, "%s: no buttons measured", where)
	for _, b := range got.Buttons {
		assert.True(t, b.IsButton, "%s: %s is drawn as a button but is not a <button>", where, b.What)
		assert.LessOrEqual(t, math.Abs(b.Height-first.Height), 0.5,
			"%s: %s is %.2fpx tall, the fields are %.2fpx - buttons share the control height", where, b.What, b.Height, first.Height)
	}
}

// TestE2E_ImportDialog_EverySectionLooksAlike opens both doors to the
// import - the "Import collection…" modal and the profiles modal - in each
// theme, for a game that maps every source, and holds every section to one
// layout and every field and button to one size.
func TestE2E_ImportDialog_EverySectionLooksAlike(t *testing.T) {
	f := newE2EImportDialogFixture(t)

	for _, theme := range []string{"light", "dark"} {
		t.Run(theme, func(t *testing.T) {
			setTheme := chromedp.Evaluate(`document.documentElement.setAttribute("data-theme", "`+theme+`")`, nil)

			var collection importDialogReport
			f.runInBrowser(t,
				chromedp.Navigate(f.HomePath()),
				faceInEffect(e2eDefaultFace),
				setTheme,
				openImportCollectionModal(),
				measureImportDialog(`.modal[data-kind="import-collection"]`, &collection),
				chromedp.KeyEvent("\x1b"),
				waitGone(`.modal`),
			)
			assertImportDialogLooksAlike(t, theme+" import collection modal", collection)
			var sources []string
			for _, s := range collection.Sections {
				sources = append(sources, s.Source)
			}
			assert.Equal(t, []string{"file", "url", "steamworkshop", "nexusmods"}, sources,
				"a game that maps both collection sources offers all four")
			assert.Empty(t, collection.MapHint)

			var profiles importDialogReport
			f.runInBrowser(t,
				openProfilesModal(),
				chromedp.WaitVisible(`.profiles-row`, chromedp.ByQuery),
				settleEffects(),
				measureImportDialog(`.modal[data-kind="profiles"]`, &profiles),
				chromedp.KeyEvent("\x1b"),
				waitGone(`.modal`),
			)
			assertImportDialogLooksAlike(t, theme+" profiles modal", profiles)
			assert.Len(t, profiles.Sections, 4)
		})
	}
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// importSources opens the "Import collection…" modal and reads which
// sections it offers.
func importSources(t *testing.T, f e2eFixture) importDialogReport {
	t.Helper()
	var got importDialogReport
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openImportCollectionModal(),
		measureImportDialog(`.modal[data-kind="import-collection"]`, &got),
	)
	return got
}

// sectionSources lists a report's section sources in order.
func sectionSources(got importDialogReport) []string {
	var out []string
	for _, s := range got.Sections {
		out = append(out, s.Source)
	}
	return out
}

// TestE2E_ImportDialog_OffersOnlyTheMappedSources: a game that maps only
// NexusMods (Skyrim SE's mapping) is offered no Steam Workshop section; a
// Workshop-only game no NexusMods one; a game that maps neither is offered
// the file and the URL, and one line saying where to map the others.
func TestE2E_ImportDialog_OffersOnlyTheMappedSources(t *testing.T) {
	t.Run("nexusmods only", func(t *testing.T) {
		f := newE2ENexusCollectionFixture(t)
		got := importSources(t, f)
		assert.Equal(t, []string{"file", "url", "nexusmods"}, sectionSources(got))
		assert.Empty(t, got.MapHint)
		assert.Empty(t, f.BrowserErrors())
	})

	t.Run("steamworkshop only", func(t *testing.T) {
		f := newE2EFixture(t)
		f.Game.SourceIDs["steamworkshop"] = "1133870"
		require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
		got := importSources(t, f)
		assert.Equal(t, []string{"file", "url", "steamworkshop"}, sectionSources(got))
		assert.Empty(t, got.MapHint)
		assert.Empty(t, f.BrowserErrors())
	})

	t.Run("neither", func(t *testing.T) {
		f := newE2EFixture(t)
		got := importSources(t, f)
		assert.Equal(t, []string{"file", "url"}, sectionSources(got))
		assert.Contains(t, got.MapHint, "Steam Workshop")
		assert.Contains(t, got.MapHint, "NexusMods")
		assert.Contains(t, got.MapHint, "Setup")
		assert.True(t, strings.HasSuffix(got.MapHref, "/setup?section=games"), "the line links to Setup > Games: %q", got.MapHref)

		// The link closes the modal and opens Setup on the Games section.
		f.runInBrowser(t,
			clickWhenSettled(`[data-testid="import-map-sources"] a`),
			waitGone(`.modal`),
			chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
		)
		assert.Empty(t, f.BrowserErrors())
	})
}

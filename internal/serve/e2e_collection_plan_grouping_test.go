package serve_test

// #654: a collection import plan grouped for a collection of hundreds - the
// summary line first, then the optional mods (the only real choices), then
// the items that need the user, then every item behind a closed disclosure.
// A toggle re-plans and the summary follows it; the import saves what the
// plan last showed; nothing overruns a phone-width modal.

import (
	"context"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eGroupingSource is the NexusMods double over an account the source
// serves downloads to, so the plan's items are installs, not downloads by
// hand.
type e2eGroupingSource struct{ e2eNexusSource }

func (e2eGroupingSource) AccountTier(context.Context) (source.Tier, error) {
	return source.TierPremium, nil
}

// e2eGroupingRecipe has a normal mod, one whose installer asks, two
// optional mods (one with a long unbroken name and the curator's words for
// it), an optional mod lmm cannot import, and an item fetched by hand.
const e2eGroupingRecipe = `{"Name": "Grouped", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact"},
	{"Name": "Asks", "Version": "1.0", "SourceType": "nexus", "ModID": "11", "FileID": "110", "UpdatePolicy": "exact", "InstallerType": "fomod"},
	{"Name": "Optional_Texture_Pack_For_Ultra_High_Resolution_Screens_v2.0.0-final-release-candidate", "Version": "2.0", "SourceType": "nexus", "ModID": "12", "FileID": "120", "UpdatePolicy": "exact", "Optional": true,
	 "Instructions": "Recommended for 4K screens: see https://example.invalid/a/very/long/path/that/never/breaks/on/its/own/because/it/has/no/spaces/at/all"},
	{"Name": "Opt B", "Version": "3.0", "SourceType": "nexus", "ModID": "13", "FileID": "130", "UpdatePolicy": "exact", "Optional": true},
	{"Name": "Opt C", "Version": "1.0", "SourceType": "nexus", "ModID": "14", "FileID": "140", "UpdatePolicy": "exact", "Optional": true, "FileOverrides": ["c.txt"]},
	{"Name": "Hand", "SourceType": "manual", "URL": "https://example.invalid/hand", "Instructions": "Unzip it into the game folder.", "UpdatePolicy": "exact"}]}`

func newE2EGroupingFixture(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)
	f.Svc.RegisterSource(e2eGroupingSource{e2eNexusSource{newFakeSource("nexusmods")}})
	f.Game.SourceIDs["nexusmods"] = "skyrimspecialedition"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	seedE2ENexusRecipe(t, f, "grouped", e2eGroupingRecipe)
	return f
}

// groupingSectionOrderJS is "ok" when the plan's four groups are there, in
// order.
const groupingSectionOrderJS = `(() => {
	const ids = ["collection-summary", "collection-optional", "collection-attention", "collection-everything"];
	const els = ids.map((id) => document.querySelector('.modal [data-testid="' + id + '"]'));
	const missing = ids.filter((id, i) => !els[i]);
	if (missing.length > 0) return "missing " + missing.join(", ");
	for (let i = 1; i < els.length; i++) {
		if (!(els[i - 1].compareDocumentPosition(els[i]) & Node.DOCUMENT_POSITION_FOLLOWING)) return ids[i] + " is before " + ids[i - 1];
	}
	return "ok";
})()`

// groupingRowsJS counts each group's rows and lists the attention reasons.
const groupingRowsJS = `(() => {
	const q = (sel) => [...document.querySelectorAll(sel)];
	return {
		optional: q('[data-testid="collection-optional"] li[data-item]').map((li) => li.dataset.item),
		attention: q('[data-testid="collection-attention"] li[data-item]').map((li) =>
			li.dataset.item + "=" + [...li.querySelectorAll('[data-testid="attention-reason"]')].map((p) => p.dataset.reason).join(",")),
		everything: q('[data-testid="collection-everything"] li[data-item]').length,
		open: document.querySelector('[data-testid="collection-everything"]').open,
	};
})()`

type groupingRows struct {
	Optional   []string `json:"optional"`
	Attention  []string `json:"attention"`
	Everything int      `json:"everything"`
	Open       bool     `json:"open"`
}

// groupingCheckAlignJS reports each optional row whose checkbox is not
// beside the first line of its label's text.
const groupingCheckAlignJS = `(() => {
	const bad = [];
	for (const label of document.querySelectorAll('[data-testid="collection-optional"] .form-check')) {
		const box = label.querySelector('input[type="checkbox"]').getBoundingClientRect();
		const line = label.querySelector(".plan__choice-text").getClientRects()[0];
		const mid = (box.top + box.bottom) / 2;
		if (mid < line.top || mid > line.bottom || box.right > line.left + 0.5) {
			bad.push(label.textContent.trim().slice(0, 30));
		}
	}
	return bad;
})()`

// groupingOverflowJS lists the plan rows and notes wider than their box.
const groupingOverflowJS = `(() => [...document.querySelectorAll('.modal .plan__mod, .modal .plan__note, .modal .plan__summary')]
	.filter((el) => el.getBoundingClientRect().width > 0 && el.scrollWidth > el.clientWidth + 1)
	.map((el) => el.textContent.trim().slice(0, 40)))()`

const groupingOptionalCount = `document.querySelector('[data-count="optional"]')?.textContent.trim()`

func TestE2E_CollectionPlan_GroupsSummarisesAndImportsTheSelection(t *testing.T) {
	f := newE2EGroupingFixture(t)

	var order, summary, attentionC string
	var before groupingRows
	var misaligned []string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.HomePath()),
		openNexusImport("grouped"),
		chromedp.Evaluate(groupingSectionOrderJS, &order),
		textContent(`[data-testid="collection-summary"]`, &summary),
		chromedp.Evaluate(groupingRowsJS, &before),
		chromedp.Evaluate(groupingCheckAlignJS, &misaligned),
		textContent(`[data-testid="collection-attention"] [data-item="14:140"]`, &attentionC),
	)
	assert.Equal(t, "ok", order)
	assert.Contains(t, summary, "2 will be installed · 3 optional (0 selected) · 1 will ask installer questions · 1 external or prerequisite · 1 cannot be imported")
	assert.Equal(t, []string{"12:120", "13:130", "14:140"}, before.Optional, "one row per optional mod, and the counts agree")
	assert.Equal(t, []string{"11:110=installer_questions", "14:140=unsupported", "item:5=external"}, before.Attention,
		"the optional mod lmm cannot import is listed where its count points")
	assert.Equal(t, 6, before.Everything, "every item is in the full list")
	assert.False(t, before.Open, "the full list is closed by default")
	assert.Empty(t, misaligned, "each checkbox sits beside its label's first line")
	assert.Contains(t, attentionC, "so this optional mod is left out")

	// A count goes to its section: the install count opens the full list.
	var opened bool
	f.runInBrowser(t,
		clickWhenSettled(`[data-count="install"]`),
		chromedp.Evaluate(`document.querySelector('[data-testid="collection-everything"]').open`, &opened),
	)
	assert.True(t, opened)

	// Toggling one re-plans, and the summary follows: one more to install.
	f.runInBrowser(t,
		clickWhenSettled(`[data-testid="collection-optional"] [data-item="12:120"] input[type="checkbox"]`),
		pollUntil(groupingOptionalCount+` === "3 optional (1 selected)"`),
		textContent(`[data-testid="collection-summary"]`, &summary),
	)
	assert.Contains(t, summary, "3 will be installed")

	// Select all, then none, then the one again.
	f.runInBrowser(t,
		clickWhenSettled(`[data-action="optional-select-all"]`),
		pollUntil(groupingOptionalCount+` === "3 optional (2 selected)"`),
		clickWhenSettled(`[data-action="optional-select-none"]`),
		pollUntil(groupingOptionalCount+` === "3 optional (0 selected)"`),
		clickWhenSettled(`[data-testid="collection-optional"] [data-item="12:120"] input[type="checkbox"]`),
		pollUntil(groupingOptionalCount+` === "3 optional (1 selected)"`),
	)

	// Narrow: nothing overruns a phone-width modal, the full list open.
	var fit dialogFitReport
	var overflow []string
	f.runInBrowser(t,
		chromedp.EmulateViewport(375, 800),
		chromedp.Evaluate(`document.querySelector('[data-testid="collection-everything"]').open = true`, nil),
		settleEffects(),
		chromedp.Evaluate(dialogFitJS+`(".modal[data-kind=\"profile_import\"]")`, &fit),
		chromedp.Evaluate(groupingOverflowJS, &overflow),
		chromedp.Evaluate(groupingCheckAlignJS, &misaligned),
		chromedp.EmulateViewport(1280, 900),
	)
	assert.Empty(t, fit.Overruns, "controls overrun the dialog at 375px")
	assert.Empty(t, overflow, "text runs past its row at 375px")
	assert.Empty(t, misaligned, "each checkbox still sits beside its label at 375px")

	// The import saves the selection the plan last showed.
	f.runInBrowser(t, clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`))
	require.Eventually(t, func() bool {
		_, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Grouped")
		return err == nil
	}, 15*time.Second, 50*time.Millisecond)
	p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Grouped")
	require.NoError(t, err)
	assert.NotNil(t, p.FindRef("nexusmods", "12"), "the opted-in optional mod is imported")
	assert.Nil(t, p.FindRef("nexusmods", "13"), "the other is left out")
	assert.NotNil(t, p.FindRef("nexusmods", "11"))
	assert.Empty(t, f.BrowserErrors())
}

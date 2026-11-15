package serve_test

// #653: every dialog's controls as a browser computes them. The CSS
// ratchets (control_system_test.go, input_types_test.go) prove no rule
// reshapes a field or a button by hand and that every form uses the form
// layout; only a browser can say the result lines up. This holds every
// modal, slide-over and Setup form the SPA can open - one row of
// dialogScenarios each, saying how to open it - to the control system's
// promise, in both themes and every layout face:
//
//   - every one-line field (a select, a text-entry input) is one height;
//   - every default-size button is that same height, and every small one
//     shares the small height;
//   - in a .form-row, a field and a button on one line share a top and a
//     bottom;
//   - the parts of a form stand apart: a form-layout item (a section, its
//     heading, a field, a check, a row, actions, a hint) sits at least the
//     form rhythm's gap below whatever comes before it - a wrapped row's
//     gap inside a .form-row or .form-actions; --form-gap in a
//     .form-section, and next to a section or a heading wherever it is;
//     --form-field-gap otherwise - so a container that is not a
//     .form-section cannot collapse its parts together.
//
// TestE2E_EveryDialogFitsANarrowWindow opens each one again at 375px and
// holds every control inside its dialog, and a modal or slide-over panel
// inside the window with a side gutter.
//
// A new dialog joins by gaining a row. TestE2E_DialogScenariosCoverEveryKind
// fails when a plan kind or a modal kind has none.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// dialogControlsJS measures the controls inside the dialog root names.
const dialogControlsJS = `((root) => {
	const scope = document.querySelector(root);
	if (!scope) return null;
	const visible = (el) => {
		const r = el.getBoundingClientRect();
		const s = getComputedStyle(el);
		return r.width > 0 && r.height > 0 && s.visibility !== "hidden" && s.display !== "none";
	};
	const name = (el) => el.tagName.toLowerCase() +
		(el.name ? '[name="' + el.name + '"]' : "") +
		(el.dataset.action ? '[data-action="' + el.dataset.action + '"]' : "") +
		(el.dataset.testid ? '[data-testid="' + el.dataset.testid + '"]' : "") +
		" " + (el.textContent || el.placeholder || el.getAttribute("aria-label") || "").trim().slice(0, 30);
	const FIELD = 'select, input:not([type="checkbox"]):not([type="radio"]):not([type="file"]):not([type="hidden"])';
	const BUTTON = "button.button, a.button";
	const box = (el) => {
		const r = el.getBoundingClientRect();
		return { what: name(el), height: r.height, top: r.top, bottom: r.bottom };
	};
	const fields = [...scope.querySelectorAll(FIELD)].filter(visible).map(box);
	const buttons = [...scope.querySelectorAll(BUTTON)]
		.filter(visible)
		.filter((el) => !el.matches(".button--link, .button--tile, .button--prose"))
		.map((el) => ({ ...box(el), small: el.classList.contains("button--small") }));
	const rows = [...scope.querySelectorAll(".form-row")].filter(visible).map((row) =>
		[...row.querySelectorAll(FIELD + ", " + BUTTON)]
			.filter(visible)
			.filter((el) => el.closest(".form-row") === row)
			.map(box));
	// The form rhythm (see the file comment): each token resolved to px.
	const px = (v) => {
		const d = document.createElement("div");
		d.style.height = v;
		document.body.appendChild(d);
		const h = d.getBoundingClientRect().height;
		d.remove();
		return h;
	};
	const formGap = px("var(--form-gap)"), fieldGap = px("var(--form-field-gap)"), rowGap = px("var(--space-2)");
	const LAYOUT = ".form-section, .form-section__heading, .form-field, .form-check, .form-row, .form-actions, .form-hint, .form-error";
	const prevVisible = (el) => {
		let p = el.previousElementSibling;
		while (p && !visible(p)) p = p.previousElementSibling;
		return p;
	};
	const tight = [];
	for (const el of [...scope.querySelectorAll(LAYOUT)].filter(visible)) {
		const prev = prevVisible(el);
		if (!prev) continue;
		const a = prev.getBoundingClientRect(), b = el.getBoundingClientRect();
		if (b.top < a.bottom - 0.5) continue; // side by side, not stacked
		const parent = el.parentElement;
		const sectional = (n) => n.matches(".form-section, .form-section__heading, h1, h2, h3, h4, h5, h6, legend");
		const min = parent.matches(".form-field") ? fieldGap
			: parent.matches(".form-row, .form-actions") ? rowGap
			: sectional(el) || sectional(prev) || parent.matches(".form-section") ? formGap
			: fieldGap;
		const gap = b.top - a.bottom;
		if (gap < min - 0.5) tight.push({ what: name(el), prev: name(prev), gap, min });
	}
	return { fields, buttons, rows, tight };
})`

// dialogFitJS measures, at the current width, the controls that overrun
// the dialog root names, and where the modal or slide-over panel holding
// it sits in the window.
const dialogFitJS = `((root) => {
	const scope = document.querySelector(root);
	if (!scope) return null;
	const visible = (el) => {
		const r = el.getBoundingClientRect();
		const s = getComputedStyle(el);
		return r.width > 0 && r.height > 0 && s.visibility !== "hidden" && s.display !== "none";
	};
	const name = (el) => el.tagName.toLowerCase() + (el.name ? '[name="' + el.name + '"]' : "") +
		" " + (el.textContent || el.placeholder || el.getAttribute("aria-label") || "").trim().slice(0, 30);
	const box = scope.getBoundingClientRect();
	const overruns = [...scope.querySelectorAll('button, a.button, select, textarea, input:not([type="hidden"]):not([type="file"])')]
		.filter(visible)
		.filter((el) => {
			const r = el.getBoundingClientRect();
			return r.left < box.left - 0.5 || r.right > box.right + 0.5;
		})
		.map(name);
	const slide = scope.matches(".slide-over");
	const panel = slide ? scope.querySelector(".slide-over__panel") : scope.closest(".modal");
	const p = panel ? panel.getBoundingClientRect() : null;
	return {
		overruns,
		panel: p ? [p.left, p.right] : null,
		// A slide-over is anchored to the window's right edge by design.
		anchoredRight: slide,
		viewport: window.innerWidth,
	};
})`

type dialogControl struct {
	What   string  `json:"what"`
	Height float64 `json:"height"`
	Top    float64 `json:"top"`
	Bottom float64 `json:"bottom"`
	Small  bool    `json:"small"`
}

// dialogGap is two parts of a form closer together than the form rhythm.
type dialogGap struct {
	What string  `json:"what"`
	Prev string  `json:"prev"`
	Gap  float64 `json:"gap"`
	Min  float64 `json:"min"`
}

type dialogControlsReport struct {
	Fields  []dialogControl   `json:"fields"`
	Buttons []dialogControl   `json:"buttons"`
	Rows    [][]dialogControl `json:"rows"`
	Tight   []dialogGap       `json:"tight"`
}

type dialogFitReport struct {
	Overruns      []string  `json:"overruns"`
	Panel         []float64 `json:"panel"`
	AnchoredRight bool      `json:"anchoredRight"`
	Viewport      float64   `json:"viewport"`
}

// dialogFixture is a fixture a scenario opens its dialog in, with the
// files an opener uploads (an archive to import, a download to fill from),
// by name.
type dialogFixture struct {
	e2eFixture
	files map[string]string
}

// dialogScenario is one dialog and how to open it: the fixture it needs
// (scenarios naming the same fixture share one), the steps from any page,
// and the selector of the dialog's own root. wantField says the dialog has
// a text field or select to measure, so an opener that stopped short of
// the form fails rather than measuring nothing.
type dialogScenario struct {
	name      string
	fixture   string
	open      func(f dialogFixture) chromedp.Tasks
	root      string
	wantField bool
}

// dialogFixtures builds each fixture a scenario can name.
var dialogFixtures = map[string]func(t *testing.T) dialogFixture{
	"copyable": func(t *testing.T) dialogFixture {
		f := newE2EFixtureWithCopyableMods(t)
		dir := t.TempDir()
		files := map[string]string{
			// No version in the name, so the plan asks for one (#530's
			// version prompt: a field and its button in a row).
			"update": filepath.Join(dir, "copyme.zip"),
			"import": filepath.Join(dir, "Imported Mod-1.0.zip"),
		}
		require.NoError(t, os.WriteFile(files["update"], e2eZipWith("copyme.pak", "new"), 0o644))
		require.NoError(t, os.WriteFile(files["import"], e2eZipWith("Mods/imported.pak", "x"), 0o644))
		return dialogFixture{e2eFixture: f, files: files}
	},
	"attention": func(t *testing.T) dialogFixture {
		return dialogFixture{e2eFixture: newE2EFixtureWithAttention(t)}
	},
	"search": newE2ESearchDialogFixture,
	"nexus":  func(t *testing.T) dialogFixture { return dialogFixture{e2eFixture: newE2EImportDialogFixture(t)} },
	// #657: a NexusMods collection pinning a file NexusMods no longer
	// offers, so its plan shows the gone-pin choices.
	"gonepins": func(t *testing.T) dialogFixture { return dialogFixture{e2eFixture: newE2EGonePinFixture(t)} },
	"rollback": func(t *testing.T) dialogFixture {
		return dialogFixture{e2eFixture: newE2EFixtureWithRollbackReadyMod(t)}
	},
	"collection": func(t *testing.T) dialogFixture {
		f, _ := newE2ECollectionUpdateFixture(t)
		return dialogFixture{e2eFixture: f}
	},
	"unapplied": func(t *testing.T) dialogFixture {
		return dialogFixture{e2eFixture: newE2EFixtureWithAnUnappliedProfile(t).e2eFixture}
	},
	"workshop": func(t *testing.T) dialogFixture {
		f := newE2EWorkshopFixture(t)
		src, err := f.Svc.GetSource(e2eWorkshopSourceID)
		require.NoError(t, err)
		ws, ok := src.(*e2eWorkshopSource)
		require.True(t, ok)
		ws.scan.Items = append(ws.scan.Items, domain.WorkshopItem{
			FileID: "3512001122", Path: t.TempDir(), Manifest: "1122334455667788990", TimeUpdated: 1758000000,
		})
		return dialogFixture{e2eFixture: f}
	},
	"launch": func(t *testing.T) dialogFixture {
		f := newE2EFixtureWithDeployableMods(t)
		(&e2eLauncher{appID: "892970"}).install(f.Svc)
		return dialogFixture{e2eFixture: f}
	},
	"fill": func(t *testing.T) dialogFixture {
		f := newE2EFillFixture(t)
		f.seedFillProfile(t)
		return dialogFixture{e2eFixture: f.e2eFixture, files: map[string]string{"ten": f.ten}}
	},
}

// newE2ESearchDialogFixture is the searchable-mods fixture with two FOMOD
// mods: one in the catalog (installing it opens the wizard), and one
// installed with its installer answered (reconfiguring it opens the
// mod_fomod plan). An auth source with no key gives Setup's Authentication
// panel a login form.
func newE2ESearchDialogFixture(t *testing.T) dialogFixture {
	t.Helper()
	f := newE2EFixtureWithSearchableMods(t)
	f.Svc.RegisterSource(newE2EAuthSource("authy", "good-key"))
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "wizard", SourceID: "fake", Name: "Wizard Mod", Version: "1.0"},
		files:    []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "wizard.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}},
		archives: map[string][]byte{"w1": e2eFomodArchive(t)},
	})
	files := []domain.DownloadableFile{{ID: "b1", Name: "Main", FileName: "bugs.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}}
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "bugs", SourceID: "fake", Name: "Scrambled Bugs", Version: "1.0"},
		files:    files,
		archives: map[string][]byte{"b1": e2eFomodArchive(t)},
	})
	ctx := t.Context()
	plan, err := f.Svc.PlanInstall(ctx, f.Game, "default", "fake", "bugs", false)
	require.NoError(t, err)
	plan.Files = files
	_, err = f.Svc.ApplyInstall(ctx, f.Game, plan, core.InstallOptions{TargetFileIDs: []string{"b1"}, Fomod: map[string]domain.FomodChoices{
		domain.FomodFileKey("fake", "bugs", "b1"): {Steps: []domain.FomodStepChoice{{Name: "Size", Groups: []domain.FomodGroupChoice{{Name: "Texture", Choices: []domain.FomodOptionChoice{{Name: "2K", Idx: 0}}}}}}},
	}}, nil)
	require.NoError(t, err)
	return dialogFixture{e2eFixture: f.e2eFixture}
}

// home opens Mission Control and waits for it to hydrate.
func home(f dialogFixture) chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
	}
}

// fromRowMenu opens the library row menu of the mod named name and clicks
// the item action.
func fromRowMenu(f dialogFixture, name, action string) chromedp.Tasks {
	return chromedp.Tasks{
		home(f),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(fmt.Sprintf(`Array.from(document.querySelectorAll(".mod-row")).some((r) => r.textContent.includes(%q))`, name)),
		chromedp.Evaluate(openRowMenuJS(name), nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		clickWhenSettled(`.row-menu [data-action="` + action + `"]`),
	}
}

// inProfilesModal opens the profiles modal and runs then inside it.
func inProfilesModal(f dialogFixture, then ...chromedp.Action) chromedp.Tasks {
	return append(chromedp.Tasks{home(f), openProfilesModal()}, then...)
}

// searchAndInstall searches the omnibar for query and presses Install on
// the result row.
func searchAndInstall(f dialogFixture, query, modID string) chromedp.Tasks {
	row := searchResultRow("fake", modID)
	return chromedp.Tasks{
		home(f),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, query, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		clickWhenSettled(row + " .search-result__install"),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
	}
}

// planOpen waits for the plan modal of kind to render its plan, and opens
// its Advanced disclosure, where its option controls are, when it has one.
func planOpen(kind string) chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`.modal[data-kind="`+kind+`"] .plan`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const d = document.querySelector('.modal[data-kind="`+kind+`"] [data-testid="plan-advanced"]');
			if (d) d.open = true;
		})()`, nil),
	}
}

// clickByText clicks the first element matching sel whose text includes
// text.
func clickByText(sel, text string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`[...document.querySelectorAll(%q)].find((el) => el.textContent.includes(%q)).click()`, sel, text), nil)
}

// dialogScenarios is every dialog the SPA can open. Plan modals are the
// confirm-plan shell with the kind's renderer; the rest are modals of their
// own, the mod slide-over, and the Setup forms.
var dialogScenarios = []dialogScenario{
	// Modals of their own.
	{name: "profiles", fixture: "copyable", root: `.modal[data-kind="profiles"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks { return inProfilesModal(f) }},
	{name: "profiles: duplicating", fixture: "copyable", root: `.modal[data-kind="profiles"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return inProfilesModal(f,
				clickWhenSettled(`[data-action="duplicate-profile"][data-profile="alt"]`),
				chromedp.WaitVisible(`[data-testid="profile-duplicate-row"] input`, chromedp.ByQuery))
		}},
	{name: "profiles: renaming", fixture: "copyable", root: `.modal[data-kind="profiles"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return inProfilesModal(f,
				pollUntil(`[...document.querySelectorAll('.profiles-row:has([data-profile="spare"]) button')].some((b) => b.textContent.includes("Rename"))`),
				clickByText(`.profiles-row:has([data-profile="spare"]) button`, "Rename"),
				chromedp.WaitVisible(`.profiles-row form.form-row input`, chromedp.ByQuery))
		}},
	{name: "profiles: export with my files", fixture: "copyable", root: `.modal[data-kind="profiles"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return inProfilesModal(f,
				clickWhenSettled(`[data-action="export-bundle"][data-profile="default"]`),
				chromedp.WaitVisible(`[data-testid="profile-bundle-row"] [data-action="export-bundle-download"]`, chromedp.ByQuery))
		}},
	{name: "profiles: fill from downloads", fixture: "fill", root: `.modal[data-kind="profiles"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return inProfilesModal(f,
				chromedp.WaitVisible(`[data-action="fill-profile"][data-profile="Coll"]`, chromedp.ByQuery),
				clickWhenSettled(`[data-action="fill-profile"][data-profile="Coll"]`),
				chromedp.WaitVisible(`[data-testid="fill-drop"] [data-action="fill-folder"]`, chromedp.ByQuery))
		}},
	{name: "import-collection", fixture: "nexus", root: `.modal[data-kind="import-collection"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.HomePath()), openImportCollectionModal()}
		}},
	{name: "shortcuts", fixture: "copyable", root: `.modal[data-kind="shortcuts"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{home(f), clickWhenSettled(`[data-action="shortcuts"]`),
				chromedp.WaitVisible(`.modal[data-kind="shortcuts"]`, chromedp.ByQuery)}
		}},
	{name: "reorder", fixture: "copyable", root: `.modal[data-kind="reorder"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{home(f), clickWhenSettled(`.library__toolbar [data-action="reorder"]`),
				chromedp.WaitVisible(`.modal[data-kind="reorder"] .reorder-row`, chromedp.ByQuery)}
		}},
	{name: "uninstall-batch", fixture: "copyable", root: `.modal[data-kind="uninstall-batch"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{home(f),
				chromedp.WaitVisible(`.library__table [data-testid="select-all"]`, chromedp.ByQuery),
				clickWhenSettled(`.library__table [data-testid="select-all"]`),
				clickWhenSettled(`[data-action="batch-uninstall"]`),
				chromedp.WaitVisible(`.modal[data-kind="uninstall-batch"] .plan`, chromedp.ByQuery)}
		}},
	{name: "folder-picker", fixture: "copyable", root: `.modal[data-kind="folder-picker"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SetupPath("games")),
				openAddGameForm(),
				clickWhenSettled(`[data-testid="setup-add-game"] .form-field:has(input[name="add-install-path"]) [data-action="browse-folder"]`),
				chromedp.WaitVisible(`.modal[data-kind="folder-picker"] [data-testid="folder-picker-list"]`, chromedp.ByQuery)}
		}},
	{name: "fomod", fixture: "search", root: `.modal[data-kind="fomod"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{searchAndInstall(f, "Wizard", "wizard"),
				clickWhenSettled(`.modal [data-action="confirm"]`),
				chromedp.WaitVisible(`.modal[data-kind="fomod"] [data-testid="fomod-step"]`, chromedp.ByQuery)}
		}},
	{name: "launch-drift", fixture: "launch", root: `.modal[data-kind="launch-drift"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{home(f),
				pollUntil(`document.querySelector('[data-action="play"]')?.getAttribute("aria-disabled") === null`),
				clickWhenSettled(`[data-action="play"]`),
				chromedp.WaitVisible(`.modal[data-kind="launch-drift"]`, chromedp.ByQuery)}
		}},

	// Plan modals.
	{name: "deploy", fixture: "copyable", root: `.modal[data-kind="deploy"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{home(f), clickWhenSettled(`[data-action="deploy"]`), planOpen("deploy")}
		}},
	{name: "uninstall", fixture: "copyable", root: `.modal[data-kind="uninstall"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SlideOverPath("fake", "copyme")),
				chromedp.WaitVisible(`.slide-over__actions .button--danger`, chromedp.ByQuery),
				clickWhenSettled(`.slide-over__actions .button--danger`),
				planOpen("uninstall")}
		}},
	{name: "mod_relink", fixture: "copyable", root: `.modal[data-kind="mod_relink"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{fromRowMenu(f, "Copy Me", "relink"), planOpen("mod_relink")}
		}},
	{name: "mod_copy", fixture: "copyable", root: `.modal[data-kind="mod_copy"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{fromRowMenu(f, "Copy Me", "copy-to-profile"), planOpen("mod_copy")}
		}},
	{name: "update_from_archive", fixture: "copyable", root: `.modal[data-kind="update_from_archive"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{fromRowMenu(f, "Copy Me", "update-from-file"),
				chromedp.SetUploadFiles(e2eFromFileInput, []string{f.files["update"]}, chromedp.ByQuery),
				chromedp.WaitVisible(`.modal[data-kind="update_from_archive"] [data-testid="version-prompt"] input`, chromedp.ByQuery)}
		}},
	{name: "switch", fixture: "copyable", root: `.modal[data-kind="switch"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{home(f), clickWhenSettled(`.profile-picker__trigger`),
				chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
				settleEffects(),
				clickWhenSettled(`[data-action="switch"][data-profile="alt"]`),
				planOpen("switch")}
		}},
	{name: "purge", fixture: "copyable", root: `.modal[data-kind="purge"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return inProfilesModal(f,
				clickWhenSettled(`[data-action="purge-profile"][data-profile="default"]`),
				chromedp.WaitVisible(`.modal[data-kind="purge"] input[name="purge-confirm"]`, chromedp.ByQuery),
				planOpen("purge"))
		}},
	{name: "profile_sync", fixture: "copyable", root: `.modal[data-kind="profile_sync"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return inProfilesModal(f,
				clickWhenSettled(`[data-action="sync-profile"][data-profile="default"]`),
				planOpen("profile_sync"))
		}},
	{name: "profile_apply", fixture: "unapplied", root: `.modal[data-kind="profile_apply"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{home(f), chromedp.WaitVisible(`.card--profile`, chromedp.ByQuery),
				clickWhenSettled(`[data-action="apply-profile"]`),
				planOpen("profile_apply")}
		}},
	{name: "snapshot_restore", fixture: "copyable", root: `.modal[data-kind="snapshot_restore"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{home(f),
				clickWhenSettled(`[data-action="snapshot-now"]`),
				chromedp.WaitVisible(`[data-action="snapshot-restore"]`, chromedp.ByQuery),
				clickWhenSettled(`[data-action="snapshot-restore"]`),
				planOpen("snapshot_restore")}
		}},
	{name: "updates", fixture: "attention", root: `.modal[data-kind="updates"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{home(f),
				chromedp.WaitVisible(`.card--updates [data-action="update-all"]`, chromedp.ByQuery),
				pollUntil(`!document.querySelector('.card--updates [data-action="update-all"]').disabled`),
				clickWhenSettled(`.card--updates [data-action="update-all"]`),
				planOpen("updates")}
		}},
	{name: "verify_fix", fixture: "attention", root: `.modal[data-kind="verify_fix"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{fromRowMenu(f, "Better Boots", "row-repair"), planOpen("verify_fix")}
		}},
	{name: "install", fixture: "search", root: `.modal[data-kind="install"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{searchAndInstall(f, "Boots", e2eSearchInstallModID), planOpen("install")}
		}},
	{name: "mod_fomod", fixture: "search", root: `.modal[data-kind="mod_fomod"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.ModPagePath("fake", "bugs")),
				clickWhenSettled(`[data-action="reconfigure-fomod"]`),
				planOpen("mod_fomod")}
		}},
	{name: "rollback", fixture: "rollback", root: `.modal[data-kind="rollback"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.ModPagePath("fake", "a")),
				pollUntil(`[...document.querySelectorAll("button")].some((b) => b.textContent.includes("Roll back to the previous version") && !b.disabled)`),
				clickByText(`button`, "Roll back to the previous version"),
				planOpen("rollback")}
		}},
	{name: "adopt", fixture: "copyable", root: `.modal[data-kind="adopt"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SetupPath("adopt")),
				clickWhenSettled(`[data-action="plan-adopt"]`),
				planOpen("adopt")}
		}},
	{name: "workshop_adopt", fixture: "workshop", root: `.modal[data-kind="workshop_adopt"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SetupPath("adopt")),
				clickWhenSettled(`[data-action="plan-workshop-adopt"]`),
				planOpen("workshop_adopt")}
		}},
	{name: "import_archive", fixture: "copyable", root: `.modal[data-kind="import_archive"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SetupPath("archive")),
				chromedp.WaitVisible(`[data-testid="setup-import-archive"] .button`, chromedp.ByQuery),
				chromedp.SetUploadFiles(`[data-testid="setup-import-archive"] input[type="file"]`, []string{f.files["import"]}, chromedp.ByQuery),
				clickWhenSettled(`[data-action="import-archive"]`),
				planOpen("import_archive")}
		}},
	{name: "profile_import", fixture: "nexus", root: `.modal[data-kind="profile_import"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.HomePath()), openNexusImport("abcdef"), planOpen("profile_import")}
		}},
	{name: "profile_import: gone pins", fixture: "gonepins", root: `.modal[data-kind="profile_import"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.HomePath()), openNexusImport("abcdef"), planOpen("profile_import"),
				chromedp.WaitVisible(`[data-testid="gone-pins"]`, chromedp.ByQuery)}
		}},
	{name: "collection_update", fixture: "collection", root: `.modal[data-kind="collection_update"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return inProfilesModal(f,
				clickWhenSettled(`[data-action="update-collection"][data-profile="shared"]`),
				chromedp.WaitVisible(`[data-testid="update-summary"]`, chromedp.ByQuery),
				planOpen("collection_update"))
		}},
	{name: "profile_fill", fixture: "fill", root: `.modal[data-kind="profile_fill"]`,
		open: func(f dialogFixture) chromedp.Tasks {
			return inProfilesModal(f,
				chromedp.WaitVisible(`[data-action="fill-profile"][data-profile="Coll"]`, chromedp.ByQuery),
				clickWhenSettled(`[data-action="fill-profile"][data-profile="Coll"]`),
				chromedp.WaitReady(fillInput, chromedp.ByQuery),
				chromedp.SetUploadFiles(fillInput, []string{f.files["ten"]}, chromedp.ByQuery),
				chromedp.WaitVisible(`.modal[data-kind="profile_fill"] [data-testid="fill-matched"]`, chromedp.ByQuery),
				planOpen("profile_fill"))
		}},

	// The mod slide-over: its settings and its category form.
	{name: "slide-over", fixture: "copyable", root: `.slide-over`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SlideOverPath("fake", "copyme")),
				chromedp.WaitVisible(`.slide-over [data-testid="category-input"]`, chromedp.ByQuery)}
		}},

	// Setup's forms.
	// Two sources (the fake and the auth one), so "Also map other sources"
	// renders; both disclosures are opened so their bodies are measured.
	{name: "setup: add a game", fixture: "search", root: `[data-testid="setup-add-game"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SetupPath("games")), openAddGameForm(),
				chromedp.WaitVisible(`[data-testid="setup-add-game"] .setup-add__extra-sources`, chromedp.ByQuery),
				chromedp.Evaluate(`document.querySelectorAll('[data-testid="setup-add-game"] details').forEach((d) => { d.open = true; })`, nil)}
		}},
	{name: "setup: game editor", fixture: "copyable", root: `[data-testid="game-editor"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SetupPath("games")),
				pollUntil(`[...document.querySelectorAll('[data-testid="setup-games"] [data-action]')].length > 0 &&
					[...document.querySelectorAll('[data-testid="setup-games"] [data-action]')].every((b) => !b.disabled)`),
				openEditor(f.Game.ID)}
		}},
	{name: "setup: custom source editor", fixture: "copyable", root: `[data-testid="source-editor"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SetupPath("sources")),
				clickWhenSettled(`[data-action="new-source"]`),
				chromedp.WaitVisible(`[data-testid="source-editor"]`, chromedp.ByQuery),
				clickWhenSettled(`[data-testid="source-editor"] .form-check input[type="checkbox"]`),
				chromedp.WaitVisible(`[data-testid="source-editor"] input[type="text"]`, chromedp.ByQuery)}
		}},
	{name: "setup: authentication", fixture: "search", root: `[data-testid="setup-auth"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SetupPath("auth")),
				chromedp.WaitVisible(`[data-source="authy"] input[type="password"]`, chromedp.ByQuery)}
		}},
	{name: "setup: import an archive", fixture: "copyable", root: `[data-testid="setup-import-archive"]`, wantField: true,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SetupPath("archive")),
				chromedp.WaitVisible(`[data-testid="setup-import-archive"] .button`, chromedp.ByQuery),
				chromedp.SetUploadFiles(`[data-testid="setup-import-archive"] input[type="file"]`, []string{f.files["import"]}, chromedp.ByQuery),
				chromedp.WaitVisible(`[data-testid="staged-upload"] select`, chromedp.ByQuery)}
		}},
	{name: "setup: adopt", fixture: "workshop", root: `.setup-page__body`,
		open: func(f dialogFixture) chromedp.Tasks {
			return chromedp.Tasks{chromedp.Navigate(f.SetupPath("adopt")),
				chromedp.WaitVisible(`[data-testid="setup-workshop-adopt"] .button`, chromedp.ByQuery)}
		}},
}

// dialogKindsNotDriven is every modal kind TestE2E_EveryDialogsControlsLineUp
// does not open, with the reason. Empty, and meant to stay that way: a new
// entry needs a reason a reviewer accepts.
var dialogKindsNotDriven = map[string]string{}

// openAddGameForm shows Setup › Games' add form.
func openAddGameForm() chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
		chromedp.Evaluate(`[...document.querySelectorAll('[data-testid="setup-games"] .setup-section__actions button')]
			.find((b) => b.textContent.includes("Add a game manually")).click()`, nil),
		chromedp.WaitVisible(`[data-testid="setup-add-game"] select[name="add-source"]`, chromedp.ByQuery),
	}
}

// assertDialogControls holds one dialog's report to the control system.
func assertDialogControls(t *testing.T, where string, got dialogControlsReport, wantField bool) {
	t.Helper()
	if wantField {
		require.NotEmpty(t, got.Fields, "%s: no field measured - the opener stopped short of the form", where)
	}
	var normal, small []dialogControl
	for _, b := range got.Buttons {
		if b.Small {
			small = append(small, b)
		} else {
			normal = append(normal, b)
		}
	}
	require.NotEmpty(t, append(normal, got.Fields...), "%s: no control measured at all", where)

	same := func(what string, controls []dialogControl, want dialogControl) {
		for _, c := range controls {
			assert.LessOrEqual(t, math.Abs(c.Height-want.Height), 0.5,
				"%s: %s %s is %.2fpx tall, %s is %.2fpx - %s share one height",
				where, what, c.What, c.Height, want.What, want.Height, what)
		}
	}
	if len(got.Fields) > 0 {
		same("fields", got.Fields, got.Fields[0])
	}
	if len(normal) > 0 {
		same("buttons", normal, normal[0])
		same("fields and buttons", got.Fields, normal[0])
	}
	if len(small) > 0 {
		same("small buttons", small, small[0])
	}
	for i, row := range got.Rows {
		if len(row) < 2 {
			continue
		}
		first := row[0]
		for _, c := range row[1:] {
			if c.Top >= first.Bottom || c.Bottom <= first.Top {
				continue // wrapped onto a line of its own
			}
			assert.LessOrEqual(t, math.Abs(c.Top-first.Top), 0.5,
				"%s: form-row %d: %s's top is %.2f, %s's is %.2f - a field and its button line up", where, i, c.What, c.Top, first.What, first.Top)
			assert.LessOrEqual(t, math.Abs(c.Bottom-first.Bottom), 0.5,
				"%s: form-row %d: %s's bottom is %.2f, %s's is %.2f - a field and its button line up", where, i, c.What, c.Bottom, first.What, first.Bottom)
		}
	}
	for _, g := range got.Tight {
		t.Errorf("%s: %s sits %.2fpx below %s, under the form rhythm's %.2fpx - its container lays the form out without the form-layout classes",
			where, g.What, g.Gap, g.Prev, g.Min)
	}
}

// TestE2E_EveryDialogsControlsLineUp opens every dialog in dialogScenarios,
// in each theme and layout face, and holds its fields and buttons to one
// height and its form rows to one line.
func TestE2E_EveryDialogsControlsLineUp(t *testing.T) {
	fixtures := buildDialogFixtures(t)
	for _, face := range e2eLayoutFaces {
		t.Run(face.name, func(t *testing.T) {
			// Each fixture drives a tab of its own: the face goes on every one.
			for _, f := range fixtures {
				useFace(t, f.Ctx, face)
			}
			for _, theme := range []string{"light", "dark"} {
				t.Run(theme, func(t *testing.T) {
					for _, s := range dialogScenarios {
						t.Run(s.name, func(t *testing.T) {
							f := fixtures[s.fixture]
							var got dialogControlsReport
							f.runInBrowser(t, chromedp.EmulateViewport(1280, 900))
							f.runInBrowser(t, s.open(f)...)
							f.runInBrowser(t,
								faceInEffect(face),
								chromedp.Evaluate(`document.documentElement.setAttribute("data-theme", "`+theme+`")`, nil),
								settleEffects(),
								chromedp.Evaluate(dialogControlsJS+`(`+jsString(s.root)+`)`, &got),
							)
							assertDialogControls(t, face.name+" "+theme+" "+s.name, got, s.wantField)
						})
					}
				})
			}
		})
	}
	for _, f := range fixtures {
		// Two refusals are the dialogs themselves: Play on a drifted
		// profile answers 409 with the drift the modal shows, and an
		// archive with no version answers 400 with the question the
		// version prompt asks.
		assertOnlyExpectedErrors(t, f.BrowserErrors(), "/launch", "/api/v1/plans/update_from_archive")
	}
}

// dialogNarrowGutter is the least space a modal or slide-over panel keeps
// to the window's edge at a phone width.
const dialogNarrowGutter = 16

// dialogNarrowExceptions are the dialogs that do not yet fit a 375px
// window, with the reason. Each was already so on develop before #653.
var dialogNarrowExceptions = map[string]string{
	"launch-drift":  "its footer's Cancel / Deploy & launch / Launch anyway row does not wrap and overruns the modal at 375px - as on develop before #653",
	"folder-picker": "a breadcrumb is one path segment on a button that never wraps, and the fixture's temporary folder name is wider than a 375px modal - as on develop before #653",
}

// TestE2E_EveryDialogFitsANarrowWindow opens every dialog in
// dialogScenarios in a 375px window and holds every control inside its
// dialog, and a modal or slide-over panel inside the window with a
// dialogNarrowGutter on each side. (A page that scrolls sideways at this
// width is a separate, known gap: the page is not measured, the dialog
// is.)
func TestE2E_EveryDialogFitsANarrowWindow(t *testing.T) {
	fixtures := buildDialogFixtures(t)
	for _, s := range dialogScenarios {
		t.Run(s.name, func(t *testing.T) {
			f := fixtures[s.fixture]
			var got dialogFitReport
			f.runInBrowser(t, chromedp.EmulateViewport(375, 800))
			f.runInBrowser(t, s.open(f)...)
			f.runInBrowser(t,
				settleEffects(),
				// A panel slides in: measure where it comes to rest.
				pollUntil(`document.getAnimations().every((a) => a.playState !== "running")`),
				chromedp.Evaluate(dialogFitJS+`(`+jsString(s.root)+`)`, &got),
			)
			if reason, ok := dialogNarrowExceptions[s.name]; ok {
				t.Skipf("known not to fit 375px yet: %s", reason)
			}
			assert.Empty(t, got.Overruns, "%s: controls overrun the dialog at 375px", s.name)
			if got.Panel != nil {
				assert.GreaterOrEqual(t, got.Panel[0], float64(dialogNarrowGutter)-0.5,
					"%s: the panel's left edge is %.1fpx from the window's", s.name, got.Panel[0])
				gutter := float64(dialogNarrowGutter)
				if got.AnchoredRight {
					gutter = 0
				}
				assert.LessOrEqual(t, got.Panel[1], got.Viewport-gutter+0.5,
					"%s: the panel's right edge is %.1fpx from the window's", s.name, got.Viewport-got.Panel[1])
			}
		})
	}
	for _, f := range fixtures {
		assertOnlyExpectedErrors(t, f.BrowserErrors(), "/launch", "/api/v1/plans/update_from_archive")
	}
}

// buildDialogFixtures builds every fixture dialogScenarios names, once.
func buildDialogFixtures(t *testing.T) map[string]dialogFixture {
	t.Helper()
	fixtures := map[string]dialogFixture{}
	for _, s := range dialogScenarios {
		if _, ok := fixtures[s.fixture]; !ok {
			build, known := dialogFixtures[s.fixture]
			require.True(t, known, "%s: unknown fixture %q", s.name, s.fixture)
			fixtures[s.fixture] = build(t)
		}
	}
	return fixtures
}

// TestE2E_DialogScenariosCoverEveryKind fails when a modal kind the SPA can
// open - a plan kind (plankinds.go's closed table, each one a confirm-plan
// modal) or a modal of its own (a Modal kind="..." in spa/app) - has no
// scenario, or is listed in dialogKindsNotDriven with no reason.
func TestE2E_DialogScenariosCoverEveryKind(t *testing.T) {
	covered := map[string]bool{}
	kindOf := regexp.MustCompile(`data-kind="([a-z_-]+)"`)
	for _, s := range dialogScenarios {
		if m := kindOf.FindStringSubmatch(s.root); m != nil {
			covered[m[1]] = true
		}
	}
	var kinds []string
	kinds = append(kinds, planKindNames()...)
	err := filepath.Walk(filepath.Join("spa", "app"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".js" {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		kinds = append(kinds, modalKindsIn(string(data))...)
		return nil
	})
	require.NoError(t, err)
	slices.Sort(kinds)
	kinds = slices.Compact(kinds)
	require.Contains(t, kinds, "profiles", "the scan found no Modal kind - it is reading the wrong place")

	var missing []string
	for _, k := range kinds {
		if covered[k] {
			continue
		}
		if reason, ok := dialogKindsNotDriven[k]; ok && strings.TrimSpace(reason) != "" {
			continue
		}
		missing = append(missing, k)
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "modal kinds with no row in dialogScenarios (or reasoned entry in dialogKindsNotDriven)")
}

// modalKind matches a kind="..." attribute - a Modal's kind, wherever it
// sits in its tag - and not data-kind="..." (which stamps it on the DOM).
var modalKind = regexp.MustCompile(`(?:^|[^\w-])kind="([a-z_-]+)"`)

// modalKindsIn returns every literal Modal kind in one module's source.
func modalKindsIn(src string) []string {
	var out []string
	for _, m := range modalKind.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestModalKindScan_ReadsEveryShape pins the coverage guard's reach: a kind
// on its own line, one on the same line as the tag, and not data-kind.
func TestModalKindScan_ReadsEveryShape(t *testing.T) {
	src := "html`<${Modal}\n  kind=\"profiles\"\n  title=\"x\">\n" +
		"<${Modal} kind=\"brand-new\" title=\"x\">\n" +
		"<div data-kind=\"not-a-modal\" class=\"modal\">`"
	assert.Equal(t, []string{"profiles", "brand-new"}, modalKindsIn(src))
}

// planKindNames reads every plan kind's name from the kind_*.go files -
// each one a confirm-plan modal - leaving out enable and disable, which
// are the sanctioned non-Plan jobs and open no modal.
func planKindNames() []string {
	var out []string
	paths, _ := filepath.Glob("kind_*.go")
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, m := range regexp.MustCompile(`(?m)^\s*Name:\s*"([a-z_]+)",`).FindAllStringSubmatch(string(data), -1) {
			if m[1] != "enable" && m[1] != "disable" {
				out = append(out, m[1])
			}
		}
	}
	return out
}

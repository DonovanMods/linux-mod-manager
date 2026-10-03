package serve_test

// #525: Setup's tables edit a row with a pencil icon button, not a repeated
// "Edit <field>…" text button. These scenarios hold the pencil's accessible
// name, its tooltip (on hover AND on keyboard focus, never clipped), and the
// layout in every face.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// setupGamesSample is one row of newE2EFixtureWithSetupGames.
type setupGamesSample struct {
	id, name, dir string
	sources       []string
	loader        string
}

// newE2EFixtureWithSetupGames is newE2EFixture plus six more games shaped
// like a real library (the #525 screenshot): long Steam-style install and
// mod paths, one to three sources each, a few with a bepinex loader. The
// fixture's own game stays the default, so every other row offers "Set
// default".
func newE2EFixtureWithSetupGames(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)
	for _, id := range []string{"nexusmods", "curseforge", "steamworkshop", "thunderstore", "donovan-mods", "icarus"} {
		f.Svc.RegisterSource(newFakeSource(id))
	}
	root := t.TempDir()
	for _, s := range []setupGamesSample{
		{"skyrim", "Skyrim Special Edition", "SkyrimSpecialEdition", []string{"donovan-mods", "nexusmods"}, ""},
		{"fallout4", "Fallout 4", "Fallout 4", []string{"nexusmods"}, ""},
		{"valheim", "Valheim", "Valheim", []string{"steamworkshop"}, "bepinex"},
		{"icarus", "Icarus", "Icarus", []string{"icarus"}, ""},
		{"lethal", "Lethal Company", "Lethal Company", []string{"thunderstore"}, "bepinex"},
		{"drive", "Driveclub Moderne Edition", "battlenet/driveclub", []string{"curseforge"}, ""},
	} {
		install := filepath.Join(root, "data", "SteamLibrary", "steamapps", "common", s.dir)
		mods := filepath.Join(install, "Data", "Mods")
		require.NoError(t, os.MkdirAll(mods, 0o755))
		g := &domain.Game{
			ID: s.id, Name: s.name, InstallPath: install, ModPath: mods,
			LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{},
		}
		for _, src := range s.sources {
			g.SourceIDs[src] = s.id
		}
		if s.loader != "" {
			g.Loader = &domain.GameLoader{Kind: s.loader}
		}
		require.NoError(t, f.Svc.SaveGame(t.Context(), g))
	}
	return f
}

// pencilSel is one row's pencil for a field (data-action edit-mod-path,
// edit-sources, edit-loader or edit-game).
func pencilSel(action, game string) string {
	return fmt.Sprintf(`[data-testid="setup-games"] [data-action=%q][data-game=%q]`, action, game)
}

// setupGamesReady waits for the Games table to hold every seeded row with
// its sources read in (the pencils that need them are disabled until then).
func setupGamesReady() chromedp.Action {
	return chromedp.Tasks{
		pollUntil(`document.querySelectorAll('[data-testid="setup-games"] tbody tr').length === 7`),
		pollUntil(`[...document.querySelectorAll('[data-testid="setup-games"] [data-action]')].every((b) => !b.disabled)`),
	}
}

// pointerTo scrolls sel into view (at a narrow width the table is wider than
// the page), then moves the real mouse onto its centre once its box has
// stopped moving (the same box twice in a row, as clickWhenSettled waits for:
// coordinates measured while the table is still laying out land on whatever
// is there next), arriving from a few pixels away so the browser always sees
// the pointer ENTER the control.
func pointerTo(sel string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var prev, c struct{ X, Y float64 }
		for i := 0; i < 100; i++ {
			if err := chromedp.Evaluate(fmt.Sprintf(`(() => {
				const el = document.querySelector(%q);
				el.scrollIntoView({ block: "center", inline: "center" });
				const r = el.getBoundingClientRect();
				return { X: r.x + r.width / 2, Y: r.y + r.height / 2 };
			})()`, sel), &c).Do(ctx); err != nil {
				return err
			}
			if c == prev {
				break
			}
			prev = c
			time.Sleep(e2ePollInterval)
		}
		if err := input.DispatchMouseEvent(input.MouseMoved, c.X-40, c.Y-40).Do(ctx); err != nil {
			return err
		}
		return input.DispatchMouseEvent(input.MouseMoved, c.X, c.Y).Do(ctx)
	})
}

// focusByKeyboard focuses sel the way a keyboard user arrives at it: a Tab
// first, so the browser is in keyboard modality and :focus-visible matches.
func focusByKeyboard(sel string) chromedp.Action {
	return chromedp.Tasks{
		chromedp.KeyEvent(kb.Tab),
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).focus()`, sel), nil),
	}
}

// tooltipState is what the shared tooltip shows and where.
type tooltipState struct {
	Visible bool    `json:"visible"`
	Text    string  `json:"text"`
	Hidden  string  `json:"ariaHidden"`
	Role    string  `json:"role"`
	Left    float64 `json:"left"`
	Top     float64 `json:"top"`
	Right   float64 `json:"right"`
	Bottom  float64 `json:"bottom"`
	ViewW   float64 `json:"viewW"`
	ViewH   float64 `json:"viewH"`
}

const tooltipStateJS = `(() => {
	const el = document.querySelector(".tooltip");
	if (!el) return { visible: false, viewW: document.documentElement.clientWidth, viewH: window.innerHeight };
	const r = el.getBoundingClientRect();
	const shown = el.dataset.visible === "true" && r.width > 0 && getComputedStyle(el).visibility !== "hidden";
	return {
		visible: shown, text: el.textContent, ariaHidden: el.getAttribute("aria-hidden") ?? "", role: el.getAttribute("role") ?? "",
		left: r.left, top: r.top, right: r.right, bottom: r.bottom,
		viewW: document.documentElement.clientWidth, viewH: window.innerHeight,
	};
})()`

// assertTooltipShows fails unless the shared tooltip is showing want and sits
// wholly inside the viewport.
func assertTooltipShows(t *testing.T, where, want string, got tooltipState) {
	t.Helper()
	require.True(t, got.Visible, "%s: the tooltip is visible", where)
	assert.Equal(t, want, got.Text, where)
	assert.GreaterOrEqual(t, got.Left, 0.0, "%s: the tooltip is not clipped on the left", where)
	assert.GreaterOrEqual(t, got.Top, 0.0, "%s: the tooltip is not clipped at the top", where)
	assert.LessOrEqual(t, got.Right, got.ViewW, "%s: the tooltip is not clipped on the right", where)
	assert.LessOrEqual(t, got.Bottom, got.ViewH, "%s: the tooltip is not clipped at the bottom", where)
	assert.Equal(t, "true", got.Hidden, "%s: the tooltip is hidden from assistive technology - the control's aria-label is its name", where)
	assert.Empty(t, got.Role, "%s: the tooltip carries no role that would announce the name twice", where)
}

// TestE2E_SetupGames_EditsArePencilsWithTheRowInTheirName: no "Edit
// <field>..." text is left in the Games table; each pencil's accessible name,
// as Chrome computes it, says which game it edits.
func TestE2E_SetupGames_EditsArePencilsWithTheRowInTheirName(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)

	var texts []string
	var withIcon, total int
	names := map[string]e2eAXNode{}
	games := map[string]string{"skyrim": "Skyrim Special Edition", "valheim": "Valheim", "g1": "Fixture Game"}
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		chromedp.Evaluate(`[...document.querySelectorAll('[data-testid="setup-games"] button')]
			.map((b) => b.textContent.trim()).filter((t) => /^Edit/.test(t) || t === "Cancel")`, &texts),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="setup-games"] button[data-action^="edit-"]').length`, &total),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="setup-games"] button[data-action^="edit-"].button--quiet.button--icon.button--small svg[aria-hidden="true"]').length`, &withIcon),
		chromedp.ActionFunc(func(ctx context.Context) error {
			for id := range games {
				for _, action := range []string{"edit-mod-path", "edit-sources", "edit-loader", "edit-game"} {
					var ax e2eAXNode
					if err := accessibleNodeOf(pencilSel(action, id), &ax).Do(ctx); err != nil {
						return err
					}
					names[action+"/"+id] = ax
				}
			}
			return nil
		}),
	)

	assert.Empty(t, texts, "no visible \"Edit ...\" text label remains in the Games table")
	assert.Equal(t, 28, total, "four pencils on each of the seven rows")
	assert.Equal(t, total, withIcon, "every one is a quiet small icon button drawing an aria-hidden SVG")
	for id, name := range games {
		for action, want := range map[string]string{
			"edit-mod-path": "Edit mod path for " + name,
			"edit-sources":  "Edit sources for " + name,
			"edit-loader":   "Edit loader for " + name,
			"edit-game":     "Edit " + name,
		} {
			ax := names[action+"/"+id]
			assert.False(t, ax.Ignored, "%s/%s", action, id)
			assert.Equal(t, "button", ax.Role, "%s/%s", action, id)
			assert.Equal(t, want, ax.Name, "%s/%s", action, id)
		}
	}
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_Tooltip_ShowsOnHoverAndOnKeyboardFocusAndStaysInView: every pencil
// says what it edits in a tooltip a mouse OR the keyboard raises, and the
// right-most column's does not run off the page - at the widths the table is
// used at, in every layout face.
func TestE2E_Tooltip_ShowsOnHoverAndOnKeyboardFocusAndStaysInView(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		for _, width := range []int64{1280, 700} {
			where := fmt.Sprintf("%s %dpx", face.name, width)
			var hover, focus, esc, left, afterEsc tooltipState
			var focusVisible bool
			f.runInBrowser(t,
				chromedp.EmulateViewport(width, 900),
				chromedp.Navigate(f.SetupPath("games")),
				setupGamesReady(),
				faceInEffect(face),
				// Hover, on the right-most pencil: the one that could clip.
				pointerTo(pencilSel("edit-game", "valheim")),
				pollUntil(`document.querySelector(".tooltip")?.dataset.visible === "true"`),
				chromedp.Evaluate(tooltipStateJS, &hover),
				pointerTo(`.setup-nav__tab[data-section="auth"]`),
				pollUntil(`document.querySelector(".tooltip")?.dataset.visible !== "true"`),
				chromedp.Evaluate(tooltipStateJS, &left),
				// Keyboard focus, on a field pencil.
				focusByKeyboard(pencilSel("edit-mod-path", "skyrim")),
				chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).matches(":focus-visible")`, pencilSel("edit-mod-path", "skyrim")), &focusVisible),
				pollUntil(`document.querySelector(".tooltip")?.dataset.visible === "true"`),
				chromedp.Evaluate(tooltipStateJS, &focus),
				// Escape dismisses it without moving focus (WCAG 1.4.13).
				chromedp.KeyEvent(kb.Escape),
				pollUntil(`document.querySelector(".tooltip")?.dataset.visible !== "true"`),
				chromedp.Evaluate(tooltipStateJS, &esc),
				chromedp.Evaluate(fmt.Sprintf(`document.activeElement === document.querySelector(%q)`, pencilSel("edit-mod-path", "skyrim")), &afterEsc.Visible),
			)
			assertTooltipShows(t, where+" hover", "Edit Valheim", hover)
			assert.False(t, left.Visible, "%s: leaving the pencil hides its tooltip", where)
			require.True(t, focusVisible, "%s: the scenario really arrived by keyboard", where)
			assertTooltipShows(t, where+" focus", "Edit mod path", focus)
			assert.False(t, esc.Visible, "%s: Escape dismisses the tooltip", where)
			assert.True(t, afterEsc.Visible, "%s: and leaves focus on the pencil", where)
		}
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_Tooltip_NamesEveryFieldPencil: each field's tooltip names the
// field, not the row.
func TestE2E_Tooltip_NamesEveryFieldPencil(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)

	for action, want := range map[string]string{
		"edit-mod-path": "Edit mod path",
		"edit-sources":  "Edit sources",
		"edit-loader":   "Edit loader",
		"edit-game":     "Edit Fallout 4",
	} {
		var got tooltipState
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SetupPath("games")),
			setupGamesReady(),
			focusByKeyboard(pencilSel(action, "fallout4")),
			pollUntil(`document.querySelector(".tooltip")?.dataset.visible === "true"`),
			chromedp.Evaluate(tooltipStateJS, &got),
		)
		assertTooltipShows(t, action, want, got)
	}
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SetupGames_PencilsStillOpenTheirEditors: the pencil is the same
// control the text button was - same data-action, same editor, and an open
// editor is announced by aria-expanded rather than by a changed label.
func TestE2E_SetupGames_PencilsStillOpenTheirEditors(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)

	for action, editor := range map[string]string{
		"edit-mod-path": `[data-testid="setup-games"] .setup-table__editor input`,
		"edit-sources":  `[data-testid="setup-games"] [data-action="save-sources"]`,
		"edit-loader":   `[data-testid="loader-editor"]`,
	} {
		var expanded, name string
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SetupPath("games")),
			setupGamesReady(),
			clickWhenSettled(pencilSel(action, "valheim")),
			chromedp.WaitVisible(editor, chromedp.ByQuery),
			chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).getAttribute("aria-expanded") ?? ""`, pencilSel(action, "valheim")), &expanded),
			chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).getAttribute("aria-label")`, pencilSel(action, "valheim")), &name),
			clickWhenSettled(pencilSel(action, "valheim")),
			waitGone(`.setup-table__editor`),
		)
		assert.Equal(t, "true", expanded, action)
		assert.Contains(t, name, "Valheim", "%s: the name does not change when the editor opens", action)
	}
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SetupGamesTable_PencilsHoldInEveryFace is #521's stress pass over
// the pencil layout: seven rows with long paths, a loader and up to two
// sources, 1280px, every face. Nothing wraps, every pencil is a square on
// one line inside the table and the page, and the page does not scroll.
func TestE2E_SetupGamesTable_PencilsHoldInEveryFace(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var got map[string][]labelBox
		var cellLines []struct {
			Cell  string `json:"cell"`
			Lines int    `json:"lines"`
		}
		var pencils []struct {
			W, H, Right, TableRight, ValueTop, ValueBottom, Top, Bottom float64
			Action                                                      string
		}
		var overflows bool
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SetupPath("games")),
			setupGamesReady(),
			faceInEffect(face),
			measureLabels(`{"games table": "[data-testid=\"setup-games\"]"}`, &got),
			chromedp.Evaluate(`(() => { `+textLinesJS+` return [...document.querySelectorAll('[data-testid="setup-games"] tbody tr:not(.setup-table__editor) > td:not(.col--path)')]
				.map((td) => ({ cell: td.closest("table").querySelectorAll("thead th")[td.cellIndex].textContent.trim() || "actions", lines: textLines(td) })); })()`, &cellLines),
			chromedp.Evaluate(`[...document.querySelectorAll('[data-testid="setup-games"] button[data-action^="edit-"]')].map((b) => {
				const r = b.getBoundingClientRect();
				const t = b.closest("table").getBoundingClientRect();
				const v = (b.closest(".setup-table__value") ?? b.closest("td")).getBoundingClientRect();
				return { W: r.width, H: r.height, Right: r.right, TableRight: t.right, Top: r.top, Bottom: r.bottom, ValueTop: v.top, ValueBottom: v.bottom, Action: b.dataset.action };
			})`, &pencils),
			chromedp.Evaluate(`document.documentElement.scrollWidth > window.innerWidth`, &overflows),
		)
		assertLabelsCentred(t, "setup games table", got["games table"])
		require.Len(t, cellLines, 42, "six cells on each of the seven rows")
		for _, c := range cellLines {
			switch c.Cell {
			case "Actions":
				assert.Zero(t, c.Lines, "the row's pencil has no text")
			case "Sources":
				// A list of sources wraps between its ids by design (the
				// pencil stays beside it, asserted below); never mid-id.
				assert.LessOrEqual(t, c.Lines, 2, "the Sources cell wraps at most between two ids")
			default:
				assert.Equal(t, 1, c.Lines, "the %s cell is laid out on one line", c.Cell)
			}
		}
		require.Len(t, pencils, 28)
		for _, p := range pencils {
			assert.InDelta(t, p.W, p.H, 0.5, "%s: a pencil is square", p.Action)
			assert.LessOrEqual(t, p.Right, p.TableRight+0.5, "%s: a pencil is inside the table", p.Action)
			assert.LessOrEqual(t, p.Right, 1280.0, "%s: a pencil is inside the page", p.Action)
			assert.GreaterOrEqual(t, p.Top, p.ValueTop-0.5, "%s: a pencil sits in its own value's row", p.Action)
			assert.LessOrEqual(t, p.Bottom, p.ValueBottom+0.5, "%s: a pencil sits in its own value's row", p.Action)
		}
		assert.False(t, overflows, "the games table fits the page")
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SetupSources_EditIsAPencil: a custom source's Edit is a pencil too
// (Download and Delete stay words: they are actions, not edits), named for
// its source and tooltipped, in every face.
func TestE2E_SetupSources_EditIsAPencil(t *testing.T) {
	f, _ := newE2EIndexFixture(t, nil)
	yaml := "id: my-mods\nname: My Mods\ntype: directory\ndirectory:\n  path: " + t.TempDir() + "\n"
	_, err := app.SaveSourceDefinition(f.Ctx, f.Svc, "", []byte(yaml))
	require.NoError(t, err)
	sel := `tr[data-source="my-mods"] [data-action="edit-source"]`

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var ax e2eAXNode
		var text string
		var square struct{ W, H float64 }
		var tip tooltipState
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SetupPath("sources")),
			chromedp.WaitVisible(sel, chromedp.ByQuery),
			faceInEffect(face),
			accessibleNodeOf(sel, &ax),
			chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).textContent.trim()`, sel), &text),
			chromedp.Evaluate(fmt.Sprintf(`(() => { const r = document.querySelector(%q).getBoundingClientRect(); return { W: r.width, H: r.height }; })()`, sel), &square),
			focusByKeyboard(sel),
			pollUntil(`document.querySelector(".tooltip")?.dataset.visible === "true"`),
			chromedp.Evaluate(tooltipStateJS, &tip),
		)
		assert.Equal(t, "button", ax.Role)
		assert.Equal(t, "Edit source My Mods", ax.Name)
		assert.Empty(t, text, "the pencil has no text label")
		assert.InDelta(t, square.W, square.H, 0.5)
		assertTooltipShows(t, face.name, "Edit source", tip)
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

package serve_test

// #529: every directory field has a "Browse…" button that opens a folder
// chooser over the SERVER's filesystem (a browser cannot name a folder), and
// typing a path by hand still works. Every scenario asserts the END STATE -
// the field's value, and games.yaml on disk - and the layout checks run again
// under #521's face stress pass.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

const (
	// modalSel is the whole dialog: its footer (Cancel, Choose) sits outside
	// the body that pickerSel names.
	modalSel      = `.modal[data-kind="folder-picker"]`
	pickerSel     = `[data-testid="folder-picker"]`
	pickerPathSel = pickerSel + ` input[name="folder-path"]`
	pickerListSel = pickerSel + ` [data-testid="folder-picker-list"]`
)

// folderTree builds root/{alpha,beta/deep,.hidden,file.txt} and returns root.
func folderTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"alpha", "beta/deep", ".hidden"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644))
	return root
}

// browseSel is the Browse… button of the editor field named name
// ("install-path" or "mod-path").
func browseSel(name string) string {
	return fmt.Sprintf(`[data-testid="game-editor"] .game-editor__field:has(input[name=%q]) [data-action="browse-folder"]`, name)
}

// pickerReady waits for the picker to have read a folder and handed focus to
// its list.
func pickerReady() chromedp.Action {
	return chromedp.Tasks{
		chromedp.WaitVisible(pickerSel, chromedp.ByQuery),
		pollUntil(fmt.Sprintf(`!!document.querySelector(%q)?.value && document.activeElement === document.querySelector(%q)`, pickerPathSel, pickerListSel)),
	}
}

// pickerPathIs waits for the picker to be showing path.
func pickerPathIs(path string) chromedp.Action {
	return pollUntil(fmt.Sprintf(`document.querySelector(%q)?.value === %q`, pickerPathSel, path))
}

func pickerEntries(out *[]string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`[...document.querySelectorAll(%q)].map((e) => e.dataset.folder)`, pickerSel+` [role="option"]`), out)
}

func fieldValue(sel string, out *string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).value`, sel), out)
}

func savedInstallPath(t *testing.T, f e2eFixture, game string) string {
	t.Helper()
	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	require.NotNil(t, onDisk[game])
	return onDisk[game].InstallPath
}

// TestE2E_FolderPicker_GameEditorInstallPath: from the editor's install path,
// open the chooser where the field points, go down and up, choose, see the
// field filled and Save it to games.yaml.
func TestE2E_FolderPicker_GameEditorInstallPath(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	root := folderTree(t)

	var entries, afterHidden []string
	var field, hiddenFieldWhileOpen string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("valheim"),
		typeInto(editorSel(`input[name="install-path"]`), root),
		clickWhenSettled(browseSel("install-path")),
		pickerReady(),
		// It opened where the field points, listing folders only - no
		// file.txt, no .hidden.
		pickerPathIs(root),
		pickerEntries(&entries),
	)
	assert.Equal(t, []string{"alpha", "beta"}, entries)

	f.runInBrowser(t,
		// Hidden folders are opt-in.
		clickWhenSettled(pickerSel+` input[name="folder-hidden"]`),
		pollUntil(fmt.Sprintf(`document.querySelectorAll(%q).length === 3`, pickerSel+` [role="option"]`)),
		pickerEntries(&afterHidden),
		clickWhenSettled(pickerSel+` input[name="folder-hidden"]`),
		pollUntil(fmt.Sprintf(`document.querySelectorAll(%q).length === 2`, pickerSel+` [role="option"]`)),
		// Down: double-click beta, then its subfolder.
		chromedp.DoubleClick(pickerSel+` [data-folder="beta"]`, chromedp.ByQuery),
		pickerPathIs(filepath.Join(root, "beta")),
		chromedp.DoubleClick(pickerSel+` [data-folder="deep"]`, chromedp.ByQuery),
		pickerPathIs(filepath.Join(root, "beta", "deep")),
		// Up twice, once by Up and once by a breadcrumb.
		clickWhenSettled(pickerSel+` [data-action="folder-up"]`),
		pickerPathIs(filepath.Join(root, "beta")),
		clickWhenSettled(fmt.Sprintf(`%s [data-crumb=%q]`, pickerSel, root)),
		pickerPathIs(root),
		// Choose alpha: open it, then Choose this folder.
		chromedp.DoubleClick(pickerSel+` [data-folder="alpha"]`, chromedp.ByQuery),
		pickerPathIs(filepath.Join(root, "alpha")),
		fieldValue(editorSel(`input[name="install-path"]`), &hiddenFieldWhileOpen),
		clickWhenSettled(modalSel+` [data-action="folder-choose"]`),
		waitGone(pickerSel),
		fieldValue(editorSel(`input[name="install-path"]`), &field),
	)
	assert.Equal(t, []string{".hidden", "alpha", "beta"}, afterHidden)
	assert.Equal(t, root, hiddenFieldWhileOpen, "browsing alone changes nothing")
	assert.Equal(t, filepath.Join(root, "alpha"), field, "the chosen folder fills the field")

	f.runInBrowser(t,
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
	)
	assert.Equal(t, filepath.Join(root, "alpha"), savedInstallPath(t, f, "valheim"), "Save persists it")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_FolderPicker_ModPathIsChosenAbsolute: the mod path's Browse writes
// an absolute path, and Save persists it.
func TestE2E_FolderPicker_ModPathIsChosenAbsolute(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	root := folderTree(t)

	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("valheim"),
		typeInto(editorSel(`input[name="mod-path"]`), root),
		clickWhenSettled(browseSel("mod-path")),
		pickerReady(),
		pickerPathIs(root),
		chromedp.DoubleClick(pickerSel+` [data-folder="beta"]`, chromedp.ByQuery),
		pickerPathIs(filepath.Join(root, "beta")),
		clickWhenSettled(modalSel+` [data-action="folder-choose"]`),
		waitGone(pickerSel),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
	)
	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "beta"), onDisk["valheim"].ModPath)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_FolderPicker_KeyboardOnly: Enter on Browse… opens it with the list
// focused; arrows move, Enter opens, Backspace goes up, Tab reaches Choose;
// Escape closes and gives focus back to Browse….
func TestE2E_FolderPicker_KeyboardOnly(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	root := folderTree(t)

	var field, focused string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("valheim"),
		typeInto(editorSel(`input[name="install-path"]`), root),
		chromedp.Focus(browseSel("install-path"), chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		pickerReady(),
		pickerPathIs(root),
		settleEffects(),
		// alpha is highlighted; Down moves to beta; Enter opens it.
		chromedp.KeyEvent(kb.ArrowDown),
		chromedp.KeyEvent(kb.Enter),
		pickerPathIs(filepath.Join(root, "beta")),
		// Backspace goes up.
		chromedp.KeyEvent(kb.Backspace),
		pickerPathIs(root),
		// Down, Down is clamped at the last entry; Enter opens beta again.
		chromedp.KeyEvent(kb.ArrowDown),
		chromedp.KeyEvent(kb.ArrowDown),
		chromedp.KeyEvent(kb.Enter),
		pickerPathIs(filepath.Join(root, "beta")),
		// Tab out of the list: Cancel, then Choose.
		chromedp.KeyEvent(kb.Tab),
		chromedp.KeyEvent(kb.Tab),
		chromedp.Evaluate(`document.activeElement.dataset.action`, &focused),
	)
	require.Equal(t, "folder-choose", focused)
	f.runInBrowser(t,
		chromedp.KeyEvent(kb.Enter),
		waitGone(pickerSel),
		fieldValue(editorSel(`input[name="install-path"]`), &field),
	)
	assert.Equal(t, filepath.Join(root, "beta"), field)

	// Escape: nothing changes, focus is back on Browse….
	var active string
	var editorOpen bool
	f.runInBrowser(t,
		chromedp.Focus(browseSel("install-path"), chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		pickerReady(),
		settleEffects(),
		chromedp.KeyEvent(kb.Enter), // opens a folder - still only browsing
		chromedp.KeyEvent(kb.Escape),
		waitGone(pickerSel),
		chromedp.Evaluate(`document.activeElement.dataset.action ?? ""`, &active),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"]') !== null`, &editorOpen),
		fieldValue(editorSel(`input[name="install-path"]`), &field),
	)
	assert.Equal(t, "browse-folder", active, "Escape returns focus to Browse…")
	assert.True(t, editorOpen, "and closes only the chooser")
	assert.Equal(t, filepath.Join(root, "beta"), field, "Escape leaves the field alone")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_FolderPicker_CancelLeavesTheFieldAlone and the typed-path bar: a
// refused path says why and keeps the folder on screen.
func TestE2E_FolderPicker_CancelAndTypedPath(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	root := folderTree(t)
	before := savedInstallPath(t, f, "valheim")

	var field, errText string
	var entries []string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("valheim"),
		typeInto(editorSel(`input[name="install-path"]`), root),
		clickWhenSettled(browseSel("install-path")),
		pickerReady(),
		pickerPathIs(root),
		// A path that is not there: refused, listing kept.
		typeInto(pickerPathSel, filepath.Join(root, "nope")),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(`[data-testid="folder-picker-error"]`, chromedp.ByQuery),
		textContent(`[data-testid="folder-picker-error"]`, &errText),
		pickerEntries(&entries),
		// A relative path is refused too.
		typeInto(pickerPathSel, "relative/dir"),
		clickWhenSettled(pickerSel+` [data-action="folder-go"]`),
		pollUntil(`document.querySelector('[data-testid="folder-picker-error"]')?.textContent.includes("absolute")`),
		// A typed, existing path navigates.
		typeInto(pickerPathSel, filepath.Join(root, "beta")),
		chromedp.KeyEvent(kb.Enter),
		pickerPathIs(filepath.Join(root, "beta")),
		waitGone(`[data-testid="folder-picker-error"]`),
		clickWhenSettled(modalSel+` [data-action="folder-cancel"]`),
		waitGone(pickerSel),
		fieldValue(editorSel(`input[name="install-path"]`), &field),
	)
	assert.Contains(t, errText, "does not exist")
	assert.Equal(t, []string{"alpha", "beta"}, entries, "the folder on screen is kept")
	assert.Equal(t, root, field, "Cancel leaves the field unchanged")

	// And nothing was saved behind the user's back.
	assert.Equal(t, before, savedInstallPath(t, f, "valheim"))
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_FolderPicker_AddGameForm: the add-game form's install path and mod
// path both browse; the mod path opens at the install path while it is empty.
func TestE2E_FolderPicker_AddGameForm(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	root := folderTree(t)
	const form = `[data-testid="setup-add-game"] `

	var install, mod string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.BaseURL+"/"),
		pollUntil(`document.querySelector('[data-testid="setup-add-game"]') !== null`),
		clickWhenSettled(form+`.plan__control:has(input[name="add-install-path"]) [data-action="browse-folder"]`),
		pickerReady(),
	)
	// The empty install path opens at the home folder (the sandbox's): go to
	// the tree by typing into the path bar, then choose it.
	f.runInBrowser(t,
		typeInto(pickerPathSel, filepath.Join(root, "beta")),
		chromedp.KeyEvent(kb.Enter),
		pickerPathIs(filepath.Join(root, "beta")),
		clickWhenSettled(modalSel+` [data-action="folder-choose"]`),
		waitGone(pickerSel),
		fieldValue(form+`input[name="add-install-path"]`, &install),
		// The mod path starts at the install path.
		clickWhenSettled(form+`.plan__control:has(input[name="add-mod-path"]) [data-action="browse-folder"]`),
		pickerReady(),
		pickerPathIs(filepath.Join(root, "beta")),
		chromedp.DoubleClick(pickerSel+` [data-folder="deep"]`, chromedp.ByQuery),
		pickerPathIs(filepath.Join(root, "beta", "deep")),
		clickWhenSettled(modalSel+` [data-action="folder-choose"]`),
		waitGone(pickerSel),
		fieldValue(form+`input[name="add-mod-path"]`, &mod),
	)
	assert.Equal(t, filepath.Join(root, "beta"), install)
	assert.Equal(t, filepath.Join(root, "beta", "deep"), mod)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_FolderPicker_LayoutHoldsInEveryFace: the Browse… button sits level
// with its input at the control height, and the open chooser fits its panel
// and the viewport with every control inside it - in the default face and
// under #521's stress faces.
func TestE2E_FolderPicker_LayoutHoldsInEveryFace(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	root := folderTree(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		for _, width := range []int{1280, 720} {
			var got struct {
				InputH, ButtonH         float64
				InputTop, ButtonTop     float64
				ButtonRight, FieldRight float64
				ModalLeft, ModalRight   float64
				ModalBottom, Viewport   float64
				ViewportW               float64
				ModalScrollW, ModalW    float64
				Outside                 []string
			}
			f.runInBrowser(t,
				chromedp.EmulateViewport(int64(width), 800),
				chromedp.Navigate(f.SetupPath("games")),
				setupGamesReady(),
				faceInEffect(face),
				openEditor("valheim"),
				typeInto(editorSel(`input[name="install-path"]`), root),
				chromedp.Evaluate(fmt.Sprintf(`(() => {
					const field = document.querySelector(%q).closest(".folder-field");
					const i = field.querySelector("input").getBoundingClientRect();
					const b = field.querySelector("button").getBoundingClientRect();
					return {InputH: i.height, ButtonH: b.height, InputTop: i.top, ButtonTop: b.top,
						ButtonRight: b.right, FieldRight: field.getBoundingClientRect().right};
				})()`, browseSel("install-path")), &got),
			)
			assert.InDelta(t, got.InputH, got.ButtonH, 0.5, "%dpx: Browse… is the input's height", width)
			assert.InDelta(t, got.InputTop, got.ButtonTop, 0.5, "%dpx: Browse… is level with the input", width)
			assert.LessOrEqual(t, got.ButtonRight, got.FieldRight+0.5, "%dpx: Browse… stays inside the field", width)

			f.runInBrowser(t,
				clickWhenSettled(browseSel("install-path")),
				pickerReady(),
				chromedp.Evaluate(`(() => {
					const m = document.querySelector('.modal[data-kind="folder-picker"]');
					const r = m.getBoundingClientRect();
					const outside = [...m.querySelectorAll("button, input, [role=listbox]")]
						.filter((el) => {
							const b = el.getBoundingClientRect();
							return b.left < r.left - 0.5 || b.right > r.right + 0.5;
						}).map((el) => el.dataset.action || el.name || el.tagName);
					return {ModalLeft: r.left, ModalRight: r.right, ModalBottom: r.bottom,
						ViewportW: innerWidth, Viewport: innerHeight,
						ModalScrollW: m.querySelector(".modal__body").scrollWidth,
						ModalW: m.querySelector(".modal__body").clientWidth, Outside: outside};
				})()`, &got),
			)
			assert.GreaterOrEqual(t, got.ModalLeft, 0.0, "%dpx: chooser is on screen", width)
			assert.LessOrEqual(t, got.ModalRight, got.ViewportW, "%dpx: chooser is on screen", width)
			assert.LessOrEqual(t, got.ModalBottom, got.Viewport, "%dpx: chooser fits the viewport height", width)
			assert.LessOrEqual(t, got.ModalScrollW, got.ModalW, "%dpx: no horizontal overflow inside the chooser", width)
			assert.Empty(t, got.Outside, "%dpx: every control is inside the chooser", width)
		}
		assertNoUncaughtErrors(t, f.BrowserErrors())
	})
}

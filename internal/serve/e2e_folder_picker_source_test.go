package serve_test

// #529 follow-up: the custom-source editor's `directory.path` lives in a YAML
// textarea, not an input, so its folder chooser is an "Insert folder path…"
// button under the editor: it inserts the chosen absolute path at the caret
// (or over the selection) and gives the caret back to the textarea. No YAML
// is parsed. Assertions are on the saved source file on disk.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
)

const (
	sourceTextarea = `.source-editor__textarea`
	insertPathSel  = `[data-action="insert-folder-path"]`
	// pathLinePrefix is the YAML a user has typed when they reach for the
	// button: an empty `path: ` line, with a newline after it.
	pathLinePrefix = "id: my-mods\nname: My Mods\ntype: directory\ndirectory:\n  path: "
)

// caretAt puts the textarea's caret (or, with end > start, its selection) at
// the given offsets after focusing it.
func caretAt(start, end int) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(
		`(() => { const t = document.querySelector(%q); t.focus(); t.setSelectionRange(%d, %d); })()`,
		sourceTextarea, start, end), nil)
}

// savedSourcePath is the `path:` line of the saved source file.
func savedSourcePath(t *testing.T, f e2eFixture, id string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(app.SourcesDir(f.Svc.ConfigDir()), id+".yaml"))
	require.NoError(t, err)
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "path:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no path: line in %s", data)
	return ""
}

func sourceEditorState(out *struct {
	Value  string `json:"value"`
	Active string `json:"active"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`(() => {
		const t = document.querySelector(%q);
		return {value: t.value, active: document.activeElement === t ? "textarea" : (document.activeElement?.dataset.action ?? document.activeElement?.tagName ?? ""),
			start: t.selectionStart, end: t.selectionEnd};
	})()`, sourceTextarea), out)
}

// TestE2E_SourceEditor_InsertFolderPath: insert into an empty `path: ` line,
// validate, save, and find the folder in the saved file; the caret lands
// right after the inserted text.
func TestE2E_SourceEditor_InsertFolderPath(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))
	root := folderTree(t)
	chosen := filepath.Join(root, "beta")

	var st struct {
		Value  string `json:"value"`
		Active string `json:"active"`
		Start  int    `json:"start"`
		End    int    `json:"end"`
	}
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("sources")),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="new-source"]`),
		chromedp.WaitVisible(`[data-testid="source-editor"]`, chromedp.ByQuery),
		chromedp.SetValue(sourceTextarea, pathLinePrefix+"\n", chromedp.ByQuery),
		caretAt(len(pathLinePrefix), len(pathLinePrefix)),
		clickWhenSettled(insertPathSel),
		pickerReady(),
		// Nothing selected: it opens at home, not at anything in the YAML.
		typeInto(pickerPathSel, chosen),
		chromedp.KeyEvent(kb.Enter),
		pickerPathIs(chosen),
		clickWhenSettled(modalSel+` [data-action="folder-choose"]`),
		waitGone(pickerSel),
		pollUntil(fmt.Sprintf(`document.querySelector(%q).value.includes(%q)`, sourceTextarea, chosen)),
		pollUntil(fmt.Sprintf(`document.activeElement === document.querySelector(%q)`, sourceTextarea)),
		sourceEditorState(&st),
	)
	assert.Equal(t, pathLinePrefix+chosen+"\n", st.Value, "the path is inserted at the caret, nothing else touched")
	assert.Equal(t, "textarea", st.Active, "focus is back in the textarea")
	assert.Equal(t, len(pathLinePrefix+chosen), st.Start, "the caret is after the inserted text")
	assert.Equal(t, st.Start, st.End)

	f.runInBrowser(t,
		clickWhenSettled(`[data-action="validate-source"]`),
		chromedp.WaitVisible(`.source-editor__report .plan__note`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="save-source"]`),
		chromedp.WaitVisible(`tr[data-source="my-mods"]`, chromedp.ByQuery),
	)
	assert.Equal(t, chosen, savedSourcePath(t, f, "my-mods"), "the saved source's directory.path")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SourceEditor_InsertReplacesTheSelection: a selected absolute path
// is where the chooser opens (its nearest folder, here), and the chosen
// folder replaces exactly the selection.
func TestE2E_SourceEditor_InsertReplacesTheSelection(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))
	root := folderTree(t)
	selected := filepath.Join(root, "alpha", "not-yet")
	text := pathLinePrefix + selected + "\n"
	chosen := filepath.Join(root, "beta")

	var st struct {
		Value  string `json:"value"`
		Active string `json:"active"`
		Start  int    `json:"start"`
		End    int    `json:"end"`
	}
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("sources")),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="new-source"]`),
		chromedp.WaitVisible(`[data-testid="source-editor"]`, chromedp.ByQuery),
		chromedp.SetValue(sourceTextarea, text, chromedp.ByQuery),
		caretAt(len(pathLinePrefix), len(pathLinePrefix)+len(selected)),
		clickWhenSettled(insertPathSel),
		pickerReady(),
		// The selection was a folder that does not exist: it opens at the
		// nearest one that does.
		pickerPathIs(filepath.Join(root, "alpha")),
		typeInto(pickerPathSel, chosen),
		chromedp.KeyEvent(kb.Enter),
		pickerPathIs(chosen),
		clickWhenSettled(modalSel+` [data-action="folder-choose"]`),
		waitGone(pickerSel),
		pollUntil(fmt.Sprintf(`document.activeElement === document.querySelector(%q)`, sourceTextarea)),
		sourceEditorState(&st),
	)
	assert.Equal(t, pathLinePrefix+chosen+"\n", st.Value)
	assert.Equal(t, len(pathLinePrefix+chosen), st.Start)
	assert.Equal(t, st.Start, st.End)

	// Cancel changes nothing, and focus goes back to the button that opened
	// the chooser, like every Browse….
	f.runInBrowser(t,
		caretAt(0, 2),
		clickWhenSettled(insertPathSel),
		pickerReady(),
		clickWhenSettled(modalSel+` [data-action="folder-cancel"]`),
		waitGone(pickerSel),
		pollUntil(fmt.Sprintf(`document.activeElement === document.querySelector(%q)`, insertPathSel)),
		sourceEditorState(&st),
	)
	assert.Equal(t, pathLinePrefix+chosen+"\n", st.Value, "Cancel inserts nothing")
	assert.Equal(t, "insert-folder-path", st.Active)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SourceEditor_InsertFolderPathKeyboardOnly: Tab from the textarea
// reaches the button; Enter opens the chooser; arrows, Enter and Tab choose;
// the caret is back in the textarea after the inserted text, and typing goes
// on from there.
func TestE2E_SourceEditor_InsertFolderPathKeyboardOnly(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))
	root := folderTree(t)

	var st struct {
		Value  string `json:"value"`
		Active string `json:"active"`
		Start  int    `json:"start"`
		End    int    `json:"end"`
	}
	var onButton string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("sources")),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="new-source"]`),
		chromedp.WaitVisible(`[data-testid="source-editor"]`, chromedp.ByQuery),
		chromedp.SetValue(sourceTextarea, pathLinePrefix+root+"\n", chromedp.ByQuery),
		// Select the existing root path, so the chooser opens there.
		caretAt(len(pathLinePrefix), len(pathLinePrefix)+len(root)),
		chromedp.KeyEvent(kb.Tab),
		chromedp.Evaluate(`document.activeElement.dataset.action ?? ""`, &onButton),
	)
	require.Equal(t, "insert-folder-path", onButton, "the button follows the textarea in tab order")
	f.runInBrowser(t,
		chromedp.KeyEvent(kb.Enter),
		pickerReady(),
		pickerPathIs(root),
		settleEffects(),
		chromedp.KeyEvent(kb.ArrowDown), // alpha -> beta
		chromedp.KeyEvent(kb.Enter),
		pickerPathIs(filepath.Join(root, "beta")),
		chromedp.KeyEvent(kb.Tab), // Cancel
		chromedp.KeyEvent(kb.Tab), // Choose
		chromedp.KeyEvent(kb.Enter),
		waitGone(pickerSel),
		pollUntil(fmt.Sprintf(`document.activeElement === document.querySelector(%q)`, sourceTextarea)),
		// Typing continues from the caret.
		chromedp.KeyEvent("#x"),
		sourceEditorState(&st),
	)
	chosen := filepath.Join(root, "beta")
	assert.Equal(t, pathLinePrefix+chosen+"#x\n", st.Value, "keys typed next land right after the inserted path")
	assert.Equal(t, "textarea", st.Active)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SourceEditor_InsertButtonLayoutInEveryFace: the button sits under
// the editor, inside it, at the control height, and the editor's footprint
// holds in every face - the stress pass.
func TestE2E_SourceEditor_InsertButtonLayoutInEveryFace(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		for _, width := range []int{1280, 720} {
			var got struct {
				ButtonH, ButtonTop, ButtonLeft, ButtonRight float64
				FrameBottom, EditorLeft, EditorRight        float64
				Next                                        float64
			}
			f.runInBrowser(t,
				chromedp.EmulateViewport(int64(width), 900),
				chromedp.Navigate(f.SetupPath("sources")),
				chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
				faceInEffect(face),
				clickWhenSettled(`[data-action="new-source"]`),
				chromedp.WaitVisible(`[data-testid="source-editor"]`, chromedp.ByQuery),
				chromedp.Evaluate(fmt.Sprintf(`(() => {
					const ed = document.querySelector('[data-testid="source-editor"]');
					const b = document.querySelector(%q).getBoundingClientRect();
					const frame = ed.querySelector(".source-editor__frame").getBoundingClientRect();
					const e = ed.getBoundingClientRect();
					const probe = ed.querySelector('input[type="checkbox"]').getBoundingClientRect();
					return {ButtonH: b.height, ButtonTop: b.top, ButtonLeft: b.left, ButtonRight: b.right,
						FrameBottom: frame.bottom, EditorLeft: e.left, EditorRight: e.right, Next: probe.top};
				})()`, insertPathSel), &got),
			)
			assert.InDelta(t, 32, got.ButtonH, 0.5, "%dpx: the button is control height (one line)", width)
			assert.GreaterOrEqual(t, got.ButtonTop, got.FrameBottom, "%dpx: the button is under the editor", width)
			assert.GreaterOrEqual(t, got.ButtonLeft, got.EditorLeft-0.5, "%dpx", width)
			assert.LessOrEqual(t, got.ButtonRight, got.EditorRight+0.5, "%dpx: inside the editor", width)
			assert.LessOrEqual(t, got.ButtonTop+got.ButtonH, got.Next+0.5, "%dpx: clear of the probe checkbox below", width)
		}
		assertNoUncaughtErrors(t, f.BrowserErrors())
	})
}

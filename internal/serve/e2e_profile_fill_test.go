package serve_test

// #608: filling a collection profile's pending mods from the browser - the
// archives a free account downloaded by hand, dropped (here: picked) on the
// fill drop zone, which uploads only the files of a pending file's size,
// plans the fill over them, and installs the matches. Two ways in: the
// finished collection import's entry in the activity tray, and the
// profile's "Fill from downloads…" in the profiles modal.

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eFillFixture is the NexusMods collection fixture whose recipe records
// each file's md5 and size, with Ten's and Eleven's archives on disk where
// a browser can pick them, and a file no pending mod could be.
type e2eFillFixture struct {
	e2eFixture
	dir, ten, eleven, junk string
	tenBytes, elevenBytes  []byte
}

func newE2EFillFixture(t *testing.T) e2eFillFixture {
	t.Helper()
	dir := t.TempDir()
	// The folder chooser opens at the downloads folder.
	t.Setenv("XDG_DOWNLOAD_DIR", dir)
	f := newE2ENexusCollectionFixture(t)
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, data, 0o644))
		return p
	}
	ten := e2eZipWith("ten.esp", "ten")
	eleven := e2eZipWith("eleven.esp", "eleven, a little longer")
	sum := func(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }
	recipe := fmt.Sprintf(`{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact", "MD5": %q, "SizeBytes": %d},
	{"Name": "Eleven", "Version": "1.1", "SourceType": "nexus", "ModID": "11", "FileID": "110", "UpdatePolicy": "latest", "MD5": %q, "SizeBytes": %d}]}`,
		sum(ten), len(ten), sum(eleven), len(eleven))
	seedE2ENexusRecipe(t, f, "abcdef", recipe)
	return e2eFillFixture{e2eFixture: f, dir: dir, tenBytes: ten, elevenBytes: eleven,
		ten: write("Ten-10-1-0.zip", ten), eleven: write("Eleven-11-1-1.zip", eleven),
		junk: write("notes.txt", []byte("not a mod"))}
}

// seedFillProfile saves "Coll", the profile a free account's import of the
// collection leaves: both mods pinned, neither installed.
func (f e2eFillFixture) seedFillProfile(t *testing.T) {
	t.Helper()
	require.NoError(t, config.SaveProfile(f.Svc.ConfigDir(), &domain.Profile{
		Name: "Coll", GameID: f.Game.ID,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "10", Version: "1.0", FileIDs: []string{"100"}, Locked: true},
			{SourceID: "nexusmods", ModID: "11", Version: "1.1", FileIDs: []string{"110"}},
		},
		Collection: &domain.CollectionInfo{ID: "nexusmods:abcdef", Origin: &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abcdef", Revision: 3}},
	}))
}

const fillInput = `[data-testid="fill-drop-input"]`

func TestE2E_ProfileFill_ProfilesModalInstallsTheDroppedFiles(t *testing.T) {
	f := newE2EFillFixture(t)
	f.seedFillProfile(t)

	var plan string
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
		chromedp.WaitVisible(`[data-action="fill-profile"][data-profile="Coll"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="fill-profile"][data-profile="Coll"]`),
		chromedp.WaitReady(fillInput, chromedp.ByQuery),
		chromedp.SetUploadFiles(fillInput, []string{f.ten, f.junk}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_fill"] [data-testid="fill-matched"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_fill"]`, &plan),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)
	assert.Contains(t, plan, "Ten ← Ten-10-1-0.zip")
	assert.Contains(t, plan, "Still pending (1)")
	assert.Contains(t, plan, "Eleven: not among these files")
	assert.Contains(t, plan, "notes.txt: not uploaded", "a file no pending file could be is never sent")

	require.Eventually(t, func() bool {
		_, err := f.Svc.GetInstalledMod(t.Context(), "nexusmods", "10", f.Game.ID, "Coll")
		return err == nil
	}, 15*time.Second, 50*time.Millisecond, "the matched archive is installed into the profile")
	_, err := os.Stat(f.ten)
	assert.NoError(t, err, "the picked file is the user's: never moved")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ProfileFill_TheImportResultOffersTheDropZone(t *testing.T) {
	f := newE2EFillFixture(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("abcdef"),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitReady(`.tray__row [data-testid="fill-drop"][data-profile="Three Mods"]`, chromedp.ByQuery),
		// A real drop: the files arrive on the zone's drop event.
		chromedp.Evaluate(dropFilesJS(`.tray__row [data-testid="fill-drop"]`, map[string][]byte{
			"Ten-10-1-0.zip": f.tenBytes, "Eleven-11-1-1.zip": f.elevenBytes,
		}), nil),
		chromedp.WaitVisible(`.modal[data-kind="profile_fill"] [data-testid="fill-matched"]`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)

	require.Eventually(t, func() bool {
		rows, err := f.Svc.GetInstalledMods(t.Context(), f.Game.ID, "Three Mods")
		return err == nil && len(rows) == 2
	}, 15*time.Second, 50*time.Millisecond, "both downloads installed")
	row, err := f.Svc.GetInstalledMod(t.Context(), "nexusmods", "11", f.Game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, row.UpdatePolicy, "the curator's latest item follows its newest file")
	assert.Empty(t, f.BrowserErrors())
}

// dropFilesJS dispatches a drop of files (name -> bytes) on the element sel
// names, as a drag from the desktop delivers them.
func dropFilesJS(sel string, files map[string][]byte) string {
	js := "(() => { const dt = new DataTransfer();\n"
	for name, data := range files {
		js += fmt.Sprintf("dt.items.add(new File([Uint8Array.from(atob(%q), (c) => c.charCodeAt(0))], %q));\n",
			base64.StdEncoding.EncodeToString(data), name)
	}
	js += fmt.Sprintf("document.querySelector(%q).dispatchEvent(new DragEvent(\"drop\", {dataTransfer: dt, bubbles: true, cancelable: true})); })()", sel)
	return js
}

// #608 review: the folder mode - choose the folder with the folder chooser,
// which opens at the downloads folder, and the server scans it; nothing is
// uploaded.
func TestE2E_ProfileFill_ChooseAFolderInstallsWhatItHolds(t *testing.T) {
	f := newE2EFillFixture(t)
	f.seedFillProfile(t)

	var plan string
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
		chromedp.WaitVisible(`[data-action="fill-profile"][data-profile="Coll"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="fill-profile"][data-profile="Coll"]`),
		chromedp.WaitVisible(`[data-testid="fill-drop"][data-downloads]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="fill-folder"]`),
		chromedp.WaitVisible(`[data-testid="folder-picker"]`, chromedp.ByQuery),
		pollUntil(fmt.Sprintf(`document.querySelector('[data-testid="folder-picker"]')?.textContent.includes(%q)`, filepath.Base(f.dir))),
		clickWhenSettled(`[data-action="folder-choose"]`),
		chromedp.WaitVisible(`.modal[data-kind="profile_fill"] [data-testid="fill-matched"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_fill"]`, &plan),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)
	assert.Contains(t, plan, "Ten ← Ten-10-1-0.zip")
	assert.Contains(t, plan, "Eleven ← Eleven-11-1-1.zip")
	assert.Contains(t, plan, "notes.txt: matches no pending file")

	require.Eventually(t, func() bool {
		rows, err := f.Svc.GetInstalledMods(t.Context(), f.Game.ID, "Coll")
		return err == nil && len(rows) == 2
	}, 15*time.Second, 50*time.Millisecond, "both files in the folder installed")
	assert.Empty(t, f.BrowserErrors())
}

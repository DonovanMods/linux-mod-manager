package serve_test

// #610 review B1 in a browser: an older profile export naming a patch this
// machine keeps for another file shows the file lists and patches section
// and the "Apply these patches" box, the same box (the same option) as for
// bundled code; ticked, the import saves the profile.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_ProfileImport_OlderExportPatchesAreGated(t *testing.T) {
	f := newE2EFixture(t)
	patch := append([]byte("BSDIFF40"), make([]byte, 24)...)
	sum := sha256.Sum256(patch)
	sha := hex.EncodeToString(sum[:])
	store := filepath.Join(f.Svc.GlobalCacheDir(), f.Game.ID, "collections", "patches")
	require.NoError(t, os.MkdirAll(store, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(store, sha+".diff"), patch, 0o644))
	doc := fmt.Sprintf("name: patched\ngame_id: %s\nmods:\n  - source_id: fake\n    mod_id: x\n    version: \"1\"\n    file_ids: [\"f\"]\n"+
		"    file_list:\n      \"f\": [{path: data/a.json, md5: 0cc175b9c0f1b6a831c399e269772661}]\n"+
		"    patches:\n      \"f\": [{path: data/config.json, crc32: CABDDFC4, sha256: %s}]\n", f.Game.ID, sha)
	importPath := filepath.Join(t.TempDir(), "patched.yaml")
	require.NoError(t, os.WriteFile(importPath, []byte(doc), 0o644))

	var inputs, gate string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openProfilesModal(),
		chromedp.SetUploadFiles(`.profiles-import input[type="file"]`, []string{importPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] [data-testid="import-restricted-patches"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-tree-inputs"]`, &inputs),
		textContent(`[data-testid="import-restricted-patches"]`, &gate),
		chromedp.Click(`[data-testid="import-patch-code"]`, chromedp.ByQuery),
		settleEffects(),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		waitGone(`.modal[data-kind="profile_import"]`),
	)
	assert.Contains(t, inputs, "installs exactly 1 listed file")
	assert.Contains(t, inputs, "patches data/config.json (baseline CRC32 CABDDFC4)")
	assert.Contains(t, gate, "another file")
	assert.Contains(t, gate, "fake:x")
	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "patched")
		return err == nil && len(p.Mods) == 1 && len(p.Mods[0].Patches["f"]) == 1
	}, 10*time.Second, 50*time.Millisecond, "ticked, the import saves the profile with its patch")
	assert.Empty(t, f.BrowserErrors())
}

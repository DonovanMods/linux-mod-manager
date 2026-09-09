package steamworkshop_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "acf", name))
	require.NoError(t, err)
	return string(data)
}

func TestParseAppWorkshop(t *testing.T) {
	t.Run("populated ACF yields every installed item, file-id sorted", func(t *testing.T) {
		aw, err := steamworkshop.ParseAppWorkshop(readFixture(t, "appworkshop_1133870.acf"))
		require.NoError(t, err)

		assert.Equal(t, "1133870", aw.AppID)
		assert.Equal(t, int64(960823296), aw.SizeOnDisk)
		require.Len(t, aw.Items, 3)

		// Sorted by file id so a scan is deterministic across ACF layouts.
		assert.Equal(t, []string{"2900001111", "3512001122", "3617086610"},
			[]string{aw.Items[0].FileID, aw.Items[1].FileID, aw.Items[2].FileID})

		newest := aw.Items[2]
		assert.Equal(t, int64(572330), newest.SizeOnDisk)
		assert.Equal(t, int64(1764767935), newest.TimeUpdated)
		assert.Equal(t, "7987119735124793734", newest.Manifest)
	})

	t.Run("empty-stub ACF parses cleanly with no items", func(t *testing.T) {
		aw, err := steamworkshop.ParseAppWorkshop(readFixture(t, "appworkshop_294100.acf"))
		require.NoError(t, err)
		assert.Equal(t, "294100", aw.AppID)
		assert.Empty(t, aw.Items)
	})

	t.Run("item without a manifest keeps its timestamp", func(t *testing.T) {
		aw, err := steamworkshop.ParseAppWorkshop(readFixture(t, "appworkshop_nomanifest.acf"))
		require.NoError(t, err)
		require.Len(t, aw.Items, 1)
		assert.Equal(t, "1000000001", aw.Items[0].FileID)
		assert.Empty(t, aw.Items[0].Manifest)
		assert.Equal(t, int64(1712345678), aw.Items[0].TimeUpdated)
	})

	t.Run("malformed ACF is an error, never a silent empty scan", func(t *testing.T) {
		_, err := steamworkshop.ParseAppWorkshop(readFixture(t, "appworkshop_malformed.acf"))
		require.Error(t, err)
	})

	t.Run("ACF with no AppWorkshop block is an error", func(t *testing.T) {
		_, err := steamworkshop.ParseAppWorkshop(`"AppState"` + "\n{\n\t\"appid\"\t\"1\"\n}\n")
		require.Error(t, err)
	})
}

// writeLibrary lays out a fake Steam library root with a workshop ACF and
// the content directories that ACF claims, returning the library path.
func writeLibrary(t *testing.T, root, fixture string, contentDirs map[string][]string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "steamapps", "workshop"), 0o755))
	data := readFixture(t, fixture)
	appID := steamworkshopAppIDOf(t, data)
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "steamapps", "workshop", "appworkshop_"+appID+".acf"), []byte(data), 0o644))
	for fileID, files := range contentDirs {
		dir := filepath.Join(root, "steamapps", "workshop", "content", appID, fileID)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		for _, f := range files {
			require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644))
		}
	}
	return root
}

func steamworkshopAppIDOf(t *testing.T, acf string) string {
	t.Helper()
	aw, err := steamworkshop.ParseAppWorkshop(acf)
	if err != nil {
		// The malformed fixture is never laid out as a library.
		t.Fatalf("fixture is not parseable: %v", err)
	}
	return aw.AppID
}

func TestScanLibraries(t *testing.T) {
	t.Run("collects items across every library and resolves content paths", func(t *testing.T) {
		libA := writeLibrary(t, t.TempDir(), "appworkshop_1133870.acf", map[string][]string{
			"3617086610": {"mod.pak"},
			"3512001122": {"data.bin"},
		})
		// A second library holding the same app's ACF is the multi-library
		// case: nothing is lost and nothing is double-counted.
		libB := writeLibrary(t, t.TempDir(), "appworkshop_nomanifest.acf", map[string][]string{
			"1000000001": {"about.xml"},
		})

		got, err := steamworkshop.ScanLibraries([]string{libA, libB}, "1133870")
		require.NoError(t, err)

		assert.Equal(t, []string{libA}, got.Libraries, "only libraries holding this app's ACF are reported")
		require.Len(t, got.Items, 3)
		assert.Equal(t, filepath.Join(libA, "steamapps", "workshop", "content", "1133870", "3617086610"),
			got.Items[2].Path)
		assert.Empty(t, got.Warnings)
	})

	t.Run("an item the ACF claims but disk does not have still scans, with its real path", func(t *testing.T) {
		lib := writeLibrary(t, t.TempDir(), "appworkshop_1133870.acf", map[string][]string{
			"3617086610": {"mod.pak"},
		})
		got, err := steamworkshop.ScanLibraries([]string{lib}, "1133870")
		require.NoError(t, err)
		require.Len(t, got.Items, 3, "a missing content dir is verify's finding, not a scan omission")
	})

	t.Run("empty-stub ACF contributes no items but names its library", func(t *testing.T) {
		lib := writeLibrary(t, t.TempDir(), "appworkshop_294100.acf", nil)
		got, err := steamworkshop.ScanLibraries([]string{lib}, "294100")
		require.NoError(t, err)
		assert.Equal(t, []string{lib}, got.Libraries)
		assert.Empty(t, got.Items)
	})

	t.Run("a malformed ACF warns and does not fail the scan", func(t *testing.T) {
		lib := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(lib, "steamapps", "workshop"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(lib, "steamapps", "workshop", "appworkshop_431960.acf"),
			[]byte(readFixture(t, "appworkshop_malformed.acf")), 0o644))

		got, err := steamworkshop.ScanLibraries([]string{lib}, "431960")
		require.NoError(t, err)
		assert.Empty(t, got.Items)
		require.Len(t, got.Warnings, 1)
		assert.Contains(t, got.Warnings[0], "appworkshop_431960.acf")
	})

	t.Run("no ACF anywhere is an empty scan, not an error", func(t *testing.T) {
		got, err := steamworkshop.ScanLibraries([]string{t.TempDir()}, "1133870")
		require.NoError(t, err)
		assert.Empty(t, got.Items)
		assert.Empty(t, got.Libraries)
		assert.Empty(t, got.Warnings)
	})

	t.Run("a non-numeric app id is refused before any filesystem read", func(t *testing.T) {
		_, err := steamworkshop.ScanLibraries([]string{t.TempDir()}, "../../etc")
		require.Error(t, err)
	})
}

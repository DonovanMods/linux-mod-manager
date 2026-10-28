package custom

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCustomSources_ReportUpdatedAt is #433's per-source table for the
// three custom-source kinds: a manifest's updated_at, an api mapping's
// updated_at, and - for a directory source, which has no publisher to ask -
// the entry's modification time. A manifest or api mod without a date keeps
// the zero time.
func TestCustomSources_ReportUpdatedAt(t *testing.T) {
	ctx := context.Background()

	t.Run("manifest", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mods.yaml")
		require.NoError(t, os.WriteFile(path, []byte(validManifestYAML), 0o644))
		m, err := NewManifest(manifestDef(path))
		require.NoError(t, err)

		found, err := m.Search(ctx, source.SearchQuery{})
		require.NoError(t, err)
		dates := map[string]time.Time{}
		for _, mod := range found.Mods {
			dates[mod.ID] = mod.UpdatedAt
		}
		assert.True(t, dates["cool-mod"].Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)), "dated: %v", dates["cool-mod"])
		assert.True(t, dates["other-mod"].IsZero(), "undated: %v", dates["other-mod"])
	})

	t.Run("api mapping", func(t *testing.T) {
		doc := map[string]any{"id": "a", "name": "A", "updated": "2026-02-03T04:05:06Z"}
		mod, err := mapMod(doc, map[string]string{"id": "id", "name": "name", "updated_at": "updated"}, "api")
		require.NoError(t, err)
		assert.True(t, mod.UpdatedAt.Equal(time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)), "dated: %v", mod.UpdatedAt)

		mod, err = mapMod(map[string]any{"id": "b", "name": "B"}, map[string]string{"id": "id", "name": "name", "updated_at": "updated"}, "api")
		require.NoError(t, err)
		assert.True(t, mod.UpdatedAt.IsZero(), "undated: %v", mod.UpdatedAt)
	})

	t.Run("directory", func(t *testing.T) {
		d := newTestDirectory(t)
		stamp := time.Date(2025, 5, 6, 7, 8, 9, 0, time.UTC)
		require.NoError(t, os.Chtimes(filepath.Join(d.path, "archived-mod-2.0.zip"), stamp, stamp))
		require.NoError(t, os.Chtimes(filepath.Join(d.path, "PlainMod-0.5"), stamp, stamp))

		for _, id := range []string{"archived-mod-2.0", "PlainMod-0.5"} {
			mod, err := d.GetMod(ctx, "", id)
			require.NoError(t, err)
			assert.True(t, mod.UpdatedAt.Equal(stamp), "%s: %v", id, mod.UpdatedAt)

			files, err := d.GetModFiles(ctx, mod)
			require.NoError(t, err)
			require.Len(t, files, 1)
			assert.True(t, files[0].UploadedAt.Equal(stamp), "%s file: %v", id, files[0].UploadedAt)
		}
	})
}

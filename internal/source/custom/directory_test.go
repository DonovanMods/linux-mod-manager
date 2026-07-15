package custom

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testModInfo = `<?xml version="1.0" encoding="UTF-8" ?>
<xml>
	<Name value="BiggerBackpack"/>
	<DisplayName value="Bigger Backpack"/>
	<Version value="1.2.0"/>
	<Description value="Carry more stuff"/>
	<Author value="Donovan"/>
</xml>`

// newTestDirectory builds a source over a temp dir containing:
//
//	BiggerBackpack/        (with ModInfo.xml)
//	PlainMod-0.5/          (no metadata; version from dirname)
//	archived-mod-2.0.zip   (archive mod)
//	README.md              (ignored: not a dir or archive)
//	.git/                  (ignored: dot-prefixed directory)
//	.hidden-mod.zip        (ignored: dot-prefixed file, even though it's a .zip)
func newTestDirectory(t *testing.T) *Directory {
	t.Helper()
	root := t.TempDir()

	bb := filepath.Join(root, "BiggerBackpack")
	require.NoError(t, os.MkdirAll(bb, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bb, "ModInfo.xml"), []byte(testModInfo), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bb, "readme.txt"), []byte("hi"), 0644))

	require.NoError(t, os.MkdirAll(filepath.Join(root, "PlainMod-0.5"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "archived-mod-2.0.zip"), []byte("zipbytes"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("ignored"), 0644))

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"), []byte("ignored"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".hidden-mod.zip"), []byte("zipbytes"), 0644))

	def := SourceDefinition{
		ID:        "my-mods",
		Name:      "My Mods",
		Type:      TypeDirectory,
		Directory: &DirectoryConfig{Path: root},
	}
	d, err := NewDirectory(def)
	require.NoError(t, err)
	return d
}

func TestNewDirectoryValidation(t *testing.T) {
	def := SourceDefinition{
		ID:        "x",
		Name:      "X",
		Type:      TypeDirectory,
		Directory: &DirectoryConfig{Path: filepath.Join(t.TempDir(), "missing")},
	}
	_, err := NewDirectory(def)
	assert.ErrorContains(t, err, "missing")
}

func TestDirectoryIdentityAndCapabilities(t *testing.T) {
	d := newTestDirectory(t)
	assert.Equal(t, "my-mods", d.ID())
	assert.Equal(t, "My Mods", d.Name())
	assert.Equal(t, source.Capabilities{Search: true, Updates: true}, d.Capabilities())
	assert.Empty(t, d.AuthURL())

	_, err := d.ExchangeToken(context.Background(), "code")
	assert.True(t, errors.Is(err, source.ErrNotSupported))

	_, err = d.GetDependencies(context.Background(), nil)
	assert.True(t, errors.Is(err, source.ErrNotSupported))
}

func TestDirectorySearch(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	t.Run("empty query returns all mods", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{GameID: "anything"})
		require.NoError(t, err)
		assert.Equal(t, 3, res.TotalCount)
		require.Len(t, res.Mods, 3)
	})

	t.Run("metadata takes priority over dirname", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Query: "backpack"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 1)
		m := res.Mods[0]
		assert.Equal(t, "BiggerBackpack", m.ID)
		assert.Equal(t, "Bigger Backpack", m.Name)
		assert.Equal(t, "1.2.0", m.Version)
		assert.Equal(t, "Carry more stuff", m.Summary)
		assert.Equal(t, "Donovan", m.Author)
		assert.Equal(t, "my-mods", m.SourceID)
	})

	t.Run("fallback parses version from name", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Query: "plainmod"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 1)
		assert.Equal(t, "PlainMod-0.5", res.Mods[0].ID)
		assert.Equal(t, "PlainMod", res.Mods[0].Name)
		assert.Equal(t, "0.5", res.Mods[0].Version)
	})

	t.Run("summary matches rank after name matches", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Query: "stuff"}) // only in summary
		require.NoError(t, err)
		require.Len(t, res.Mods, 1)
		assert.Equal(t, "BiggerBackpack", res.Mods[0].ID)
	})

	t.Run("pagination", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Page: 0, PageSize: 2})
		require.NoError(t, err)
		assert.Len(t, res.Mods, 2)
		assert.Equal(t, 3, res.TotalCount)

		res, err = d.Search(ctx, source.SearchQuery{Page: 1, PageSize: 2})
		require.NoError(t, err)
		assert.Len(t, res.Mods, 1)
	})

	t.Run("negative page clamps to the first page instead of panicking", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Page: -1, PageSize: 2})
		require.NoError(t, err)
		assert.Len(t, res.Mods, 2)
		assert.Equal(t, 3, res.TotalCount)
	})

	t.Run("dot-prefixed entries are skipped", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{})
		require.NoError(t, err)
		assert.Equal(t, 3, res.TotalCount, "hidden .git dir and .hidden-mod.zip must not be listed as mods")
		for _, m := range res.Mods {
			assert.NotEqual(t, "config", m.ID)
			assert.NotEqual(t, ".git", m.ID)
			assert.NotEqual(t, ".hidden-mod", m.ID)
		}
	})
}

func TestDirectoryGetMod(t *testing.T) {
	d := newTestDirectory(t)

	mod, err := d.GetMod(context.Background(), "ignored", "BiggerBackpack")
	require.NoError(t, err)
	assert.Equal(t, "Bigger Backpack", mod.Name)

	_, err = d.GetMod(context.Background(), "ignored", "nope")
	assert.ErrorContains(t, err, "not found")
}

func TestDirectoryFilesAndDownloadURL(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	mod, err := d.GetMod(ctx, "", "BiggerBackpack")
	require.NoError(t, err)

	files, err := d.GetModFiles(ctx, mod)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "main", files[0].ID)
	assert.Equal(t, "BiggerBackpack", files[0].FileName)
	assert.True(t, files[0].IsPrimary)

	url, err := d.GetDownloadURL(ctx, mod, files[0].ID)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(url, "file://"))
	assert.True(t, strings.HasSuffix(url, "/BiggerBackpack"))

	// Archive mods point at the archive file.
	zipMod, err := d.GetMod(ctx, "", "archived-mod-2.0")
	require.NoError(t, err)
	zipFiles, err := d.GetModFiles(ctx, zipMod)
	require.NoError(t, err)
	require.Len(t, zipFiles, 1)
	assert.Equal(t, "archived-mod-2.0.zip", zipFiles[0].FileName)
	assert.Equal(t, int64(8), zipFiles[0].Size) // len("zipbytes")
}

func TestDirectoryCheckUpdates(t *testing.T) {
	d := newTestDirectory(t) // BiggerBackpack is at 1.2.0

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "BiggerBackpack", SourceID: "my-mods", Name: "Bigger Backpack", Version: "1.0.0"}},
		{Mod: domain.Mod{ID: "PlainMod-0.5", SourceID: "my-mods", Name: "PlainMod", Version: "0.5"}},
		{Mod: domain.Mod{ID: "Removed", SourceID: "my-mods", Name: "Removed", Version: "1.0"}},
	}

	updates, err := d.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "BiggerBackpack", updates[0].InstalledMod.ID)
	assert.Equal(t, "1.2.0", updates[0].NewVersion)
}

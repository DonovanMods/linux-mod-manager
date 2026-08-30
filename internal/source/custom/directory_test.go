package custom

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
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

func TestDirectory_TypeLabel(t *testing.T) {
	var _ source.TypeLabeler = (*Directory)(nil)

	d := newTestDirectory(t)
	assert.Equal(t, "directory", d.TypeLabel())
}

func TestDirectorySearch(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	t.Run("empty query returns all mods", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{GameID: "anything"})
		require.NoError(t, err)
		assert.Equal(t, 3, res.TotalCount)
		require.Len(t, res.Mods, 3)
		assert.Equal(t, 20, res.PageSize, "PageSize must default to 20 when the query leaves it unset")
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

	t.Run("description is not aliased to summary (#235)", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Query: "backpack"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 1)
		assert.Equal(t, "Carry more stuff", res.Mods[0].Summary)
		assert.Empty(t, res.Mods[0].Description,
			"ModInfo metadata has no full-description field; Description must stay empty, not copy Summary")
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

	t.Run("GameID is echoed onto returned mods for identity, not used to filter", func(t *testing.T) {
		res, err := d.Search(ctx, source.SearchQuery{Query: "backpack", GameID: "7dtd"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 1)
		assert.Equal(t, "7dtd", res.Mods[0].GameID)
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

	mod, err := d.GetMod(context.Background(), "7dtd", "BiggerBackpack")
	require.NoError(t, err)
	assert.Equal(t, "Bigger Backpack", mod.Name)
	assert.Equal(t, "7dtd", mod.GameID, "GetMod must echo the gameID it was given so installs are attributed to the right game")

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

// writeTestZip builds a real zip file at path containing entryPath -> content.
func writeTestZip(t *testing.T, path, entryPath, content string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	w := zip.NewWriter(f)
	fw, err := w.Create(entryPath)
	require.NoError(t, err)
	_, err = fw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, w.Close())
}

// TestDirectoryArchiveMetadata verifies that .zip mods with an embedded
// ModInfo.xml (7 Days to Die's standard wrapper-folder layout) surface real
// metadata instead of filename-derived guesses, while archives without
// readable metadata still fall back to filename parsing.
func TestDirectoryArchiveMetadata(t *testing.T) {
	root := t.TempDir()

	// Real zip: donovan-aio.zip containing donovan-aio/ModInfo.xml.
	writeTestZip(t, filepath.Join(root, "donovan-aio.zip"), "donovan-aio/ModInfo.xml", testModInfo)

	// Not a real zip (fake bytes) - must keep falling back to filename parsing.
	require.NoError(t, os.WriteFile(filepath.Join(root, "archived-mod-2.0.zip"), []byte("zipbytes"), 0644))

	def := SourceDefinition{
		ID:        "my-mods",
		Name:      "My Mods",
		Type:      TypeDirectory,
		Directory: &DirectoryConfig{Path: root},
	}
	d, err := NewDirectory(def)
	require.NoError(t, err)
	ctx := context.Background()

	t.Run("archive with embedded ModInfo.xml surfaces metadata", func(t *testing.T) {
		mod, err := d.GetMod(ctx, "7dtd", "donovan-aio")
		require.NoError(t, err)
		assert.Equal(t, "donovan-aio", mod.ID, "mod ID stays the archive base name")
		assert.Equal(t, "Bigger Backpack", mod.Name)
		assert.Equal(t, "1.2.0", mod.Version)
		assert.Equal(t, "Carry more stuff", mod.Summary)
		assert.Equal(t, "Donovan", mod.Author)
	})

	t.Run("archive with no readable metadata falls back to filename parsing", func(t *testing.T) {
		mod, err := d.GetMod(ctx, "7dtd", "archived-mod-2.0")
		require.NoError(t, err)
		assert.Equal(t, "archived-mod-2.0", mod.ID)
		assert.Equal(t, "archived-mod", mod.Name)
		assert.Equal(t, "2.0", mod.Version)

		files, err := d.GetModFiles(ctx, mod)
		require.NoError(t, err)
		require.Len(t, files, 1)
		assert.Equal(t, int64(8), files[0].Size, `len("zipbytes")`)
	})
}

// TestDirectorySearch_FollowsSymlinkedModDir is a regression test: scan()
// classified entries via entry.IsDir(), which reflects the raw dirent type
// (ModeSymlink) and is always false for a symlink even when it points at a
// directory. A symlinked mod dir has no file extension either, so it fell
// through both branches and was silently dropped from every scan.
func TestDirectorySearch_FollowsSymlinkedModDir(t *testing.T) {
	root := t.TempDir()

	// The real directory lives outside root so scan() only ever sees it via
	// the symlink - keeps the mod count assertion below unambiguous.
	real := filepath.Join(t.TempDir(), "real-mod-dir")
	require.NoError(t, os.MkdirAll(real, 0755))

	require.NoError(t, os.Symlink(real, filepath.Join(root, "LinkedMod-1.0")))

	def := SourceDefinition{
		ID:        "linked",
		Name:      "Linked",
		Type:      TypeDirectory,
		Directory: &DirectoryConfig{Path: root},
	}
	d, err := NewDirectory(def)
	require.NoError(t, err)

	res, err := d.Search(context.Background(), source.SearchQuery{})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1, "symlinked mod directory must be scanned like any other")
	assert.Equal(t, "LinkedMod-1.0", res.Mods[0].ID)
	assert.Equal(t, "LinkedMod", res.Mods[0].Name)
	assert.Equal(t, "1.0", res.Mods[0].Version)
}

// TestDirectoryScan_SkipsDanglingSymlink locks in the one case scan() is
// allowed to skip silently: a symlink whose target no longer exists. Its
// os.Stat fails with ENOENT, which is exactly what a plain os.ReadDir-based
// classification would already have missed (the entry just wouldn't be
// there) - so it is not a scan-fatal error.
func TestDirectoryScan_SkipsDanglingSymlink(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, os.Symlink(filepath.Join(root, "missing-target"), filepath.Join(root, "Dangling-1.0")))

	def := SourceDefinition{
		ID:        "dangling",
		Name:      "Dangling",
		Type:      TypeDirectory,
		Directory: &DirectoryConfig{Path: root},
	}
	d, err := NewDirectory(def)
	require.NoError(t, err)

	res, err := d.Search(context.Background(), source.SearchQuery{})
	require.NoError(t, err)
	assert.Empty(t, res.Mods, "dangling symlink must be skipped silently, not surfaced as a mod or a scan error")
}

// TestDirectoryScan_NonENOENTStatErrorPropagates is a regression test: scan()
// must not swallow stat errors that aren't "the entry doesn't exist" (the
// never-swallow-errors rule). We force a non-ENOENT failure by symlinking to
// a target buried inside a directory with its execute/search bit removed, so
// resolving the symlink fails with EACCES rather than ENOENT.
func TestDirectoryScan_NonENOENTStatErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks, so this EACCES setup can't fail")
	}

	root := t.TempDir()

	sealed := filepath.Join(root, "sealed")
	require.NoError(t, os.MkdirAll(sealed, 0755))
	target := filepath.Join(sealed, "target-mod")
	require.NoError(t, os.MkdirAll(target, 0755))
	require.NoError(t, os.Chmod(sealed, 0000))
	t.Cleanup(func() { _ = os.Chmod(sealed, 0755) }) // restore before TempDir's own cleanup removes it

	require.NoError(t, os.Symlink(target, filepath.Join(root, "BrokenPerm-1.0")))

	def := SourceDefinition{
		ID:        "broken-perm",
		Name:      "Broken Perm",
		Type:      TypeDirectory,
		Directory: &DirectoryConfig{Path: root},
	}
	d, err := NewDirectory(def)
	require.NoError(t, err)

	_, err = d.Search(context.Background(), source.SearchQuery{})
	require.Error(t, err, "a non-ENOENT stat error must propagate, not be silently skipped")
	assert.Contains(t, err.Error(), "BrokenPerm-1.0")
	assert.True(t, errors.Is(err, os.ErrPermission), "wrapped error should preserve the underlying permission error")
}

// TestNameAndVersionFrom covers the v/V trim boundary: separators (-_ ) must
// always be trimmed, but a "v"/"V" should only be trimmed when the version
// pattern actually consumed it as a prefix (immediately adjacent to the
// version digits, e.g. "MyMod-v1.0"). A real trailing V that's part of the
// mod's own name (e.g. "ModV", "ServerV2") must survive.
func TestNameAndVersionFrom(t *testing.T) {
	tests := []struct {
		in          string
		wantName    string
		wantVersion string
	}{
		{"PlainMod-0.5", "PlainMod", "0.5"},
		{"ModV-1.0", "ModV", "1.0"},
		{"Modv-2.3", "Modv", "2.3"},
		{"MyMod-v1.0", "MyMod", "1.0"},
		{"ServerV2-1.0", "ServerV2", "1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			name, version := nameAndVersionFrom(tt.in)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantVersion, version)
		})
	}
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

package core

// #658: .7z and .rar archives extract through the system 7z with the same
// protections #656 gave its zip fallback: the listing is judged before
// anything is written (no links, no escaping or reserved names), 7z writes
// into a private directory, that tree is walked without following links
// (no symlinks, special files or hard links), and every entry moves into
// place through sanitizePath. Reseeding a staging directory from an
// existing cache entry drops a link an earlier, link-unsafe extraction
// left behind.
//
// Real 7z builds the .7z fixtures it can (symlinks, traversal). 7z's own
// format stores no hard links, and no rar tool is assumed, so a fake 7z on
// PATH stands in for a listing and an extraction that real archives of
// those kinds produce.

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// build7z writes files under a staging tree, plus a symlink per links entry
// (name -> target), and archives it as a .7z with the real 7z. -snl stores
// the symlinks as links.
func build7z(t *testing.T, files, links map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	for name, content := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	for name, target := range links {
		p := filepath.Join(src, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.Symlink(target, p))
	}
	archive := filepath.Join(dir, "mod.7z")
	cmd := exec.Command("7z", "a", "-snl", archive, ".")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building fixture .7z: %s", out)
	return archive
}

// fake7zMember is one entry a fake 7z's listing reports.
type fake7zMember struct {
	path, attrs, symlink, hardlink string
	size                           int
}

// install7zFake puts a fake 7z first on PATH. `7z l` prints a -slt listing
// of members; `7z x` runs extract, a shell fragment with $out set to the
// -o directory (absolute command paths: PATH is not trusted in it).
func install7zFake(t *testing.T, members []fake7zMember, extract string) {
	t.Helper()
	bin := t.TempDir()
	var listing strings.Builder
	listing.WriteString("7-Zip (fake)\n\n--\nPath = archive\nType = 7z\n\n----------\n")
	for _, m := range members {
		fmt.Fprintf(&listing, "Path = %s\nSize = %d\nAttributes = %s\n", m.path, m.size, m.attrs)
		if m.symlink != "" {
			fmt.Fprintf(&listing, "Symbolic Link = %s\n", m.symlink)
		}
		if m.hardlink != "" {
			fmt.Fprintf(&listing, "Hard Link = %s\n", m.hardlink)
		}
		listing.WriteString("\n")
	}
	listingPath := filepath.Join(bin, "listing.txt")
	require.NoError(t, os.WriteFile(listingPath, []byte(listing.String()), 0o644))
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
l) exec /bin/cat %q ;;
x)
  out=""
  for a in "$@"; do case "$a" in -o*) out="${a#-o}" ;; esac; done
  /bin/mkdir -p "$out" || exit 2
  %s
  ;;
esac
`, listingPath, extract)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "7z"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeArchive is an archive file of format ext ("7z" or "rar"); its bytes
// matter to nothing but format detection, which goes by the extension.
func fakeArchive(t *testing.T, ext string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mod."+ext)
	require.NoError(t, os.WriteFile(p, []byte("not really an archive"), 0o644))
	return p
}

// noPrivateDirLeft asserts the private extraction directory is gone.
func noPrivateDirLeft(t *testing.T, dest string) {
	t.Helper()
	entries, err := os.ReadDir(dest)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), cache.ReservedPrefix), "private extraction dir %s left behind", e.Name())
	}
}

func Test7z_ExtractsThroughThePrivateDirectory(t *testing.T) {
	require7z(t)
	archive := build7z(t, map[string]string{
		"MyMod/a.esp":     "a",
		"MyMod/sub/b.txt": "b",
	}, nil)
	dest := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dest, ".lmm-file-1"), nil, 0o644))

	require.NoError(t, NewExtractor().Extract(context.Background(), archive, dest))

	assert.ElementsMatch(t, []string{
		".lmm-file-1", "MyMod", "MyMod/a.esp", "MyMod/sub", "MyMod/sub/b.txt",
	}, entriesUnder(t, dest), "the members land, what dest held stays, and nothing of the extraction's own is left")
}

func Test7z_RefusesSymlinkMember(t *testing.T) {
	require7z(t)
	for name, target := range map[string]string{
		"escaping": "/etc/passwd",
		"in-tree":  "real.txt",
	} {
		t.Run(name, func(t *testing.T) {
			archive := build7z(t, map[string]string{"real.txt": "real"}, map[string]string{"link.txt": target})
			dest := filepath.Join(t.TempDir(), "out")

			err := NewExtractor().Extract(context.Background(), archive, dest)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "link.txt")
			assert.Contains(t, err.Error(), "7z member")
			assert.Empty(t, entriesUnder(t, dest), "a refused archive writes nothing")

			_, err = listArchiveMembers(context.Background(), NewExtractor(), archive)
			require.Error(t, err, "the plan-time listing refuses it too")
			assert.Contains(t, err.Error(), "link.txt")
		})
	}
}

func Test7z_RefusesTraversal(t *testing.T) {
	require7z(t)
	archive := build7z(t, map[string]string{"ok.txt": "ok", "evil.txt": "evil"}, nil)
	out, err := exec.Command("7z", "rn", archive, "evil.txt", "../evil.txt").CombinedOutput()
	require.NoError(t, err, "renaming the fixture member: %s", out)
	parent := t.TempDir()
	dest := filepath.Join(parent, "out")

	err = NewExtractor().Extract(context.Background(), archive, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal detected")
	assert.Empty(t, entriesUnder(t, dest))
	_, statErr := os.Stat(filepath.Join(parent, "evil.txt"))
	assert.True(t, os.IsNotExist(statErr))

	// The listing names members as recorded; the plan refuses the escape
	// from it, as the ingest does.
	members, err := listArchiveMembers(context.Background(), NewExtractor(), archive)
	require.NoError(t, err)
	_, err = importDeployablePaths(importKindExtract, "mod.7z", members)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal detected")
}

// The fake-7z cases run for both formats that extract through 7z: a .rar
// takes exactly the .7z path.
var sevenZipFormats = []string{"7z", "rar"}

func Test7zAndRar_RefuseSymlinkMemberFromListing(t *testing.T) {
	for _, format := range sevenZipFormats {
		t.Run(format, func(t *testing.T) {
			install7zFake(t, []fake7zMember{
				{path: "real.txt", attrs: "A -rw-r--r--", size: 4},
				{path: "link", attrs: "A lrwxrwxrwx", symlink: "/etc", size: 4},
			}, `/bin/echo real > "$out/real.txt"; /bin/ln -s /etc "$out/link"`)
			archive := fakeArchive(t, format)
			dest := filepath.Join(t.TempDir(), "out")

			err := NewExtractor().Extract(context.Background(), archive, dest)
			require.Error(t, err)
			assert.Contains(t, err.Error(), format+" member link")
			assert.Empty(t, entriesUnder(t, dest))
		})
	}
}

func Test7zAndRar_RefuseHardLinkMemberFromListing(t *testing.T) {
	for _, format := range sevenZipFormats {
		t.Run(format, func(t *testing.T) {
			install7zFake(t, []fake7zMember{
				{path: "real.txt", attrs: "A -rw-r--r--", size: 4},
				{path: "hard.txt", attrs: "A -rw-r--r--", hardlink: "real.txt", size: 4},
			}, `/bin/echo real > "$out/real.txt"; /bin/ln "$out/real.txt" "$out/hard.txt"`)
			archive := fakeArchive(t, format)
			dest := filepath.Join(t.TempDir(), "out")

			err := NewExtractor().Extract(context.Background(), archive, dest)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "hard.txt")
			assert.Empty(t, entriesUnder(t, dest))
		})
	}
}

// The listing showed no link, but 7z wrote one anyway: the walk of the
// private tree refuses it before anything moves.
func Test7zAndRar_RefuseLinksTheListingHid(t *testing.T) {
	for _, format := range sevenZipFormats {
		for name, tc := range map[string]struct {
			extract, want string
		}{
			"symlink":   {`/bin/echo real > "$out/real.txt"; /bin/ln -s /etc "$out/link"`, "link or special file"},
			"hard link": {`/bin/echo real > "$out/real.txt"; /bin/ln "$out/real.txt" "$out/link"`, "hard link"},
			"fifo":      {`/bin/echo real > "$out/real.txt"; /usr/bin/mkfifo "$out/link"`, "link or special file"},
		} {
			t.Run(format+"/"+name, func(t *testing.T) {
				install7zFake(t, []fake7zMember{
					{path: "real.txt", attrs: "A -rw-r--r--", size: 5},
					{path: "link", attrs: "A -rw-r--r--", size: 5},
				}, tc.extract)
				archive := fakeArchive(t, format)
				dest := t.TempDir()

				err := NewExtractor().Extract(context.Background(), archive, dest)
				require.Error(t, err)
				assert.Contains(t, err.Error(), format+" member")
				assert.Contains(t, err.Error(), tc.want)
				assert.Empty(t, entriesUnder(t, dest), "nothing moved, and the private directory is gone")
			})
		}
	}
}

func Test7zAndRar_RefuseTraversalAndReservedNamesFromListing(t *testing.T) {
	for _, format := range sevenZipFormats {
		for member, want := range map[string]string{
			"../evil.txt":       "path traversal detected",
			"sub/../../x":       "path traversal detected",
			".lmm-file-forged":  "reserved name detected",
			"a/.lmm-tree/b.txt": "reserved name detected",
		} {
			t.Run(format+"/"+member, func(t *testing.T) {
				install7zFake(t, []fake7zMember{
					{path: "ok.txt", attrs: "A -rw-r--r--", size: 3},
					{path: member, attrs: "A -rw-r--r--", size: 3},
				}, `/bin/echo ok > "$out/ok.txt"`)
				archive := fakeArchive(t, format)
				parent := t.TempDir()
				dest := filepath.Join(parent, "out")

				err := NewExtractor().Extract(context.Background(), archive, dest)
				require.Error(t, err)
				assert.Contains(t, err.Error(), want)
				assert.Empty(t, entriesUnder(t, dest))
				assert.Equal(t, []string{"out"}, entriesUnder(t, parent))
			})
		}
	}
}

// The cleanup makes the private tree writable before removing it: a refused
// archive whose extraction left a no-write directory still leaves nothing.
func Test7z_CleanupRemovesANoWritePrivateTree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	install7zFake(t, []fake7zMember{
		{path: "ro", attrs: "D dr-xr-xr-x"},
		{path: "ro/link", attrs: "A -rw-r--r--", size: 1},
	}, `/bin/mkdir -p "$out/ro"; /bin/ln -s /etc "$out/ro/link"; /bin/chmod 555 "$out/ro"`)
	archive := fakeArchive(t, "7z")
	dest := t.TempDir()

	err := NewExtractor().Extract(context.Background(), archive, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ro/link")
	noPrivateDirLeft(t, dest)
	assert.Empty(t, entriesUnder(t, dest))
}

// moveExtractedTree never writes through a link already in destDir: a
// directory member whose destination is a symlink is refused.
func TestMoveExtractedTree_RefusesALinkInDest(t *testing.T) {
	outside := t.TempDir()
	dest := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(dest, "link")))
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "link"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "link", "x.txt"), []byte("x"), 0o644))

	err := NewExtractor().moveExtractedTree(src, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "link")
	assert.Empty(t, entriesUnder(t, outside), "nothing was written through the link")
}

// mergeAcquiredTree never writes through a link in the staging directory.
func TestMergeAcquiredTree_RefusesALinkInStage(t *testing.T) {
	outside := t.TempDir()
	stage := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(stage, "link")))
	tree := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tree, "link"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tree, "link", "x.txt"), []byte("x"), 0o644))

	err := mergeAcquiredTree(tree, stage)
	require.Error(t, err)
	assert.Empty(t, entriesUnder(t, outside), "nothing was written through the link")
}

// The free-space preflight refuses an archive that declares more than the
// destination has free, before anything is written, for every format.
func TestExtract_RefusesWhenTheDeclaredSizeExceedsFreeSpace(t *testing.T) {
	restore := availableSpace
	t.Cleanup(func() { availableSpace = restore })
	availableSpace = func(string) (uint64, bool) { return 10, true }

	t.Run("native zip", func(t *testing.T) {
		archive := writeListingZip(t, filepath.Join(t.TempDir(), "mod.zip"), "a.esp", "b.esp")
		dest := t.TempDir()
		err := NewExtractor().Extract(context.Background(), archive, dest)
		require.Error(t, err)
		assert.True(t, errors.Is(err, errInsufficientSpace), "got %v", err)
		assert.Contains(t, err.Error(), "mod.zip")
		assert.Empty(t, entriesUnder(t, dest))
	})
	for _, format := range sevenZipFormats {
		t.Run(format, func(t *testing.T) {
			install7zFake(t, []fake7zMember{{path: "big.bin", attrs: "A -rw-r--r--", size: 1 << 40}},
				`/bin/echo x > "$out/big.bin"`)
			archive := fakeArchive(t, format)
			dest := t.TempDir()
			err := NewExtractor().Extract(context.Background(), archive, dest)
			require.Error(t, err)
			assert.True(t, errors.Is(err, errInsufficientSpace), "got %v", err)
			assert.Contains(t, err.Error(), "1.0 TiB")
			assert.Empty(t, entriesUnder(t, dest))
		})
	}
	t.Run("enough space extracts", func(t *testing.T) {
		availableSpace = func(string) (uint64, bool) { return 1 << 30, true }
		archive := writeListingZip(t, filepath.Join(t.TempDir(), "mod.zip"), "a.esp")
		require.NoError(t, NewExtractor().Extract(context.Background(), archive, t.TempDir()))
	})
	t.Run("unknown free space extracts", func(t *testing.T) {
		availableSpace = func(string) (uint64, bool) { return 0, false }
		archive := writeListingZip(t, filepath.Join(t.TempDir(), "mod.zip"), "a.esp")
		require.NoError(t, NewExtractor().Extract(context.Background(), archive, t.TempDir()))
	})
}

func TestZipDeclaredSize(t *testing.T) {
	archive := writeListingZip(t, filepath.Join(t.TempDir(), "mod.zip"), "a.esp", "bb.esp")
	r, err := zip.OpenReader(archive)
	require.NoError(t, err)
	defer func() { _ = r.Close() }()
	assert.Equal(t, uint64(len("payload-a.esp")+len("payload-bb.esp")), zipDeclaredSize(r.File))
}

func TestSum7zSizes(t *testing.T) {
	out := "Path = mod.7z\nPhysical Size = 99\n\n----------\nPath = a\nSize = 10\n\nPath = d\nSize = 0\nAttributes = D\n\nPath = b\nSize = 32\nPacked Size = 5\n\n"
	assert.Equal(t, uint64(42), sum7zSizes(out))
}

// seedCacheEntryWithLinks stores a real file in (g1, src, modX, 1.0)'s cache
// entry and plants the links an earlier, link-unsafe extraction could have
// left there.
func seedCacheEntryWithLinks(t *testing.T, gameCache *cache.Cache) string {
	t.Helper()
	key := cache.Key{GameID: "g1", SourceID: "src", ModID: "modX", Version: "1.0"}
	require.NoError(t, gameCache.Store(key, "keep/real.txt", []byte("real")))
	cachePath := gameCache.ModPath(key)
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(cachePath, "escaping")))
	require.NoError(t, os.Symlink("keep", filepath.Join(cachePath, "keep", "inside")))
	require.NoError(t, os.Symlink("/nonexistent/lmm-658", filepath.Join(cachePath, "dangling")))
	return cachePath
}

func TestPrepareStaging_DropsLeftoverLinks(t *testing.T) {
	gameCache := cache.New(t.TempDir())
	seedCacheEntryWithLinks(t, gameCache)
	game := &domain.Game{ID: "g1"}
	mod := &domain.Mod{ID: "modX", SourceID: "src", Version: "1.0"}

	_, stagePath, dropped, err := prepareStaging(context.Background(), gameCache, game, mod)
	require.NoError(t, err, "a leftover link no longer fails the reseed")
	t.Cleanup(func() { _ = os.RemoveAll(stagePath) })

	assert.ElementsMatch(t, []string{"dangling", "escaping", "keep/inside"}, dropped)
	assert.ElementsMatch(t, []string{"keep", "keep/real.txt"}, memberEntries(t, stagePath))
}

// A file committed over an entry that holds leftover links: the reseed
// drops them, the commit carries none, and a warning names the mod.
func TestCommit_ReseedOverLeftoverLinksWarnsNamingTheMod(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)
	cachePath := seedCacheEntryWithLinks(t, gameCache)
	outside := t.TempDir()
	require.NoError(t, os.Remove(filepath.Join(cachePath, "escaping")))
	require.NoError(t, os.Symlink(outside, filepath.Join(cachePath, "escaping")))

	// The new file's archive has a member under the leftover link's name:
	// before #658 it would have been written through the link.
	archive := writeListingZip(t, filepath.Join(t.TempDir(), "second.zip"), "escaping/x.txt")

	game := &domain.Game{ID: "g1", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "modX", SourceID: "src", Version: "1.0", Name: "Leftover Links Mod"}
	file := &domain.DownloadableFile{ID: "second", FileName: "second.zip"}

	var warnings []string
	sink := func(e Event) {
		if w, ok := e.(WarningEvent); ok {
			warnings = append(warnings, w.Message)
		}
	}
	_, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, archive, sink)
	require.NoError(t, err)

	assert.Empty(t, entriesUnder(t, outside), "nothing was written through the leftover link")
	got, err := os.ReadFile(filepath.Join(cachePath, "escaping", "x.txt"))
	require.NoError(t, err)
	assert.Equal(t, "payload-escaping/x.txt", string(got))
	assert.ElementsMatch(t, []string{"escaping", "escaping/x.txt", "keep", "keep/real.txt"},
		memberEntries(t, cachePath), "the committed entry holds no link")

	require.Len(t, warnings, 3)
	for _, w := range warnings {
		assert.Contains(t, w, "Leftover Links Mod")
	}
	assert.Contains(t, strings.Join(warnings, "\n"), "keep/inside")
}

// memberEntries is entriesUnder without the cache's own bookkeeping.
func memberEntries(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, e := range entriesUnder(t, dir) {
		if !strings.HasPrefix(e, cache.ReservedPrefix) {
			out = append(out, e)
		}
	}
	return out
}

// TestRar_RefusesRealLinkMembers builds real .rar fixtures when a rar tool
// is installed (-ol stores a symlink as a link, -oh a hard link as one);
// without one the fake-7z cases above cover the .rar path.
func TestRar_RefusesRealLinkMembers(t *testing.T) {
	require7z(t)
	if _, err := exec.LookPath("rar"); err != nil {
		t.Skip("rar not installed")
	}
	for name, tc := range map[string]struct {
		flag string
		make func(src string) error
	}{
		"symlink":   {"-ol", func(src string) error { return os.Symlink("/etc/passwd", filepath.Join(src, "link.txt")) }},
		"hard link": {"-oh", func(src string) error { return os.Link(filepath.Join(src, "real.txt"), filepath.Join(src, "link.txt")) }},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			require.NoError(t, os.MkdirAll(src, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(src, "real.txt"), []byte("real"), 0o644))
			require.NoError(t, tc.make(src))
			archive := filepath.Join(dir, "mod.rar")
			cmd := exec.Command("rar", "a", "-idq", tc.flag, archive, ".")
			cmd.Dir = src
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "building fixture .rar: %s", out)
			dest := filepath.Join(t.TempDir(), "out")

			err = NewExtractor().Extract(context.Background(), archive, dest)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "link.txt")
			assert.Empty(t, entriesUnder(t, dest))
		})
	}
}

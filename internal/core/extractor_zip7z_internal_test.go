package core

// #656: archive/zip decodes only Store and Deflate, but mod authors also ship
// zips compressed with BZIP2, LZMA, Deflate64, Zstandard or PPMd. Such a zip
// is extracted (and listed) through the system 7z instead, with the same
// protections the native path gives every member: containment, the reserved
// cache namespace, no links, and extract7z's timeout. Without 7z the error
// names the compression method and the remedy.

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// require7z skips a test that needs the system 7z, the 7z pattern.
func require7z(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
}

// without7z hides every command from the test, so the 7z lookup fails.
func without7z(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// compressible is content large enough that 7z really compresses it with
// the requested method rather than storing it.
func compressible(seed string) string {
	return strings.Repeat(seed+" lorem ipsum dolor sit amet\n", 400)
}

// build7zZip writes files (relative path -> content) under a staging tree
// and zips them with 7z using method (e.g. "BZip2", "LZMA"). extraArgs go to
// `7z a` (e.g. -snl to store symlinks as links). The fixture is checked to
// really hold a member archive/zip cannot decode, so a 7z that quietly
// stored everything cannot make the test pass vacuously.
func build7zZip(t *testing.T, method string, files map[string]string, extraArgs ...string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	for name, content := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	return run7zZip(t, dir, src, method, extraArgs...)
}

func run7zZip(t *testing.T, dir, src, method string, extraArgs ...string) string {
	t.Helper()
	archive := filepath.Join(dir, "mod.zip")
	args := append([]string{"a", "-tzip", "-mm=" + method}, extraArgs...)
	args = append(args, archive, ".")
	cmd := exec.Command("7z", args...)
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building fixture zip: %s", out)

	r, err := zip.OpenReader(archive)
	require.NoError(t, err)
	defer func() { _ = r.Close() }()
	unsupported := false
	for _, f := range r.File {
		if f.Method != zip.Store && f.Method != zip.Deflate {
			unsupported = true
		}
	}
	require.True(t, unsupported, "fixture must hold a member archive/zip cannot decode")
	return archive
}

// writeRawZip writes a zip whose members claim method but whose bytes are
// not real compressed data: enough for a listing, for a refusal that must
// happen before anything is decompressed, and for the no-7z message - and
// it needs no 7z to build.
func writeRawZip(t *testing.T, method uint16, names ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mod.zip")
	f, err := os.Create(path)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	for _, name := range names {
		fw, err := w.CreateRaw(&zip.FileHeader{Name: name, Method: method, CompressedSize64: 3, UncompressedSize64: 3})
		require.NoError(t, err)
		_, err = fw.Write([]byte("abc"))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
	return path
}

// entriesUnder lists every path under dir, relative and slash-separated.
func entriesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.Walk(dir, func(path string, _ os.FileInfo, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		out = append(out, filepath.ToSlash(rel))
		return err
	}))
	return out
}

func TestExtract_ZipUnsupportedMethodFallsBackTo7z(t *testing.T) {
	require7z(t)
	for _, method := range []string{"BZip2", "LZMA"} {
		t.Run(method, func(t *testing.T) {
			files := map[string]string{
				"DearImGuiLicense.txt":  compressible("license"),
				"SKSE/Plugins/hdt.dll":  compressible("plugin"),
				"SKSE/Plugins/hdt.toml": compressible("config"),
			}
			archive := build7zZip(t, method, files)
			dest := filepath.Join(t.TempDir(), "out")

			require.NoError(t, NewExtractor().Extract(context.Background(), archive, dest))

			for name, content := range files {
				got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
				require.NoError(t, err, name)
				assert.Equal(t, content, string(got), name)
			}
			assert.ElementsMatch(t, []string{
				"DearImGuiLicense.txt", "SKSE", "SKSE/Plugins", "SKSE/Plugins/hdt.dll", "SKSE/Plugins/hdt.toml",
			}, entriesUnder(t, dest), "the fallback leaves nothing of its own behind")
		})
	}
}

func TestExtract_ZipFallbackKeepsWhatDestAlreadyHolds(t *testing.T) {
	require7z(t)
	archive := build7zZip(t, "BZip2", map[string]string{"new.esp": compressible("new")})
	dest := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dest, ".lmm-file-1"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "earlier.esp"), []byte("earlier"), 0o644))

	require.NoError(t, NewExtractor().Extract(context.Background(), archive, dest))

	assert.ElementsMatch(t, []string{".lmm-file-1", "earlier.esp", "new.esp"}, entriesUnder(t, dest))
}

func TestExtract_ZipFallbackRefusesTraversal(t *testing.T) {
	require7z(t)
	archive := writeRawZip(t, 12, "ok.txt", "../evil.txt")
	parent := t.TempDir()
	dest := filepath.Join(parent, "out")

	err := NewExtractor().Extract(context.Background(), archive, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal detected")
	assert.Empty(t, entriesUnder(t, dest), "a refused archive writes nothing")
	_, statErr := os.Stat(filepath.Join(parent, "evil.txt"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestExtract_ZipFallbackRefusesReservedNames(t *testing.T) {
	require7z(t)
	archive := writeRawZip(t, 12, "ok.txt", ".lmm-file-2")
	dest := t.TempDir()

	err := NewExtractor().Extract(context.Background(), archive, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved name detected")
	assert.Empty(t, entriesUnder(t, dest))
}

func TestExtract_ZipFallbackRefusesLinks(t *testing.T) {
	require7z(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "real.txt"), []byte(compressible("real")), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(src, "link.txt")))
	archive := run7zZip(t, dir, src, "BZip2", "-snl")
	dest := t.TempDir()

	err := NewExtractor().Extract(context.Background(), archive, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "link.txt")
	assert.Contains(t, err.Error(), "link")
	assert.Empty(t, entriesUnder(t, dest))
}

func TestExtract_ZipUnsupportedMethodWithout7zNamesMethodAndRemedy(t *testing.T) {
	for _, tc := range []struct {
		method uint16
		name   string
	}{
		{12, "BZIP2"},
		{14, "LZMA"},
		{9, "Deflate64"},
		{93, "Zstandard"},
		{98, "PPMd"},
		{77, "method 77"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := writeRawZip(t, tc.method, "DearImGuiLicense.txt")
			dest := t.TempDir()
			without7z(t)

			err := NewExtractor().Extract(context.Background(), archive, dest)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrSevenZipMissing), "got %v", err)
			assert.Contains(t, err.Error(), tc.name)
			assert.Contains(t, err.Error(), "DearImGuiLicense.txt")
			assert.Contains(t, err.Error(), "install")
			assert.Empty(t, entriesUnder(t, dest))
		})
	}
}

func TestExtract_DeflateZipNeedsNo7z(t *testing.T) {
	archive := writeListingZip(t, filepath.Join(t.TempDir(), "mod.zip"), "a.esp")
	dest := t.TempDir()
	without7z(t)

	require.NoError(t, NewExtractor().Extract(context.Background(), archive, dest))
	got, err := os.ReadFile(filepath.Join(dest, "a.esp"))
	require.NoError(t, err)
	assert.Equal(t, "payload-a.esp", string(got))
}

func TestListArchiveMembers_ZipUnsupportedMethodListsThrough7z(t *testing.T) {
	require7z(t)
	archive := build7zZip(t, "BZip2", map[string]string{
		"MyMod/a.esp":     compressible("a"),
		"MyMod/sub/b.txt": compressible("b"),
	})

	members, err := listArchiveMembers(context.Background(), NewExtractor(), archive)
	require.NoError(t, err)

	assert.ElementsMatch(t, []archiveMember{
		{Path: "MyMod", Dir: true},
		{Path: "MyMod/sub", Dir: true},
		{Path: "MyMod/a.esp"},
		{Path: "MyMod/sub/b.txt"},
	}, members)
}

func TestListArchiveMembers_ZipFallbackRefusesLinks(t *testing.T) {
	require7z(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "real.txt"), []byte(compressible("real")), 0o644))
	require.NoError(t, os.Symlink("real.txt", filepath.Join(src, "link.txt")))
	archive := run7zZip(t, dir, src, "BZip2", "-snl")

	_, err := listArchiveMembers(context.Background(), NewExtractor(), archive)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "link.txt")
}

func TestListArchiveMembers_ZipUnsupportedMethodWithout7z(t *testing.T) {
	archive := writeRawZip(t, 12, "MyMod/a.esp")
	without7z(t)

	_, err := listArchiveMembers(context.Background(), NewExtractor(), archive)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSevenZipMissing), "got %v", err)
	assert.Contains(t, err.Error(), "BZIP2")
	assert.Contains(t, err.Error(), "MyMod/a.esp")
}

func TestZipMethodName(t *testing.T) {
	assert.Equal(t, "BZIP2", zipMethodName(12))
	assert.Equal(t, "Deflate64", zipMethodName(9))
	assert.Equal(t, "method 77", zipMethodName(77))
}

// TestCheckExtractedTree_RefusesLinks covers the second line of defence: a
// link 7z wrote despite a listing that showed none is refused before
// anything moves into destDir.
func TestCheckExtractedTree_RefusesLinks(t *testing.T) {
	t.Run("regular tree passes", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "a.txt"), []byte("a"), 0o644))
		assert.NoError(t, checkExtractedTree(dir, "zip"))
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "evil")))
		err := checkExtractedTree(dir, "zip")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "evil")
	})
	t.Run("hard link", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644))
		require.NoError(t, os.Link(filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")))
		err := checkExtractedTree(dir, "zip")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hard link")
	})
}

// TestExtract_ZipFallbackReadOnlyDirectory: a directory the archive records
// without write permission still has its files moved out, and the private
// extraction directory is still removed - the native path always creates
// directories 0755.
func TestExtract_ZipFallbackReadOnlyDirectory(t *testing.T) {
	require7z(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	ro := filepath.Join(src, "ro")
	require.NoError(t, os.MkdirAll(ro, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ro, "a.txt"), []byte(compressible("a")), 0o644))
	require.NoError(t, os.Chmod(ro, 0o555))
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	archive := run7zZip(t, dir, src, "BZip2")
	dest := t.TempDir()

	require.NoError(t, NewExtractor().Extract(context.Background(), archive, dest))
	assert.ElementsMatch(t, []string{"ro", "ro/a.txt"}, entriesUnder(t, dest))
}

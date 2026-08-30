package core

// Unit tests for the archive LISTING and the pure member -> deployable-path
// normalisation PlanImportArchive and importWithIdentity share (#314, R-B2).
// Internal because every identifier here is unexported: the point of the
// refactor is that ONE implementation answers "what would this archive
// contribute", so the plan and the ingest cannot drift apart.

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeListingZip writes a zip holding names in the given order; a name
// ending in "/" becomes an explicit directory entry.
func writeListingZip(t *testing.T, path string, names ...string) string {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	w := zip.NewWriter(f)
	for _, name := range names {
		if name[len(name)-1] == '/' {
			_, err := w.Create(name)
			require.NoError(t, err)
			continue
		}
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte("payload-" + name))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return path
}

func TestListArchiveMembers_Zip(t *testing.T) {
	path := writeListingZip(t, filepath.Join(t.TempDir(), "mod.zip"),
		"MyMod/", "MyMod/a.esp", "MyMod/sub/b.txt", "top.txt")

	members, err := listArchiveMembers(context.Background(), NewExtractor(), path)
	require.NoError(t, err)

	assert.Equal(t, []archiveMember{
		{Path: "MyMod/", Dir: true},
		{Path: "MyMod/a.esp"},
		{Path: "MyMod/sub/b.txt"},
		{Path: "top.txt"},
	}, members)
}

func TestListArchiveMembers_UnsupportedFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mod.txt")
	require.NoError(t, os.WriteFile(path, []byte("not an archive"), 0o644))

	_, err := listArchiveMembers(context.Background(), NewExtractor(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported archive format")
}

func TestListArchiveMembers_SevenZip(t *testing.T) {
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "MyMod", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "MyMod", "a.esp"), []byte("hi"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "MyMod", "sub", "b.txt"), []byte("yo"), 0o644))

	archive := filepath.Join(dir, "mod.7z")
	cmd := exec.Command("7z", "a", "-t7z", archive, "./MyMod")
	cmd.Dir = src
	require.NoError(t, cmd.Run())

	members, err := listArchiveMembers(context.Background(), NewExtractor(), archive)
	require.NoError(t, err)

	assert.Equal(t, []archiveMember{
		{Path: "MyMod", Dir: true},
		{Path: "MyMod/sub", Dir: true},
		{Path: "MyMod/a.esp"},
		{Path: "MyMod/sub/b.txt"},
	}, members)
}

func TestImportMemberRelPath(t *testing.T) {
	t.Run("cleans", func(t *testing.T) {
		rel, err := importMemberRelPath("./MyMod//a.esp")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join("MyMod", "a.esp"), rel)
	})

	t.Run("rejects reserved namespace", func(t *testing.T) {
		_, err := importMemberRelPath(cache.ReservedPrefix + "file-7")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved name detected")
	})

	t.Run("rejects traversal", func(t *testing.T) {
		_, err := importMemberRelPath("../../etc/passwd")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "path traversal detected")
	})
}

func TestImportDeployablePaths(t *testing.T) {
	members := []archiveMember{
		{Path: "MyMod/", Dir: true},
		{Path: "MyMod/b.esp"},
		{Path: "MyMod/a.esp"},
		{Path: "MyMod/a.esp"}, // a duplicate member extracts onto one file
	}

	t.Run("extract sorts, dedupes and drops directories", func(t *testing.T) {
		paths, err := importDeployablePaths(importKindExtract, "mod.zip", members)
		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join("MyMod", "a.esp"), filepath.Join("MyMod", "b.esp")}, paths)
	})

	t.Run("copy deploys the archive itself", func(t *testing.T) {
		paths, err := importDeployablePaths(importKindCopy, "mod.zip", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"mod.zip"}, paths)
	})

	t.Run("convertible pak deploys the archive itself", func(t *testing.T) {
		paths, err := importDeployablePaths(importKindConvertPak, "Raw_Weapon.pak", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"Raw_Weapon.pak"}, paths)
	})

	t.Run("native merge source deploys nothing", func(t *testing.T) {
		paths, err := importDeployablePaths(importKindMergeSource, "mod.exmodz", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{}, paths)
	})

	t.Run("a reserved member is refused, not skipped", func(t *testing.T) {
		_, err := importDeployablePaths(importKindExtract, "mod.zip",
			[]archiveMember{{Path: cache.ReservedPrefix + "file-7"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserved name detected")
	})
}

func TestImportedModName(t *testing.T) {
	t.Run("extract folds a sole top-level directory", func(t *testing.T) {
		name := importedModName(importKindExtract, "mymod-1.0.zip", "1.0", []archiveMember{
			{Path: "MyMod/", Dir: true}, {Path: "MyMod/a.esp"},
		})
		assert.Equal(t, "MyMod", name)
	})

	t.Run("extract falls back to the archive name", func(t *testing.T) {
		name := importedModName(importKindExtract, "mymod-1.0.zip", "1.0", []archiveMember{
			{Path: "a.esp"}, {Path: "b.esp"},
		})
		assert.Equal(t, "mymod-1.0", name)
	})

	t.Run("extract falls back for an empty archive", func(t *testing.T) {
		assert.Equal(t, "mymod-1.0", importedModName(importKindExtract, "mymod-1.0.zip", "1.0", nil))
	})

	t.Run("copy trims the version suffix", func(t *testing.T) {
		assert.Equal(t, "mymod", importedModName(importKindCopy, "mymod-1.0.zip", "1.0", nil))
	})

	t.Run("copy keeps an unknown version's whole base name", func(t *testing.T) {
		assert.Equal(t, "mymod", importedModName(importKindCopy, "mymod.zip", "unknown", nil))
	})
}

func TestInstallHookNames(t *testing.T) {
	hooks := &ResolvedHooks{Install: domain.HookConfig{BeforeAll: "a", AfterEach: "b"}}

	assert.Equal(t, []string{"install.before_all", "install.after_each"}, installHookNames(hooks, false))
	assert.Nil(t, installHookNames(hooks, true), "--no-hooks runs none of them")
}

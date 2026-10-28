package core_test

// ListDirectories (#529): the server-side folder chooser's one question -
// "what folders are under this absolute path?" - answered for the CLI's
// and the web UI's directory fields alike.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newDirsService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	return svc
}

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		require.NoError(t, os.MkdirAll(filepath.Join(root, n), 0o755))
	}
}

func entryNames(l *core.DirectoryListing) []string {
	names := make([]string, 0, len(l.Entries))
	for _, e := range l.Entries {
		names = append(names, e.Name)
	}
	return names
}

func requireDirError(t *testing.T, err error, reason core.DirectoryErrorReason) {
	t.Helper()
	var de *core.DirectoryListError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, reason, de.Reason)
	assert.NotEmpty(t, de.Error())
}

func TestListDirectories_ListsSubdirectoriesOnly(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "alpha", "beta")
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644))

	l, err := newDirsService(t).ListDirectories(context.Background(), root, core.ListDirectoriesOptions{})
	require.NoError(t, err)

	assert.Equal(t, root, l.Path)
	assert.Equal(t, filepath.Dir(root), l.Parent)
	assert.Equal(t, []string{"alpha", "beta"}, entryNames(l))
	assert.Equal(t, filepath.Join(root, "alpha"), l.Entries[0].Path)
	assert.True(t, l.Entries[0].Readable)
	assert.False(t, l.Entries[0].Hidden)
	assert.False(t, l.Entries[0].Symlink)
}

func TestListDirectories_NeverReturnsRegularFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), nil, 0o644))
	require.NoError(t, os.Symlink(filepath.Join(root, "file"), filepath.Join(root, "link-to-file")))
	require.NoError(t, os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "dangling")))

	l, err := newDirsService(t).ListDirectories(context.Background(), root, core.ListDirectoriesOptions{Hidden: true})
	require.NoError(t, err)
	assert.Empty(t, l.Entries, "a file, a symlink to a file and a dangling link are not folders")
	assert.NotNil(t, l.Entries, "the wire document says [] rather than null")
}

func TestListDirectories_SortsCaseInsensitively(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "banana", "Apple", "cherry", "Banana2", "apple2")

	l, err := newDirsService(t).ListDirectories(context.Background(), root, core.ListDirectoriesOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"Apple", "apple2", "banana", "Banana2", "cherry"}, entryNames(l))
}

func TestListDirectories_HiddenFoldersAreOptIn(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, ".config", "visible")
	svc := newDirsService(t)

	l, err := svc.ListDirectories(context.Background(), root, core.ListDirectoriesOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"visible"}, entryNames(l))

	l, err = svc.ListDirectories(context.Background(), root, core.ListDirectoriesOptions{Hidden: true})
	require.NoError(t, err)
	assert.Equal(t, []string{".config", "visible"}, entryNames(l))
	assert.True(t, l.Entries[0].Hidden)
	assert.False(t, l.Entries[1].Hidden)
}

func TestListDirectories_SymlinkedDirectoryIsMarkedAndNotRecursed(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "real/inner")
	require.NoError(t, os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")))

	l, err := newDirsService(t).ListDirectories(context.Background(), root, core.ListDirectoriesOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"alias", "real"}, entryNames(l))
	assert.True(t, l.Entries[0].Symlink)
	assert.False(t, l.Entries[1].Symlink)
	assert.True(t, l.Entries[0].Readable)
	assert.Equal(t, filepath.Join(root, "alias"), l.Entries[0].Path, "the link's own path, so navigating stays where the user was")
}

func TestListDirectories_SymlinkLoopIsHarmless(t *testing.T) {
	root := t.TempDir()
	// root/loop -> root: listing it lists the same folder again, once per
	// request; nothing here walks the tree.
	require.NoError(t, os.Symlink(root, filepath.Join(root, "loop")))
	svc := newDirsService(t)

	l, err := svc.ListDirectories(context.Background(), root, core.ListDirectoriesOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"loop"}, entryNames(l))
	assert.True(t, l.Entries[0].Symlink)

	deeper, err := svc.ListDirectories(context.Background(), filepath.Join(root, "loop", "loop", "loop"), core.ListDirectoriesOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"loop"}, entryNames(deeper))
}

func TestListDirectories_CleansThePath(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a/b")

	l, err := newDirsService(t).ListDirectories(context.Background(), root+"/a/../a//b/.", core.ListDirectoriesOptions{})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "a", "b"), l.Path)
	assert.Equal(t, filepath.Join(root, "a"), l.Parent)
}

func TestListDirectories_ExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mkdirs(t, home, "Games/steam")
	svc := newDirsService(t)

	l, err := svc.ListDirectories(context.Background(), "~", core.ListDirectoriesOptions{})
	require.NoError(t, err)
	assert.Equal(t, home, l.Path)

	l, err = svc.ListDirectories(context.Background(), "~/Games", core.ListDirectoriesOptions{})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "Games"), l.Path)
	assert.Equal(t, []string{"steam"}, entryNames(l))
}

func TestListDirectories_RootHasNoParent(t *testing.T) {
	l, err := newDirsService(t).ListDirectories(context.Background(), "/", core.ListDirectoriesOptions{})
	require.NoError(t, err)
	assert.Equal(t, "/", l.Path)
	assert.Empty(t, l.Parent)
}

func TestListDirectories_RefusesANonAbsolutePath(t *testing.T) {
	svc := newDirsService(t)
	for _, p := range []string{"", "relative/dir", "./here", "~user/x"} {
		_, err := svc.ListDirectories(context.Background(), p, core.ListDirectoriesOptions{})
		requireDirError(t, err, core.DirectoryNotAbsolute)
		var de *core.DirectoryListError
		require.ErrorAs(t, err, &de)
		assert.Equal(t, p, de.Path)
	}
}

func TestListDirectories_RefusesAMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := newDirsService(t).ListDirectories(context.Background(), missing, core.ListDirectoriesOptions{})
	requireDirError(t, err, core.DirectoryNotFound)
	var de *core.DirectoryListError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, missing, de.Path)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestListDirectories_RefusesAFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "f.txt")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	svc := newDirsService(t)

	_, err := svc.ListDirectories(context.Background(), file, core.ListDirectoriesOptions{})
	requireDirError(t, err, core.DirectoryNotADirectory)

	// A path THROUGH a file is simply not there.
	_, err = svc.ListDirectories(context.Background(), filepath.Join(file, "inside"), core.ListDirectoriesOptions{})
	requireDirError(t, err, core.DirectoryNotFound)
}

func TestListDirectories_RefusesAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	require.NoError(t, os.Mkdir(locked, 0o755))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, err := newDirsService(t).ListDirectories(context.Background(), locked, core.ListDirectoriesOptions{})
	requireDirError(t, err, core.DirectoryPermissionDenied)
	assert.ErrorIs(t, err, fs.ErrPermission)
}

func TestListDirectories_MarksAnUnreadableSubdirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	mkdirs(t, root, "open")
	locked := filepath.Join(root, "locked")
	require.NoError(t, os.Mkdir(locked, 0o755))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	l, err := newDirsService(t).ListDirectories(context.Background(), root, core.ListDirectoriesOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"locked", "open"}, entryNames(l))
	assert.False(t, l.Entries[0].Readable)
	assert.True(t, l.Entries[1].Readable)
}

func TestListDirectories_HonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newDirsService(t).ListDirectories(ctx, t.TempDir(), core.ListDirectoriesOptions{})
	assert.ErrorIs(t, err, context.Canceled)
	var de *core.DirectoryListError
	assert.False(t, errors.As(err, &de))
}

func TestListDirectories_NearestFallsBackToTheClosestExistingAncestor(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "games/steam")
	svc := newDirsService(t)

	missing := filepath.Join(root, "games", "steam", "common", "Valheim")
	l, err := svc.ListDirectories(context.Background(), missing, core.ListDirectoriesOptions{Nearest: true})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "games", "steam"), l.Path)
	assert.Equal(t, missing, l.Requested, "the caller can tell the answer is not what it asked for")

	// A file falls back to the folder it is in.
	file := filepath.Join(root, "games", "f.txt")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	l, err = svc.ListDirectories(context.Background(), file, core.ListDirectoriesOptions{Nearest: true})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "games"), l.Path)
	assert.Equal(t, file, l.Requested)

	// An exact hit reports no Requested.
	l, err = svc.ListDirectories(context.Background(), root, core.ListDirectoriesOptions{Nearest: true})
	require.NoError(t, err)
	assert.Equal(t, root, l.Path)
	assert.Empty(t, l.Requested)
}

func TestListDirectories_NearestStartsAtHomeWhenThereIsNothingUsable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc := newDirsService(t)
	for _, p := range []string{"", "relative/path", "   "} {
		l, err := svc.ListDirectories(context.Background(), p, core.ListDirectoriesOptions{Nearest: true})
		require.NoError(t, err, "path %q", p)
		assert.Equal(t, home, l.Path)
	}
}

func TestListDirectories_NearestStillRefusesAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	require.NoError(t, os.Mkdir(locked, 0o755))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, err := newDirsService(t).ListDirectories(context.Background(), locked, core.ListDirectoriesOptions{Nearest: true})
	requireDirError(t, err, core.DirectoryPermissionDenied)
}

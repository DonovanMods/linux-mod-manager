package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyProfileOverrides_RejectsPathTraversal(t *testing.T) {
	baseDir := t.TempDir()
	game := &domain.Game{InstallPath: baseDir}

	tests := []struct {
		name    string
		relPath string
	}{
		{"parent escape", "../../../etc/passwd"},
		{"parent escape relative", "..\\..\\..\\.bashrc"},
		{"absolute path", "/etc/passwd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := &domain.Profile{
				Overrides: map[string][]byte{tt.relPath: []byte("content")},
			}
			err := applyProfileOverrides(game, profile, nil)
			require.Error(t, err)
			// Path traversal returns "escapes"; absolute/invalid returns "invalid override path"
			assert.True(t, strings.Contains(err.Error(), "escapes") || strings.Contains(err.Error(), "invalid override path"), "error: %s", err.Error())
		})
	}
}

func TestApplyProfileOverrides_Success(t *testing.T) {
	baseDir := t.TempDir()
	game := &domain.Game{InstallPath: baseDir}
	profile := &domain.Profile{
		Overrides: map[string][]byte{
			"Data/skyrim.ini": []byte("[General]\nkey=value"),
			"subdir/file.txt": []byte("hello"),
		},
	}
	err := applyProfileOverrides(game, profile, nil)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(baseDir, "Data", "skyrim.ini"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "[General]")

	content, err = os.ReadFile(filepath.Join(baseDir, "subdir", "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(content))
}

// TestApplyProfileOverrides_RefusesAGameWithNoInstallPath: filepath.Abs("")
// is the working directory, so a game with no install_path wrote its
// overrides there - a test's cfg/game.ini once landed in internal/core.
func TestApplyProfileOverrides_RefusesAGameWithNoInstallPath(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	game := &domain.Game{ID: "g1"}
	profile := &domain.Profile{Overrides: map[string][]byte{"cfg/game.ini": []byte("[x]\n")}}

	err := applyProfileOverrides(game, profile, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install path")
	_, statErr := os.Stat(filepath.Join(cwd, "cfg"))
	assert.True(t, os.IsNotExist(statErr), "nothing is written to the working directory")
}

// TestApplyProfileOverrides_ABadKeyWritesNothing (#621 review S6): every
// key is checked before any is written, so one escaping key fails the whole
// set rather than leaving the keys that sorted before it on disk.
func TestApplyProfileOverrides_ABadKeyWritesNothing(t *testing.T) {
	baseDir := t.TempDir()
	game := &domain.Game{InstallPath: baseDir}
	profile := &domain.Profile{Overrides: map[string][]byte{
		"a/first.ini":       []byte("1"),
		"b/second.ini":      []byte("2"),
		"../../../.bashrc":  []byte("evil"),
		"z/last-ok-key.ini": []byte("3"),
	}}
	for range 20 { // map order varies; none may write
		err := applyProfileOverrides(game, profile, nil)
		require.Error(t, err)
		entries, rerr := os.ReadDir(baseDir)
		require.NoError(t, rerr)
		require.Empty(t, entries, "nothing may be written when one key is refused")
	}
}

func TestCheckOverridePath(t *testing.T) {
	for _, ok := range []string{"engine/config/user.ini", "Game.ini", "a/./b.ini", "a/../b.ini"} {
		require.NoError(t, checkOverridePath(ok), ok)
	}
	for _, bad := range []string{"../../../.bashrc", "/etc/passwd", "", ".", "a/../../b"} {
		require.Error(t, checkOverridePath(bad), bad)
	}
}

// TestApplyProfileOverrides_ADirectorySymlinkCannotCarryTheWriteOutside is
// #621 review N2's first case: containment by path text alone let a
// directory symlink inside the game directory take the write outside it.
func TestApplyProfileOverrides_ADirectorySymlinkCannotCarryTheWriteOutside(t *testing.T) {
	baseDir, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(baseDir, "Config")))
	game := &domain.Game{InstallPath: baseDir}

	for _, key := range []string{"Config/Game.ini", "Config/sub/new.ini"} {
		err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{key: []byte("evil")}}, nil)
		require.Error(t, err, key)
		assert.Contains(t, err.Error(), "through a symlink", key)
	}
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was written, and no directory made, outside the game directory")

	// A symlink that stays inside the game directory is followed.
	require.NoError(t, os.Mkdir(filepath.Join(baseDir, "real"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(baseDir, "real"), filepath.Join(baseDir, "alias")))
	require.NoError(t, applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{"alias/a.ini": []byte("ok")}}, nil))
	got, err := os.ReadFile(filepath.Join(baseDir, "real", "a.ini"))
	require.NoError(t, err)
	assert.Equal(t, "ok", string(got))
}

// TestApplyProfileOverrides_NeverWritesThroughAFileSymlink is N2's second
// case: a mod file deployed as a symlink (the default link method) points
// into the shared mod cache, and an override used to be written through it
// into the cache. Such a link is refused; a link that stays inside the
// game directory is replaced by a regular file, its target untouched.
func TestApplyProfileOverrides_NeverWritesThroughAFileSymlink(t *testing.T) {
	baseDir, cacheDir := t.TempDir(), t.TempDir()
	game := &domain.Game{InstallPath: baseDir}

	cached := filepath.Join(cacheDir, "mod.ini")
	require.NoError(t, os.WriteFile(cached, []byte("mod bytes"), 0o644))
	require.NoError(t, os.Symlink(cached, filepath.Join(baseDir, "mod.ini")))
	err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{"mod.ini": []byte("override")}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "will not write an override through it")
	got, err := os.ReadFile(cached)
	require.NoError(t, err)
	assert.Equal(t, "mod bytes", string(got), "the cache entry is untouched")

	inside := filepath.Join(baseDir, "stock.ini")
	require.NoError(t, os.WriteFile(inside, []byte("stock"), 0o644))
	require.NoError(t, os.Symlink(inside, filepath.Join(baseDir, "linked.ini")))
	require.NoError(t, applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{"linked.ini": []byte("override")}}, nil))
	info, err := os.Lstat(filepath.Join(baseDir, "linked.ini"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the link was replaced by a regular file")
	got, err = os.ReadFile(inside)
	require.NoError(t, err)
	assert.Equal(t, "stock", string(got), "the link's target was not written through")
}

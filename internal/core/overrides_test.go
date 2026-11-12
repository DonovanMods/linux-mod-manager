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

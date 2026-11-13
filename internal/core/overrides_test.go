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

// overrideWarning returns the one warning naming relPath, or fails.
func overrideWarning(t *testing.T, warnings []string, relPath string) string {
	t.Helper()
	for _, w := range warnings {
		if strings.HasPrefix(w, "override "+relPath+" skipped: ") {
			return w
		}
	}
	t.Fatalf("no warning names override %q in %q", relPath, warnings)
	return ""
}

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
			warnings, err := applyProfileOverrides(game, profile, nil)
			require.NoError(t, err, "a refused override is a warning, not a failure")
			w := overrideWarning(t, warnings, tt.relPath)
			// Path traversal returns "escapes"; absolute/invalid returns "invalid override path"
			assert.True(t, strings.Contains(w, "escapes") || strings.Contains(w, "invalid override path"), "warning: %s", w)
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
	warnings, err := applyProfileOverrides(game, profile, nil)
	require.NoError(t, err)
	require.Empty(t, warnings)

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

	_, err := applyProfileOverrides(game, profile, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install path")
	_, statErr := os.Stat(filepath.Join(cwd, "cfg"))
	assert.True(t, os.IsNotExist(statErr), "nothing is written to the working directory")
}

// TestApplyProfileOverrides_ARefusedOverrideDoesNotStopTheOthers is the
// final review's per-override rule: an escaping override is refused and
// named in a warning, and the valid one beside it is written.
func TestApplyProfileOverrides_ARefusedOverrideDoesNotStopTheOthers(t *testing.T) {
	baseDir := t.TempDir()
	game := &domain.Game{InstallPath: baseDir}
	profile := &domain.Profile{Overrides: map[string][]byte{
		"../../../.bashrc": []byte("evil"),
		"Config/Game.ini":  []byte("[A]\n"),
	}}
	warnings, err := applyProfileOverrides(game, profile, nil)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, overrideWarning(t, warnings, "../../../.bashrc"), "escapes")
	got, err := os.ReadFile(filepath.Join(baseDir, "Config", "Game.ini"))
	require.NoError(t, err)
	assert.Equal(t, "[A]\n", string(got), "the valid override is written")
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
		warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{key: []byte("evil")}}, nil)
		require.NoError(t, err, key)
		assert.Contains(t, overrideWarning(t, warnings, key), "through a symlink", key)
	}
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was written, and no directory made, outside the game directory")

	// A symlink that stays inside the game directory is followed.
	require.NoError(t, os.Mkdir(filepath.Join(baseDir, "real"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(baseDir, "real"), filepath.Join(baseDir, "alias")))
	warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{"alias/a.ini": []byte("ok")}}, nil)
	require.NoError(t, err)
	require.Empty(t, warnings)
	got, err := os.ReadFile(filepath.Join(baseDir, "real", "a.ini"))
	require.NoError(t, err)
	assert.Equal(t, "ok", string(got))
}

// TestApplyProfileOverrides_SkipsASymlinkedModFileAndWritesTheRest is the
// final review's probe: a profile with game.ini and mod.ini, where mod.ini
// is a mod file deployed as a symlink into the mod cache. game.ini is
// written; mod.ini is skipped with a warning naming the limitation (#630),
// and the cache entry is untouched.
func TestApplyProfileOverrides_SkipsASymlinkedModFileAndWritesTheRest(t *testing.T) {
	baseDir, cacheDir := t.TempDir(), t.TempDir()
	game := &domain.Game{InstallPath: baseDir}
	cached := filepath.Join(cacheDir, "mod.ini")
	require.NoError(t, os.WriteFile(cached, []byte("mod bytes"), 0o644))
	require.NoError(t, os.Symlink(cached, filepath.Join(baseDir, "mod.ini")))

	warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{
		"game.ini": []byte("override"),
		"mod.ini":  []byte("override"),
	}}, nil)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Equal(t, "override mod.ini skipped: this file belongs to a deployed mod; lmm cannot override a mod's own files yet", warnings[0])

	got, err := os.ReadFile(filepath.Join(baseDir, "game.ini"))
	require.NoError(t, err)
	assert.Equal(t, "override", string(got), "game.ini is written")
	got, err = os.ReadFile(cached)
	require.NoError(t, err)
	assert.Equal(t, "mod bytes", string(got), "the cache entry is untouched")
	info, err := os.Lstat(filepath.Join(baseDir, "mod.ini"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "the mod's link is left as deployed")
}

// TestApplyProfileOverrides_SkipsAHardlinkedModFile is the same probe with
// the hardlink method: writing the game's name would change the cache's
// copy too, so a file with more than one link is skipped the same way.
func TestApplyProfileOverrides_SkipsAHardlinkedModFile(t *testing.T) {
	baseDir, cacheDir := t.TempDir(), t.TempDir()
	game := &domain.Game{InstallPath: baseDir}
	cached := filepath.Join(cacheDir, "mod.ini")
	require.NoError(t, os.WriteFile(cached, []byte("mod bytes"), 0o644))
	if err := os.Link(cached, filepath.Join(baseDir, "mod.ini")); err != nil {
		t.Skipf("no hard links across these temp dirs: %v", err)
	}

	warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{
		"game.ini": []byte("override"),
		"mod.ini":  []byte("override"),
	}}, nil)
	require.NoError(t, err)
	assert.Contains(t, overrideWarning(t, warnings, "mod.ini"), "belongs to a deployed mod")
	got, err := os.ReadFile(cached)
	require.NoError(t, err)
	assert.Equal(t, "mod bytes", string(got), "the cache's copy is untouched")
	got, err = os.ReadFile(filepath.Join(baseDir, "game.ini"))
	require.NoError(t, err)
	assert.Equal(t, "override", string(got))
}

// TestApplyProfileOverrides_ReplacesALinkInsideTheGameDirectory: a symlink
// that stays inside the game directory is replaced by a regular file, never
// written through, so its target keeps its content.
func TestApplyProfileOverrides_ReplacesALinkInsideTheGameDirectory(t *testing.T) {
	baseDir := t.TempDir()
	game := &domain.Game{InstallPath: baseDir}
	inside := filepath.Join(baseDir, "stock.ini")
	require.NoError(t, os.WriteFile(inside, []byte("stock"), 0o644))
	require.NoError(t, os.Symlink(inside, filepath.Join(baseDir, "linked.ini")))

	warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{"linked.ini": []byte("override")}}, nil)
	require.NoError(t, err)
	require.Empty(t, warnings)
	info, err := os.Lstat(filepath.Join(baseDir, "linked.ini"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the link was replaced by a regular file")
	got, err := os.ReadFile(inside)
	require.NoError(t, err)
	assert.Equal(t, "stock", string(got), "the link's target was not written through")
}

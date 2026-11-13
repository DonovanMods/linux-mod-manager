package core

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
			warnings, err := applyProfileOverrides(game, profile, overrideEnv{})
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
	warnings, err := applyProfileOverrides(game, profile, overrideEnv{})
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

	_, err := applyProfileOverrides(game, profile, overrideEnv{})
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
	warnings, err := applyProfileOverrides(game, profile, overrideEnv{})
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
		warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{key: []byte("evil")}}, overrideEnv{})
		require.NoError(t, err, key)
		assert.Contains(t, overrideWarning(t, warnings, key), "through a symlink", key)
	}
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was written, and no directory made, outside the game directory")

	// A symlink that stays inside the game directory is followed.
	require.NoError(t, os.Mkdir(filepath.Join(baseDir, "real"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(baseDir, "real"), filepath.Join(baseDir, "alias")))
	warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{"alias/a.ini": []byte("ok")}}, overrideEnv{})
	require.NoError(t, err)
	require.Empty(t, warnings)
	got, err := os.ReadFile(filepath.Join(baseDir, "real", "a.ini"))
	require.NoError(t, err)
	assert.Equal(t, "ok", string(got))
}

// TestApplyProfileOverrides_BreaksASymlinkedModFileIntoACopy is #630: a
// profile with game.ini and mod.ini, where mod.ini is a mod file deployed
// as a symlink into the mod cache. Both are written; mod.ini becomes a
// regular file of its own holding the override, its record carries the
// copy, and the cache entry is untouched.
func TestApplyProfileOverrides_BreaksASymlinkedModFileIntoACopy(t *testing.T) {
	baseDir, cacheDir := t.TempDir(), t.TempDir()
	game := &domain.Game{InstallPath: baseDir}
	cached := filepath.Join(cacheDir, "mod.ini")
	require.NoError(t, os.WriteFile(cached, []byte("mod bytes"), 0o644))
	require.NoError(t, os.Symlink(cached, filepath.Join(baseDir, "mod.ini")))
	var recorded []string
	env := overrideEnv{cacheRoots: []string{cacheDir}, recordCopy: func(dst string) { recorded = append(recorded, dst) }}

	warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{
		"game.ini": []byte("override"),
		"mod.ini":  []byte("override"),
	}}, env)
	require.NoError(t, err)
	require.Empty(t, warnings)

	got, err := os.ReadFile(filepath.Join(baseDir, "game.ini"))
	require.NoError(t, err)
	assert.Equal(t, "override", string(got), "game.ini is written")
	info, err := os.Lstat(filepath.Join(baseDir, "mod.ini"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the mod's link is broken into a regular file")
	got, err = os.ReadFile(filepath.Join(baseDir, "mod.ini"))
	require.NoError(t, err)
	assert.Equal(t, "override", string(got))
	got, err = os.ReadFile(cached)
	require.NoError(t, err)
	assert.Equal(t, "mod bytes", string(got), "the cache entry is untouched")
	assert.Empty(t, recorded, "no record names it, so none is re-recorded")
}

// TestApplyProfileOverrides_BreaksAHardlinkedModFile is the same with the
// hardlink method: the game's name gets a file of its own, so the cache's
// copy keeps its bytes, and the record is told about the copy.
func TestApplyProfileOverrides_BreaksAHardlinkedModFile(t *testing.T) {
	baseDir, cacheDir := t.TempDir(), t.TempDir()
	game := &domain.Game{InstallPath: baseDir}
	cached := filepath.Join(cacheDir, "mod.ini")
	require.NoError(t, os.WriteFile(cached, []byte("mod bytes"), 0o644))
	if err := os.Link(cached, filepath.Join(baseDir, "mod.ini")); err != nil {
		t.Skipf("no hard links across these temp dirs: %v", err)
	}
	modIni, err := resolveExisting(filepath.Join(baseDir, "mod.ini"))
	require.NoError(t, err)
	var recorded []string
	env := overrideEnv{
		modFile:    func(dst string) bool { return dst == modIni },
		recordCopy: func(dst string) { recorded = append(recorded, dst) },
	}

	warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{
		"game.ini": []byte("override"),
		"mod.ini":  []byte("override"),
	}}, env)
	require.NoError(t, err)
	require.Empty(t, warnings)
	got, err := os.ReadFile(cached)
	require.NoError(t, err)
	assert.Equal(t, "mod bytes", string(got), "the cache's copy is untouched")
	got, err = os.ReadFile(filepath.Join(baseDir, "mod.ini"))
	require.NoError(t, err)
	assert.Equal(t, "override", string(got))
	assert.Equal(t, []string{modIni}, recorded)
}

// TestApplyProfileOverrides_RefusesWhatIsNotLmms is the #630 comment's
// wording fix: a link or a hard link lmm did not deploy is refused with a
// reason that says so, rather than blaming a mod.
func TestApplyProfileOverrides_RefusesWhatIsNotLmms(t *testing.T) {
	baseDir, outside := t.TempDir(), t.TempDir()
	game := &domain.Game{InstallPath: baseDir}
	theirs := filepath.Join(outside, "theirs.ini")
	require.NoError(t, os.WriteFile(theirs, []byte("theirs"), 0o644))
	require.NoError(t, os.Symlink(theirs, filepath.Join(baseDir, "linked.ini")))
	shared := filepath.Join(baseDir, "shared.ini")
	require.NoError(t, os.WriteFile(shared, []byte("shared"), 0o644))
	require.NoError(t, os.Link(shared, filepath.Join(baseDir, "other-name.ini")))

	warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{
		"linked.ini": []byte("override"),
		"shared.ini": []byte("override"),
	}}, overrideEnv{})
	require.NoError(t, err)
	require.Len(t, warnings, 2)
	assert.Equal(t, "override linked.ini skipped: it is a link to a file outside the game directory that lmm did not deploy, and lmm never writes through a link",
		overrideWarning(t, warnings, "linked.ini"))
	assert.Equal(t, "override shared.ini skipped: it shares its data with another file (a hard link) that lmm did not deploy, and lmm never writes through a link",
		overrideWarning(t, warnings, "shared.ini"))
	for _, w := range warnings {
		assert.NotContains(t, w, "mod", "nothing here is a mod's")
	}
	assert.Equal(t, "theirs", readFile(t, theirs))
	assert.Equal(t, "shared", readFile(t, shared))
}

// TestApplyProfileOverrides_RefusesASpecialFileWithoutBlocking: a FIFO at
// an override's path is refused before anything opens it - opening one
// for writing with no reader would block the deploy for good.
func TestApplyProfileOverrides_RefusesASpecialFileWithoutBlocking(t *testing.T) {
	baseDir := t.TempDir()
	game := &domain.Game{InstallPath: baseDir}
	require.NoError(t, syscall.Mkfifo(filepath.Join(baseDir, "pipe.ini"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(baseDir, "dir.ini"), 0o755))

	done := make(chan []string, 1)
	go func() {
		warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{
			"pipe.ini": []byte("override"),
			"dir.ini":  []byte("override"),
		}}, overrideEnv{})
		assert.NoError(t, err)
		done <- warnings
	}()
	select {
	case warnings := <-done:
		assert.Contains(t, overrideWarning(t, warnings, "pipe.ini"), "not a regular file")
		assert.Contains(t, overrideWarning(t, warnings, "dir.ini"), "not a regular file")
	case <-time.After(10 * time.Second):
		t.Fatal("applying an override over a FIFO blocked")
	}
	info, err := os.Lstat(filepath.Join(baseDir, "pipe.ini"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeNamedPipe, "the FIFO is left as it was")
}

// TestOpenDirBeneath_RefusesALinkSwappedIntoThePath is the parent-directory
// race (#630 comment): the caller resolved a path with no link on it, and a
// directory on it was swapped for a link to somewhere else before the
// write. The walk refuses the link rather than following it.
func TestOpenDirBeneath_RefusesALinkSwappedIntoThePath(t *testing.T) {
	baseDir, outside := t.TempDir(), t.TempDir()
	// What the caller resolved: <base>/Config/sub, all real directories.
	// What is there now: Config is a link out of the game directory.
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "sub"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(baseDir, "Config")))

	_, err := openDirBeneath(baseDir, filepath.Join("Config", "sub"), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer a directory")

	dir, err := openDirBeneath(baseDir, filepath.Join("Real", "sub"), true)
	require.NoError(t, err, "a missing real directory is made")
	require.NoError(t, dir.Close())
	info, err := os.Lstat(filepath.Join(baseDir, "Real", "sub"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
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

	warnings, err := applyProfileOverrides(game, &domain.Profile{Overrides: map[string][]byte{"linked.ini": []byte("override")}}, overrideEnv{})
	require.NoError(t, err)
	require.Empty(t, warnings)
	info, err := os.Lstat(filepath.Join(baseDir, "linked.ini"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the link was replaced by a regular file")
	got, err := os.ReadFile(inside)
	require.NoError(t, err)
	assert.Equal(t, "stock", string(got), "the link's target was not written through")
}

// TestWriteOverriddenCopy_IntoAModPathNotYetMade: the first deploy of a
// game whose mod path does not exist yet places its overridden file as it
// would its link, making the directories on the way.
func TestWriteOverriddenCopy_IntoAModPathNotYetMade(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mods", "deeper")
	require.NoError(t, writeOverriddenCopy(root, "Data/k.ini", []byte("tweaked")))
	assert.Equal(t, "tweaked", readFile(t, filepath.Join(root, "Data", "k.ini")))
}

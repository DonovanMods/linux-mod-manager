package config_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Sys().(*syscall.Stat_t).Ino
}

// dirNames lists dir, so a test can see a temporary file left behind.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

var atomicGame = &domain.Game{ID: "a", Name: "A", InstallPath: "/a", ModPath: "/a/mods"}

// TestSaveGame_ReplacesGamesYAMLByRename is #403: games.yaml is replaced
// whole by a rename, never truncated in place, so a reader in another
// process sees the old file or the new one. A new inode is what a rename
// leaves behind; a truncating write keeps the old one.
func TestSaveGame_ReplacesGamesYAMLByRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "games.yaml")
	require.NoError(t, config.SaveGame(dir, atomicGame))
	before := inode(t, path)

	require.NoError(t, config.SaveGame(dir, &domain.Game{ID: "b", Name: "B", InstallPath: "/b"}))
	assert.NotEqual(t, before, inode(t, path), "games.yaml must be renamed into place, not rewritten in place")
	assert.Equal(t, []string{"games.yaml"}, dirNames(t, dir), "no temporary file may be left behind")

	before = inode(t, path)
	require.NoError(t, config.DeleteGame(dir, "b"))
	assert.NotEqual(t, before, inode(t, path))
	assert.Equal(t, []string{"games.yaml"}, dirNames(t, dir))
}

func TestSaveGame_KeepsGamesYAMLsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "games.yaml")
	require.NoError(t, config.SaveGame(dir, atomicGame))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "a new games.yaml is 0644, as it always was")

	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, config.SaveGame(dir, &domain.Game{ID: "b", Name: "B", InstallPath: "/b"}))
	info, err = os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestSaveGame_WritesThroughASymlinkedGamesYAML: a dotfile manager's link
// survives, and its target is what changes.
func TestSaveGame_WritesThroughASymlinkedGamesYAML(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(t.TempDir(), "games.yaml")
	require.NoError(t, os.WriteFile(real, []byte("games:\n"), 0o644))
	link := filepath.Join(dir, "games.yaml")
	require.NoError(t, os.Symlink(real, link))

	require.NoError(t, config.SaveGame(dir, atomicGame))

	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSymlink, info.Mode()&os.ModeSymlink, "the link itself must survive")
	games, err := config.LoadGames(filepath.Dir(real))
	require.NoError(t, err)
	assert.Contains(t, games, "a")
}

// TestSaveGame_RefusesAReadOnlyGamesYAML: a rename only needs the directory
// to be writable, so without a check a read-only games.yaml - which the
// truncating write it replaces refused - would be replaced anyway.
func TestSaveGame_RefusesAReadOnlyGamesYAML(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "games.yaml")
	require.NoError(t, os.WriteFile(path, []byte("games:\n"), 0o444))

	err := config.SaveGame(dir, atomicGame)
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrPermission)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "games:\n", string(data))
	assert.Equal(t, []string{"games.yaml"}, dirNames(t, dir))
}

// TestSaveGame_AnUnwritableDirectoryFallsBackToInPlace: a writable
// games.yaml in a directory lmm cannot create the temporary file in is
// still saved, the way it always was, rather than refused.
func TestSaveGame_AnUnwritableDirectoryFallsBackToInPlace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "games.yaml")
	require.NoError(t, os.WriteFile(path, []byte("games:\n"), 0o644))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	require.NoError(t, config.SaveGame(dir, atomicGame))
	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Contains(t, games, "a")
	assert.Equal(t, []string{"games.yaml"}, dirNames(t, dir))
}

// TestConfigSave_ReplacesConfigYAMLByRename: config.yaml shares games.yaml's
// writer (#403).
func TestConfigSave_ReplacesConfigYAMLByRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{}
	require.NoError(t, cfg.Save(dir))
	before := inode(t, path)

	require.NoError(t, cfg.Save(dir))
	assert.NotEqual(t, before, inode(t, path))
	assert.Equal(t, []string{"config.yaml"}, dirNames(t, dir))
}

package steam

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staleLibraryRoot lays out a sandboxed Steam root whose libraryfolders.vdf
// names a second library that is not there - a drive that was unplugged, or
// a library removed outside Steam. HOME and STEAM_ROOT point at it, so
// nothing reads the machine's real install.
func staleLibraryRoot(t *testing.T) (root, missing string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root = filepath.Join(home, "Steam")
	t.Setenv("STEAM_ROOT", root)
	missing = filepath.Join(home, "unplugged", "SteamLibrary")

	steamapps := filepath.Join(root, "steamapps")
	require.NoError(t, os.MkdirAll(steamapps, 0o755))
	vdf := "\n\"libraryfolders\"\n{\n\t\"0\"\n\t{\n\t\t\"path\"\t\t\"" + root +
		"\"\n\t}\n\t\"1\"\n\t{\n\t\t\"path\"\t\t\"" + missing + "\"\n\t}\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "libraryfolders.vdf"), []byte(vdf), 0o644))
	return root, missing
}

// #368 (minor): a libraryfolders.vdf entry whose directory is gone printed
// as `Warning: /data/Games/SteamLibrary/steamapps: open ...: no such file
// or directory` on EVERY run of `lmm game detect`. It is honest, it is
// nothing lmm did, and there is nothing for the user to fix - so it stops
// being a scan warning and becomes an Info-level log line that names
// libraryfolders.vdf as the thing that listed the missing directory.
func TestDetectGames_StaleLibraryIsANoticeNotAWarning(t *testing.T) {
	_, missing := staleLibraryRoot(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	_, warnings, err := DetectGames(t.TempDir(), DetectOptions{Logger: log})
	require.NoError(t, err)

	assert.Empty(t, warnings, "a library the user cannot do anything about is not a scan warning")
	logged := buf.String()
	assert.Contains(t, logged, "libraryfolders.vdf")
	assert.Contains(t, logged, missing)
	assert.Equal(t, 1, strings.Count(logged, "libraryfolders.vdf"), "said once per scan, not once per app")
}

// A library that exists but cannot be READ is a different fact: something
// is wrong with a directory that is really there (permissions, a broken
// mount), so it stays a warning the user sees without asking for logs.
func TestDetectGames_UnreadableLibraryStaysAWarning(t *testing.T) {
	root, missing := staleLibraryRoot(t)
	_ = root
	require.NoError(t, os.MkdirAll(filepath.Join(missing, "steamapps"), 0o755))
	require.NoError(t, os.Chmod(filepath.Join(missing, "steamapps"), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(missing, "steamapps"), 0o755) })

	_, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], missing)
}

// A nil Logger is the ordinary case (every existing caller), and must not
// panic: the notice is simply dropped.
func TestDetectGames_StaleLibraryWithNoLogger(t *testing.T) {
	staleLibraryRoot(t)
	_, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	assert.Empty(t, warnings)
}

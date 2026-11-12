package steam_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #605: detection reports the app's Steam compatdata prefix, probing every
// library libraryfolders.vdf lists - not only the one the game is
// installed in, since compatdata stays behind when a game moves. It is
// information for the detect listing; lmm never writes it to games.yaml.

// writeLibraryFolders lists libs in root's libraryfolders.vdf, in order.
func writeLibraryFolders(t *testing.T, root string, libs ...string) {
	t.Helper()
	vdf := "\"libraryfolders\"\n{\n"
	for i, lib := range libs {
		vdf += "\t\"" + string(rune('0'+i)) + "\"\n\t{\n\t\t\"path\"\t\t\"" + lib + "\"\n\t}\n"
	}
	vdf += "}\n"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "steamapps"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"), []byte(vdf), 0o644))
}

// writeCompatdata makes <lib>/steamapps/compatdata/<appID>/pfx a Proton
// prefix stamped mod, and returns the pfx path.
func writeCompatdata(t *testing.T, lib, appID string, mod time.Time) string {
	t.Helper()
	pfx := filepath.Join(lib, "steamapps", "compatdata", appID, "pfx")
	require.NoError(t, os.MkdirAll(filepath.Join(pfx, "drive_c", "users", "steamuser"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pfx, "system.reg"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(pfx), "version"), []byte("9.0-4\n"), 0o644))
	for _, p := range []string{filepath.Join(pfx, "system.reg"), pfx} {
		require.NoError(t, os.Chtimes(p, mod, mod))
	}
	return pfx
}

func TestDetectGames_ReportsThePrefixFromAnotherLibrarysCompatdata(t *testing.T) {
	root := steamFixture(t, "1149460", "Icarus", "Icarus")
	other := filepath.Join(t.TempDir(), "OtherLibrary")
	writeLibraryFolders(t, root, root, other)
	pfx := writeCompatdata(t, other, "1149460", time.Now())

	got := detectOne(t, steam.DetectOptions{})
	assert.Equal(t, pfx, got.Prefix)
}

func TestDetectGames_ReportsTheMostRecentlyUsedPrefix(t *testing.T) {
	root := steamFixture(t, "1149460", "Icarus", "Icarus")
	other := filepath.Join(t.TempDir(), "OtherLibrary")
	writeLibraryFolders(t, root, root, other)
	writeCompatdata(t, root, "1149460", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := writeCompatdata(t, other, "1149460", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	assert.Equal(t, newer, detectOne(t, steam.DetectOptions{}).Prefix)
}

func TestDetectGames_NoCompatdataNoPrefix(t *testing.T) {
	steamFixture(t, "1149460", "Icarus", "Icarus")
	assert.Empty(t, detectOne(t, steam.DetectOptions{}).Prefix, "a native game, or one never run, has no prefix to report")
}

func TestAllLibraryPaths_EveryLibraryOfEveryRoot(t *testing.T) {
	root := steamFixture(t, "1149460", "Icarus", "Icarus")
	other := filepath.Join(t.TempDir(), "OtherLibrary")
	writeLibraryFolders(t, root, root, other)
	assert.Equal(t, []string{root, other}, steam.AllLibraryPaths())
}

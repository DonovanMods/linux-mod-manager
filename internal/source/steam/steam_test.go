package steam

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GetLibraryPaths / getLibraryPathsFromMap (steam.go) ---

func TestGetLibraryPaths_NoVDFFile_FallsBackToRootItself(t *testing.T) {
	root := t.TempDir()

	paths, err := GetLibraryPaths(root)
	require.NoError(t, err)
	assert.Equal(t, []string{root}, paths)
}

func TestGetLibraryPaths_ValidVDF_ReturnsListedLibraries(t *testing.T) {
	root := t.TempDir()
	steamapps := filepath.Join(root, "steamapps")
	require.NoError(t, os.MkdirAll(steamapps, 0755))

	vdf := `
"libraryfolders"
{
	"0"
	{
		"path"		"` + root + `"
	}
	"1"
	{
		"path"		"/mnt/extra-library"
	}
}
`
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "libraryfolders.vdf"), []byte(vdf), 0644))

	paths, err := GetLibraryPaths(root)
	require.NoError(t, err)
	assert.Equal(t, []string{root, "/mnt/extra-library"}, paths)
}

func TestGetLibraryPaths_MalformedVDF_ReturnsError(t *testing.T) {
	root := t.TempDir()
	steamapps := filepath.Join(root, "steamapps")
	require.NoError(t, os.MkdirAll(steamapps, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "libraryfolders.vdf"), []byte(`"unterminated`), 0644))

	_, err := GetLibraryPaths(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing libraryfolders")
}

func TestGetLibraryPaths_ParsedButNoLibraries_FallsBackToRoot(t *testing.T) {
	root := t.TempDir()
	steamapps := filepath.Join(root, "steamapps")
	require.NoError(t, os.MkdirAll(steamapps, 0755))
	// Valid VDF, but no "libraryfolders" block at all: getLibraryPathsFromMap
	// returns nil, so GetLibraryPaths must fall back to the root itself.
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "libraryfolders.vdf"), []byte(`"somethingelse" { }`), 0644))

	paths, err := GetLibraryPaths(root)
	require.NoError(t, err)
	assert.Equal(t, []string{root}, paths)
}

func TestGetLibraryPathsFromMap(t *testing.T) {
	root := VDFMap{
		"libraryfolders": VDFMap{
			"0": VDFMap{"path": "/a"},
			"1": VDFMap{"path": "/b"},
		},
	}
	assert.Equal(t, []string{"/a", "/b"}, getLibraryPathsFromMap(root))
}

// --- FindSteamRoots (steam.go) ---
//
// FindSteamRoots is hardwired to os.UserHomeDir() (which reads $HOME on
// Linux) plus the STEAM_ROOT env var, so these tests override both via
// t.Setenv rather than any code seam, per the task brief.

func TestFindSteamRoots_NoneExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("STEAM_ROOT", "")

	assert.Empty(t, FindSteamRoots())
}

func TestFindSteamRoots_DotSteamPath(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".steam", "steam"), 0755))
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")

	roots := FindSteamRoots()
	assert.Equal(t, []string{filepath.Join(home, ".steam", "steam")}, roots)
}

func TestFindSteamRoots_LocalShareSteamPath(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".local", "share", "Steam"), 0755))
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")

	roots := FindSteamRoots()
	assert.Equal(t, []string{filepath.Join(home, ".local", "share", "Steam")}, roots)
}

func TestFindSteamRoots_BothPaths_DotSteamFirst(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".steam", "steam"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".local", "share", "Steam"), 0755))
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")

	roots := FindSteamRoots()
	assert.Equal(t, []string{
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".local", "share", "Steam"),
	}, roots)
}

func TestFindSteamRoots_STEAMROOTEnv_PrependedFirst(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".steam", "steam"), 0755))
	extraRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", extraRoot)

	roots := FindSteamRoots()
	assert.Equal(t, []string{extraRoot, filepath.Join(home, ".steam", "steam")}, roots)
}

func TestFindSteamRoots_STEAMROOTEnv_NonexistentPathSkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", filepath.Join(home, "does-not-exist"))

	assert.Empty(t, FindSteamRoots())
}

// TestFindSteamRoots_SymlinkedDuplicate_ReturnsOnlyOne guards #190 item 3:
// on many real Linux Steam installs, ~/.steam/steam is a symlink to
// ~/.local/share/Steam - both candidate paths exist and both pass FindSteamRoots'
// existence check, but they are the SAME real directory. Returning both
// made DetectGames scan (and warn about) that one real library twice. Since
// the two roots resolve to the same real path, only the first (".steam/steam",
// this package's own priority order) should survive.
func TestFindSteamRoots_SymlinkedDuplicate_ReturnsOnlyOne(t *testing.T) {
	home := t.TempDir()
	realSteam := filepath.Join(home, ".local", "share", "Steam")
	require.NoError(t, os.MkdirAll(realSteam, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".steam"), 0755))
	require.NoError(t, os.Symlink(realSteam, filepath.Join(home, ".steam", "steam")))
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")

	roots := FindSteamRoots()
	assert.Equal(t, []string{filepath.Join(home, ".steam", "steam")}, roots,
		"a symlinked duplicate of an already-listed root must not appear twice")
}

// --- DetectGames (steam.go), against a fabricated library tree ---

// writeAppManifest writes a minimal appmanifest_<appid>.acf into steamapps.
func writeAppManifest(t *testing.T, steamapps, appID, installDir string) {
	t.Helper()
	acf := `
"AppState"
{
	"appid"		"` + appID + `"
	"name"		"` + installDir + `"
	"installdir"		"` + installDir + `"
}
`
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "appmanifest_"+appID+".acf"), []byte(acf), 0644))
}

func TestDetectGames_NoSteamRoots_ReturnsAllNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("STEAM_ROOT", "")

	games, warnings, err := DetectGames(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, games)
	assert.Nil(t, warnings)
}

func TestDetectGames_FindsKnownGame(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")

	steamapps := filepath.Join(home, ".steam", "steam", "steamapps")
	installDir := filepath.Join(steamapps, "common", "Skyrim Special Edition")
	require.NoError(t, os.MkdirAll(installDir, 0755))
	writeAppManifest(t, steamapps, "489830", "Skyrim Special Edition") // known: skyrim-se

	games, warnings, err := DetectGames(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, games, 1)
	assert.Equal(t, "489830", games[0].SteamAppID)
	assert.Equal(t, "skyrim-se", games[0].Slug)
	assert.Equal(t, "Skyrim Special Edition", games[0].Name)
	assert.Equal(t, installDir, games[0].InstallPath)
	assert.Equal(t, filepath.Join(installDir, "Data"), games[0].ModPath)
	assert.Equal(t, "skyrimspecialedition", games[0].NexusID)
}

func TestDetectGames_UnknownAppID_SilentlySkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")

	steamapps := filepath.Join(home, ".steam", "steam", "steamapps")
	require.NoError(t, os.MkdirAll(filepath.Join(steamapps, "common", "SomeGame"), 0755))
	writeAppManifest(t, steamapps, "999999999", "SomeGame") // not in the known-games list

	games, warnings, err := DetectGames(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Empty(t, games)
}

func TestDetectGames_MissingInstallDir_Warns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")

	steamapps := filepath.Join(home, ".steam", "steam", "steamapps")
	require.NoError(t, os.MkdirAll(steamapps, 0755))
	// Deliberately no steamapps/common/<installdir> directory.
	writeAppManifest(t, steamapps, "489830", "Skyrim Special Edition")

	games, warnings, err := DetectGames(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, games)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "install dir missing")
}

// TestDetectGames_SymlinkedDuplicateRoot_WarnsOnce is the end-to-end guard
// for #190 item 3 at the reported symptom's own level: `lmm game detect`
// printed each stale-library warning twice against a real Linux install
// where ~/.steam/steam symlinks to ~/.local/share/Steam - both roots exist,
// so DetectGames' library scan (and every warning it produces) used to run
// twice against the identical, real directory. FindSteamRoots' resolved-path
// dedup means the second, symlinked root never reaches the scan at all.
func TestDetectGames_SymlinkedDuplicateRoot_WarnsOnce(t *testing.T) {
	home := t.TempDir()
	realSteam := filepath.Join(home, ".local", "share", "Steam")
	steamapps := filepath.Join(realSteam, "steamapps")
	require.NoError(t, os.MkdirAll(steamapps, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".steam"), 0755))
	require.NoError(t, os.Symlink(realSteam, filepath.Join(home, ".steam", "steam")))
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")

	// Deliberately no steamapps/common/<installdir> directory - the same
	// "stale library" shape TestDetectGames_MissingInstallDir_Warns uses.
	writeAppManifest(t, steamapps, "489830", "Skyrim Special Edition")

	games, warnings, err := DetectGames(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, games)
	require.Len(t, warnings, 1, "a symlinked duplicate root must not double every warning: got %v", warnings)
	assert.Contains(t, warnings[0], "install dir missing")
}

func TestDetectGames_DedupsSameGameAcrossLibraries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")

	steamRoot := filepath.Join(home, ".steam", "steam")
	steamapps := filepath.Join(steamRoot, "steamapps")
	require.NoError(t, os.MkdirAll(filepath.Join(steamapps, "common", "Skyrim Special Edition"), 0755))
	writeAppManifest(t, steamapps, "489830", "Skyrim Special Edition")

	// A second library (discovered via libraryfolders.vdf) also has the same game installed.
	extraLib := t.TempDir()
	extraSteamapps := filepath.Join(extraLib, "steamapps")
	require.NoError(t, os.MkdirAll(filepath.Join(extraSteamapps, "common", "Skyrim Special Edition"), 0755))
	writeAppManifest(t, extraSteamapps, "489830", "Skyrim Special Edition")

	vdf := `
"libraryfolders"
{
	"0"
	{
		"path"		"` + steamRoot + `"
	}
	"1"
	{
		"path"		"` + extraLib + `"
	}
}
`
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "libraryfolders.vdf"), []byte(vdf), 0644))

	games, warnings, err := DetectGames(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, games, 1, "the same slug found in a second library must be deduped")
}

// TestDetectGames_IcarusEntry_IncludesDeployModeAndSources pins #177: a
// detected Icarus install carries the new DeployMode/Sources fields through
// from the known-games entry, and its ModPath is joined exactly like every
// other detected game's (installPath + the known entry's relative mod_path,
// here "Icarus/Content/Paks/mods" — matching the README's hand-written
// example, which this detection path now generates instead of requiring by
// hand).
func TestDetectGames_IcarusEntry_IncludesDeployModeAndSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")

	steamapps := filepath.Join(home, ".steam", "steam", "steamapps")
	installDir := filepath.Join(steamapps, "common", "Icarus")
	require.NoError(t, os.MkdirAll(installDir, 0755))
	writeAppManifest(t, steamapps, "1149460", "Icarus")

	games, warnings, err := DetectGames(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, games, 1)
	g := games[0]
	assert.Equal(t, "1149460", g.SteamAppID)
	assert.Equal(t, "icarus", g.Slug)
	assert.Equal(t, "Icarus", g.Name)
	assert.Equal(t, installDir, g.InstallPath)
	assert.Equal(t, filepath.Join(installDir, "Icarus", "Content", "Paks", "mods"), g.ModPath)
	assert.Equal(t, "", g.NexusID)
	assert.Equal(t, "compile", g.DeployMode)
	assert.Equal(t, map[string]string{"icarus": "icarus"}, g.Sources)
}

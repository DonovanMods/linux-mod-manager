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

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
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

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
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

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
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

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
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

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
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

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
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

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
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
	// #412: the curated adapter reaches the candidate, so `game detect`
	// reports it and `game add --from-detected` writes it.
	assert.Equal(t, "icarus", g.Adapter)
	assert.Equal(t, map[string]string{"icarus": "icarus"}, g.Sources)
}

// --- #206: detecting every installed game, not only curated ones ---

// writeAppManifestNamed writes a manifest whose Steam "name" differs from
// its installdir - the shape every real appmanifest has (Steam's display
// name is not the directory name) and the one the unknown-candidate path
// reads its display name and derived slug from.
func writeAppManifestNamed(t *testing.T, steamapps, appID, name, installDir string) {
	t.Helper()
	acf := `
"AppState"
{
	"appid"		"` + appID + `"
	"name"		"` + name + `"
	"installdir"		"` + installDir + `"
}
`
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "appmanifest_"+appID+".acf"), []byte(acf), 0644))
}

// fakeSteamLibrary makes a sandboxed Steam root under t.TempDir() and points
// HOME at it, so every DetectGames test below scans a fabricated library and
// never the host's real one.
func fakeSteamLibrary(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")
	steamapps := filepath.Join(home, ".steam", "steam", "steamapps")
	require.NoError(t, os.MkdirAll(steamapps, 0755))
	return steamapps
}

// installApp creates the steamapps/common/<dir> tree a manifest points at.
func installApp(t *testing.T, steamapps, dir string) string {
	t.Helper()
	p := filepath.Join(steamapps, "common", dir)
	require.NoError(t, os.MkdirAll(p, 0755))
	return p
}

func TestDeriveSlug(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "Satisfactory", "satisfactory"},
		{"spaces", "Euro Truck Simulator 2", "euro-truck-simulator-2"},
		{"punctuation collapses", "S.T.A.L.K.E.R. 2: Heart of Chornobyl", "s-t-a-l-k-e-r-2-heart-of-chornobyl"},
		{"apostrophe", "Baldur's Gate 3", "baldur-s-gate-3"},
		{"path separators cannot survive", "Some/Game\\Name", "some-game-name"},
		{"dots cannot become a traversal", "..", ""},
		{"trims edge dashes", "  Hades II  ", "hades-ii"},
		{"non-ascii is dropped", "Ōkami", "kami"},
		{"nothing usable", "！！！", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, deriveSlug(tt.in))
		})
	}
}

func TestIsSteamTool(t *testing.T) {
	tools := []string{
		"Steamworks Common Redistributables",
		"Steamworks Shared",
		"Proton 9.0 (Beta)",
		"Proton 4.11-13",
		"Proton Experimental",
		"Proton Hotfix",
		"Proton EasyAntiCheat Runtime",
		"Steam Linux Runtime 3.0 (sniper)",
		"steam linux runtime 2.0 (soldier)",
	}
	for _, name := range tools {
		assert.Truef(t, isSteamTool(name), "%q should be recognised as a Steam tool", name)
	}
	games := []string{
		"Protonwar",        // a real game whose name merely starts with "Proton"
		"Proton Pulse",     // ... and one that starts with the whole word
		"Steamworld Dig 2", // ... and one that starts with "Steamwor"
		"Half-Life 2",
	}
	for _, name := range games {
		assert.Falsef(t, isSteamTool(name), "%q is a game, not a Steam tool", name)
	}
}

// TestDetectGames_KnownGame_CarriesKnownFlag pins the additive flag on
// today's unchanged known-only path: a curated match is Known.
func TestDetectGames_KnownGame_CarriesKnownFlag(t *testing.T) {
	steamapps := fakeSteamLibrary(t)
	installApp(t, steamapps, "Skyrim Special Edition")
	writeAppManifest(t, steamapps, "489830", "Skyrim Special Edition")

	games, _, err := DetectGames(t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	require.Len(t, games, 1)
	assert.True(t, games[0].Known)
}

// The unknown-game stand-in for every test below that needs an app lmm has
// no known-games entry for. It is a fictional app id and title on purpose:
// these tests used to borrow a real uncurated game (Satisfactory), which
// meant that curating it (#406) silently turned "an app lmm has never heard
// of" into "an app it has", and three tests failed for a reason that had
// nothing to do with what they were asserting. Nothing will ever curate
// this one.
const (
	uncuratedAppID = "9999990"
	uncuratedName  = "Uncurated Example Game"
	uncuratedDir   = "UncuratedExampleGame"
)

// TestDetectGames_IncludeUnknown_ReturnsEveryInstalledApp is #206's core
// claim: with the option set, an app id lmm has never heard of comes back
// as a candidate carrying the manifest's own name, its install path and a
// derived slug - with NO mod path and NO sources, because nothing on disk
// says where that game keeps its mods.
func TestDetectGames_IncludeUnknown_ReturnsEveryInstalledApp(t *testing.T) {
	steamapps := fakeSteamLibrary(t)
	knownInstall := installApp(t, steamapps, "Skyrim Special Edition")
	writeAppManifest(t, steamapps, "489830", "Skyrim Special Edition")
	unknownInstall := installApp(t, steamapps, uncuratedDir)
	writeAppManifestNamed(t, steamapps, uncuratedAppID, uncuratedName, uncuratedDir)

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{IncludeUnknown: true})
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, games, 2)

	byID := map[string]DetectedGame{}
	for _, g := range games {
		byID[g.SteamAppID] = g
	}

	known := byID["489830"]
	assert.True(t, known.Known)
	assert.Equal(t, "skyrim-se", known.Slug)
	assert.Equal(t, filepath.Join(knownInstall, "Data"), known.ModPath)

	unknown := byID[uncuratedAppID]
	assert.False(t, unknown.Known)
	assert.Equal(t, "uncurated-example-game", unknown.Slug)
	assert.Equal(t, uncuratedName, unknown.Name)
	assert.Equal(t, unknownInstall, unknown.InstallPath)
	assert.Empty(t, unknown.ModPath, "an unknown game's mod path is a frontend's guess, not a detection claim")
	assert.Empty(t, unknown.Sources)
	assert.Empty(t, unknown.NexusID)
}

// TestDetectGames_DefaultStaysKnownOnly guards the option's whole point:
// without it, nothing about today's listing changes.
func TestDetectGames_DefaultStaysKnownOnly(t *testing.T) {
	steamapps := fakeSteamLibrary(t)
	installApp(t, steamapps, uncuratedDir)
	writeAppManifestNamed(t, steamapps, uncuratedAppID, uncuratedName, uncuratedDir)

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Empty(t, games)
}

// TestDetectGames_IncludeUnknown_SkipsSteamTools keeps the list a list of
// GAMES: Steam's own runtimes and redistributables install as ordinary apps
// under steamapps/common and would otherwise dominate it.
func TestDetectGames_IncludeUnknown_SkipsSteamTools(t *testing.T) {
	steamapps := fakeSteamLibrary(t)
	for _, tool := range []struct{ appID, name, dir string }{
		{"228980", "Steamworks Common Redistributables", "Steamworks Shared"},
		{"2805730", "Proton Experimental", "Proton - Experimental"},
		{"1628350", "Steam Linux Runtime 3.0 (sniper)", "SteamLinuxRuntime_sniper"},
	} {
		installApp(t, steamapps, tool.dir)
		writeAppManifestNamed(t, steamapps, tool.appID, tool.name, tool.dir)
	}
	installApp(t, steamapps, uncuratedDir)
	writeAppManifestNamed(t, steamapps, uncuratedAppID, uncuratedName, uncuratedDir)

	games, _, err := DetectGames(t.TempDir(), DetectOptions{IncludeUnknown: true})
	require.NoError(t, err)
	require.Len(t, games, 1, "only the game should survive the tool deny-list: %+v", games)
	assert.Equal(t, uncuratedAppID, games[0].SteamAppID)
}

// TestDetectGames_IncludeUnknown_SlugsAreUnique covers both collisions a
// derived slug can hit: another unknown game with the same name, and a
// curated slug already spoken for. The app id disambiguates, since it is
// the one thing Steam guarantees is unique.
func TestDetectGames_IncludeUnknown_SlugsAreUnique(t *testing.T) {
	steamapps := fakeSteamLibrary(t)
	installApp(t, steamapps, "GameA")
	writeAppManifestNamed(t, steamapps, "111", "Some Game", "GameA")
	installApp(t, steamapps, "GameB")
	writeAppManifestNamed(t, steamapps, "222", "Some Game", "GameB")
	installApp(t, steamapps, "GameC")
	writeAppManifestNamed(t, steamapps, "333", "skyrim-se", "GameC") // collides with a curated slug

	games, _, err := DetectGames(t.TempDir(), DetectOptions{IncludeUnknown: true})
	require.NoError(t, err)
	require.Len(t, games, 3)

	slugs := map[string]string{}
	for _, g := range games {
		require.NotEmpty(t, g.Slug)
		require.NotContainsf(t, slugs, g.Slug, "duplicate slug %q (app %s and %s)", g.Slug, slugs[g.Slug], g.SteamAppID)
		slugs[g.Slug] = g.SteamAppID
	}
	assert.NotContains(t, slugs, "skyrim-se", "a curated slug must not be taken by an unknown game")
}

// TestDetectGames_IncludeUnknown_UnnamedApp_FallsBackToInstallDir: a
// manifest with no "name" (or one whose name derives to nothing usable)
// still has to produce a usable row.
func TestDetectGames_IncludeUnknown_UnnamedApp_FallsBackToInstallDir(t *testing.T) {
	steamapps := fakeSteamLibrary(t)
	installApp(t, steamapps, "MysteryGame")
	writeAppManifestNamed(t, steamapps, "444", "", "MysteryGame")
	installApp(t, steamapps, "Unslug")
	writeAppManifestNamed(t, steamapps, "555", "！！！", "Unslug")

	games, _, err := DetectGames(t.TempDir(), DetectOptions{IncludeUnknown: true})
	require.NoError(t, err)
	require.Len(t, games, 2)
	byID := map[string]DetectedGame{}
	for _, g := range games {
		byID[g.SteamAppID] = g
	}
	assert.Equal(t, "MysteryGame", byID["444"].Name)
	assert.Equal(t, "mysterygame", byID["444"].Slug)
	assert.Equal(t, "！！！", byID["555"].Name, "the display name is the manifest's, however unsluggable")
	assert.Equal(t, "app-555", byID["555"].Slug)
}

// TestDetectGames_IncludeUnknown_StaleManifest_SkippedSilently: a manifest
// whose install dir is gone is a warning for a KNOWN game (the user asked
// lmm to configure it) but silent noise for an unknown one - a big library
// carries plenty of those, and none of them is actionable.
func TestDetectGames_IncludeUnknown_StaleManifest_SkippedSilently(t *testing.T) {
	steamapps := fakeSteamLibrary(t)
	writeAppManifestNamed(t, steamapps, uncuratedAppID, uncuratedName, uncuratedDir) // no common/ dir

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{IncludeUnknown: true})
	require.NoError(t, err)
	assert.Empty(t, games)
	assert.Empty(t, warnings)
}

// TestDetectGames_IncludeUnknown_DedupesAcrossLibraries: the same unknown
// app installed in two libraries is one candidate, keyed by app id (a
// known game dedupes by its curated slug; an unknown one has no such key).
func TestDetectGames_IncludeUnknown_DedupesAcrossLibraries(t *testing.T) {
	steamapps := fakeSteamLibrary(t)
	steamRoot := filepath.Dir(steamapps)
	installApp(t, steamapps, uncuratedDir)
	writeAppManifestNamed(t, steamapps, uncuratedAppID, uncuratedName, uncuratedDir)

	extraLib := t.TempDir()
	extraSteamapps := filepath.Join(extraLib, "steamapps")
	require.NoError(t, os.MkdirAll(extraSteamapps, 0755))
	installApp(t, extraSteamapps, uncuratedDir)
	writeAppManifestNamed(t, extraSteamapps, uncuratedAppID, uncuratedName, uncuratedDir)

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

	games, _, err := DetectGames(t.TempDir(), DetectOptions{IncludeUnknown: true})
	require.NoError(t, err)
	require.Len(t, games, 1)
}

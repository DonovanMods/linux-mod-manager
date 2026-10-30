package battlenet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sandbox points HOME at a fresh directory and clears every other place
// prefix discovery looks, so a test sees only the prefixes it makes.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WINEPREFIX", "")
	t.Setenv("STEAM_ROOT", "")
	t.Setenv("XDG_DATA_HOME", "")
	return home
}

// installProton makes an installed Proton build where umu-run looks for
// one by name: Steam's compatibilitytools.d under the data home.
func installProton(t *testing.T, home, name string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".local", "share", "Steam", "compatibilitytools.d", name), 0o755))
}

// copyFixture copies testdata/<name> to dst.
func copyFixture(t *testing.T, name, dst string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(dst, data, 0o644))
}

// writeFile writes content to path, making its directory.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// umuPrefix lays out the owner's real prefix (#545's fixtures, trimmed and
// sanitized): a umu/Proton prefix - pfx -> ., a version file - with
// Battle.net and a World of Warcraft root whose one product is wowf, in
// _forever_, beside a stale _classic_beta_ folder no .build.info row
// names. Its system.reg has no WoW Uninstall key at all.
func umuPrefix(t *testing.T, prefix string) (wowRoot string) {
	t.Helper()
	pf := filepath.Join(prefix, "drive_c", "Program Files (x86)")
	wowRoot = filepath.Join(pf, "World of Warcraft")
	copyFixture(t, "umu-prefix/system.reg", filepath.Join(prefix, "system.reg"))
	copyFixture(t, "umu-prefix/.build.info", filepath.Join(wowRoot, ".build.info"))
	for _, dir := range []string{"_forever_/Interface/AddOns", "_classic_beta_/Interface"} {
		require.NoError(t, os.MkdirAll(filepath.Join(wowRoot, dir), 0o755))
	}
	writeFile(t, filepath.Join(pf, "Battle.net", "Battle.net Launcher.exe"), "")
	writeFile(t, filepath.Join(prefix, "version"), "GE-Proton11-11\n")
	require.NoError(t, os.Symlink(".", filepath.Join(prefix, "pfx")))
	return wowRoot
}

// multiFlavorBuildInfo is a WoW root with three flavors installed - a
// synthetic file in the real header's shape.
const multiFlavorBuildInfo = "Branch!STRING:0|Active!DEC:1|Build Key!HEX:16|Version!STRING:0|Product!STRING:0\n" +
	"us|1||11.2.5.63906|wow\n" +
	"us|1||5.5.1.63538|wow_classic\n" +
	"us|1||1.15.7.63696|wow_classic_era\n"

// regKey is one Uninstall key in system.reg's text format.
func regKey(name, displayName, installLocation string) string {
	return "[Software\\\\Wow6432Node\\\\Microsoft\\\\Windows\\\\CurrentVersion\\\\Uninstall\\\\" + name + "] 1790095745\n" +
		"#time=1dd4ab2475962f8\n" +
		"\"DisplayName\"=\"" + displayName + "\"\n" +
		"\"InstallLocation\"=\"" + strings.ReplaceAll(installLocation, `\`, `\\`) + "\"\n\n"
}

// winePrefix makes a plain Wine prefix (no Proton markers) whose system.reg
// holds keys.
func winePrefix(t *testing.T, prefix string, keys ...string) {
	t.Helper()
	writeFile(t, filepath.Join(prefix, "system.reg"), "WINE REGISTRY Version 2\n;; All keys relative to REGISTRY\\\\Machine\n\n#arch=win64\n\n"+strings.Join(keys, ""))
	require.NoError(t, os.MkdirAll(filepath.Join(prefix, "drive_c"), 0o755))
}

// TestDetectGames_OwnersUmuPrefix is the owner's prefix: one WoW root, one
// product (wowf), found although no Uninstall key names it, configured from
// the catalog, with the launch prefilled from the prefix.
func TestDetectGames_OwnersUmuPrefix(t *testing.T) {
	home := sandbox(t)
	prefix := filepath.Join(home, "Games", "battlenet")
	wowRoot := umuPrefix(t, prefix)
	installProton(t, home, "GE-Proton11-11")

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, games, 1, "_classic_beta_ has no .build.info row, so it is no game")
	g := games[0]
	assert.Equal(t, domain.DetectorBattleNet, g.Detector)
	assert.Equal(t, "wowf", g.DetectorID)
	assert.Equal(t, "battlenet:wowf", g.Ref())
	assert.Empty(t, g.SteamAppID)
	assert.Equal(t, "wow-forever", g.Slug)
	assert.Equal(t, "World of Warcraft - Forever", g.Name)
	assert.Equal(t, filepath.Join(wowRoot, "_forever_"), g.InstallPath)
	assert.Equal(t, filepath.Join(wowRoot, "_forever_", "Interface", "AddOns"), g.ModPath)
	assert.Equal(t, map[string]string{"curseforge": "1"}, g.Sources)
	assert.Nil(t, g.CurseForge, "the forever flavor's CurseForge version type is unknown, so it takes every file")
	assert.True(t, g.Known)
	assert.Equal(t, prefix, g.WinePrefix)
	require.NotNil(t, g.Launch)
	assert.Equal(t, domain.LaunchViaBattleNet, g.Launch.Via)
	assert.Equal(t, &domain.BattleNetLaunch{Prefix: "~/Games/battlenet", Proton: "GE-Proton11-11"}, g.Launch.BattleNet,
		"no launch code is known for wowf, so Play opens the client; the prefix keeps the exact Proton build that made it")
}

// A prefix made by a GE-Proton build umu-run cannot find by name falls
// back to GE-Proton - umu's newest GE - and the scan says so, since that
// runs the prefix under a different build than made it. The build is found
// in umu's own compatibilitytools folder as well as Steam's.
func TestDetectGames_ProtonBuildNotInstalledFallsBackOutLoud(t *testing.T) {
	home := sandbox(t)
	prefix := filepath.Join(home, "Games", "battlenet")
	umuPrefix(t, prefix)

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	require.Len(t, games, 1)
	assert.Equal(t, "GE-Proton", games[0].Launch.BattleNet.Proton)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "GE-Proton11-11")
	assert.Contains(t, warnings[0], "launch.battlenet.proton")

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".local", "share", "umu", "compatibilitytools", "GE-Proton11-11"), 0o755))
	games, warnings, err = DetectGames(t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Equal(t, "GE-Proton11-11", games[0].Launch.BattleNet.Proton)
}

// A version file naming something that is not a plain build name is never
// joined onto a path: it falls back like a build that is not installed.
func TestProtonFor_RefusesAPathInTheVersionFile(t *testing.T) {
	home := sandbox(t)
	// Where "GE-Proton/../../x" lands when joined onto Steam's folder.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".local", "share", "Steam", "x"), 0o755))
	for _, version := range []string{"GE-Proton/../../x", "GE-Proton.."} {
		prefix := t.TempDir()
		require.NoError(t, os.Symlink(".", filepath.Join(prefix, "pfx")))
		writeFile(t, filepath.Join(prefix, "version"), version+"\n")
		proton, warning := protonFor(prefix, home)
		assert.Equal(t, "GE-Proton", proton, version)
		assert.NotEmpty(t, warning, version)
	}
}

// One root, several flavors: a game per .build.info row, each at its own
// flavor folder with its own CurseForge flavor (#544), found from the
// Uninstall key's InstallLocation on another drive.
func TestDetectGames_OneRootManyFlavors(t *testing.T) {
	home := sandbox(t)
	prefix := filepath.Join(home, ".wine")
	winePrefix(t, prefix, regKey("World of Warcraft", "World of Warcraft", `D:\Games\World of Warcraft`))
	drive := filepath.Join(t.TempDir(), "d")
	require.NoError(t, os.MkdirAll(filepath.Join(prefix, "dosdevices"), 0o755))
	require.NoError(t, os.Symlink(drive, filepath.Join(prefix, "dosdevices", "d:")))
	root := filepath.Join(drive, "Games", "World of Warcraft")
	writeFile(t, filepath.Join(root, ".build.info"), multiFlavorBuildInfo)
	for _, f := range []string{"_retail_", "_classic_", "_classic_era_"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, f), 0o755))
	}

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	assert.Empty(t, warnings)
	bySlug := map[string]domain.DetectedGame{}
	for _, g := range games {
		bySlug[g.Slug] = g
	}
	require.Len(t, bySlug, 3)
	for slug, want := range map[string]struct {
		flavor string
		types  []int
	}{
		"wow-retail":      {"_retail_", []int{517}},
		"wow-classic":     {"_classic_", []int{79434}},
		"wow-classic-era": {"_classic_era_", []int{67408}},
	} {
		g := bySlug[slug]
		assert.Equal(t, filepath.Join(root, want.flavor), g.InstallPath, slug)
		assert.Equal(t, filepath.Join(root, want.flavor, "Interface", "AddOns"), g.ModPath, slug)
		require.NotNil(t, g.CurseForge, slug)
		assert.Equal(t, want.types, g.CurseForge.GameVersionTypes, slug)
		require.NotNil(t, g.Launch, slug)
		assert.Empty(t, g.Launch.BattleNet.Proton, "a plain Wine prefix has no Proton")
	}
	assert.Equal(t, "WoW", bySlug["wow-retail"].Launch.BattleNet.Product, "retail is the one launch code lmm fills in")
	assert.Empty(t, bySlug["wow-classic"].Launch.BattleNet.Product)
}

// A row whose flavor folder is gone is a stale .build.info row: a known
// game warns, exactly as Steam's missing install dir does.
func TestDetectGames_MissingFlavorFolderWarns(t *testing.T) {
	home := sandbox(t)
	prefix := filepath.Join(home, ".wine")
	winePrefix(t, prefix)
	root := filepath.Join(prefix, "drive_c", "Program Files (x86)", "World of Warcraft")
	writeFile(t, filepath.Join(root, ".build.info"), multiFlavorBuildInfo)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_retail_"), 0o755))

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	require.Len(t, games, 1)
	assert.Equal(t, "wow-retail", games[0].Slug)
	assert.Len(t, warnings, 2)
}

// #545's deny-list: competitive titles with anti-cheat are never returned -
// not by the catalog, not under IncludeUnknown, not even when the user's
// own battlenet-games.yaml lists one - whether the product code or the
// name gives it away.
func TestDetectGames_DenyListIsNeverReturned(t *testing.T) {
	home := sandbox(t)
	prefix := filepath.Join(home, "Games", "battlenet")
	pf := filepath.Join(prefix, "drive_c", "Program Files (x86)")
	winePrefix(t, prefix, regKey("Hearthstone", "Hearthstone", `C:\Program Files (x86)\Hearthstone`))
	header := "Branch!STRING:0|Active!DEC:1|Product!STRING:0\n"
	for dir, product := range map[string]string{
		"Overwatch":           "pro",
		"Hearthstone":         "wtcg",
		"Heroes of the Storm": "hero",
		"Call of Duty":        "auks",
		"Some Shooter":        "zzcod", // an unknown code - its Uninstall-less folder name gives nothing away
	} {
		writeFile(t, filepath.Join(pf, dir, ".build.info"), header+"us|1|"+product+"\n")
	}
	// An unknown code whose display name is a denied title is denied too.
	writeFile(t, filepath.Join(pf, "Overwatch Beta", ".build.info"), header+"us|1|prob2\n")
	writeFile(t, filepath.Join(pf, "Diablo II Resurrected", ".build.info"), header+"us|1|osi\n")
	configDir := t.TempDir()
	writeFile(t, filepath.Join(configDir, "battlenet-games.yaml"), "pro:\n  slug: overwatch\n  name: Overwatch 2\n  mod_path: mods\n  sources: {nexusmods: overwatch}\n")

	games, warnings, err := DetectGames(configDir, DetectOptions{IncludeUnknown: true})
	require.NoError(t, err)
	var refs []string
	for _, g := range games {
		refs = append(refs, g.Ref())
	}
	assert.ElementsMatch(t, []string{"battlenet:zzcod", "battlenet:osi"}, refs,
		"only the two titles nothing denies are listed, and only as unknown rows")
	for _, g := range games {
		assert.False(t, g.Known)
		assert.Empty(t, g.ModPath, "detection never guesses an unknown game's mod path")
	}
	assert.NotEmpty(t, warnings, "the user's own entry for a denied title is refused out loud")

	known, _, err := DetectGames(configDir, DetectOptions{})
	require.NoError(t, err)
	assert.Empty(t, known)
}

// Prefix discovery: Lutris' ~/Games/*, both Bottles locations, Steam's
// compatdata/*/pfx, $WINEPREFIX and ~/.wine - each prefix once, however
// many names reach it (the umu prefix's pfx -> . symlink is one).
func TestFindPrefixes(t *testing.T) {
	home := sandbox(t)
	mk := func(p string) string {
		winePrefix(t, p)
		return p
	}
	lutris := filepath.Join(home, "Games", "battlenet")
	umuPrefix(t, lutris)
	mk(filepath.Join(home, "Games", "not-a-prefix", "drive_c")) // drive_c without system.reg: no
	bottles := mk(filepath.Join(home, ".local", "share", "bottles", "bottles", "Blizzard"))
	flatpak := mk(filepath.Join(home, ".var", "app", "com.usebottles.bottles", "data", "bottles", "bottles", "bnet"))
	compat := mk(filepath.Join(home, ".local", "share", "Steam", "steamapps", "compatdata", "2000000001", "pfx"))
	env := mk(filepath.Join(t.TempDir(), "custom"))
	t.Setenv("WINEPREFIX", env)
	dotWine := mk(filepath.Join(home, ".wine"))

	got := FindPrefixes()
	assert.Equal(t, []string{env, dotWine, lutris, bottles, flatpak, compat}, got)
}

// The Uninstall keys come out of system.reg with Wine's escapes undone.
func TestReadUninstallEntries(t *testing.T) {
	entries, err := readUninstallEntries(filepath.Join("testdata", "umu-prefix", "system.reg"))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, uninstallEntry{DisplayName: "Battle.net", InstallLocation: `C:\Program Files (x86)\Battle.net`}, entries[0])
	assert.Equal(t, "Wine Mono Windows Support", entries[1].DisplayName)
	assert.Empty(t, entries[1].InstallLocation)
}

// .build.info: the Product column of every active row, from the owner's
// file and the synthetic multi-flavor one.
func TestReadBuildInfoProducts(t *testing.T) {
	products, err := readBuildInfoProducts(filepath.Join("testdata", "umu-prefix", ".build.info"))
	require.NoError(t, err)
	assert.Equal(t, []string{"wowf"}, products)

	path := filepath.Join(t.TempDir(), ".build.info")
	writeFile(t, path, multiFlavorBuildInfo+"us|0||1.0|wowt\n\nus|1||11.2|wow\n")
	products, err = readBuildInfoProducts(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"wow", "wow_classic", "wow_classic_era"}, products, "inactive and repeated rows are dropped")

	writeFile(t, path, "Branch!STRING:0|Active!DEC:1\nus|1\n")
	products, err = readBuildInfoProducts(path)
	require.NoError(t, err)
	assert.Empty(t, products, "no Product column, nothing to confirm")
}

// The embedded catalog and the user's override, mirroring steam-games.yaml.
func TestLoadKnownGames(t *testing.T) {
	games, err := LoadKnownGames(t.TempDir())
	require.NoError(t, err)
	for _, code := range []string{"wow", "wow_classic", "wow_classic_era", "wowf"} {
		info, ok := games[code]
		require.True(t, ok, code)
		assert.Equal(t, "Interface/AddOns", info.ModPath, code)
		assert.Equal(t, map[string]string{"curseforge": "1"}, info.Sources, code)
		assert.NotEmpty(t, info.InstallDir, code)
	}
	for code := range games {
		assert.False(t, denied(code, ""), "the catalog never lists a denied title: %s", code)
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "battlenet-games.yaml"), "wowf:\n  slug: wow\n  name: My WoW\n  install_dir: _forever_\n  mod_path: Interface/AddOns\n  sources: {curseforge: \"1\"}\n  curseforge: {game_version_types: [67408]}\n")
	games, err = LoadKnownGames(dir)
	require.NoError(t, err)
	assert.Equal(t, "wow", games["wowf"].Slug)
	assert.Equal(t, []int{67408}, games["wowf"].CurseForgeVersionTypes)
	assert.Contains(t, games, "wow", "the embedded entries are still there")
}

// A system.reg lmm cannot read still leaves the Program Files scan - the
// only way WoW is found in the owner's prefix - and says what went wrong.
func TestDetectGames_UnreadableRegistryStillScansProgramFiles(t *testing.T) {
	home := sandbox(t)
	prefix := filepath.Join(home, "Games", "battlenet")
	umuPrefix(t, prefix)
	installProton(t, home, "GE-Proton11-11")
	writeFile(t, filepath.Join(prefix, "system.reg"), "WINE REGISTRY Version 2\n\""+strings.Repeat("x", 5*1024*1024)+"\n")

	games, warnings, err := DetectGames(t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	require.Len(t, games, 1)
	assert.Equal(t, "wowf", games[0].DetectorID)
	assert.Len(t, warnings, 1)
}

// An InstallLocation climbing out of drive_c names nothing in the prefix.
func TestUnixPath_StaysInTheDrive(t *testing.T) {
	prefix := t.TempDir()
	assert.Equal(t, filepath.Join(prefix, "drive_c", "Games", "WoW"), unixPath(prefix, `C:\Games\WoW`))
	assert.Empty(t, unixPath(prefix, `C:\..\..\etc`))
	assert.Empty(t, unixPath(prefix, `C:\Games\..\..\..\etc`))
	assert.Empty(t, unixPath(prefix, `C:\a\..\..`), "climbs to exactly the drive's parent")
	assert.Empty(t, unixPath(prefix, `C:\a\b\..\..\..`))
	assert.Equal(t, filepath.Join(prefix, "drive_c"), unixPath(prefix, `C:\a\..`), "the drive's own root is in the drive")
}

// The retail flavor's slug is wow-retail, like every other flavor's
// wow-<flavor>: "wow" is the id a user picks by hand for whichever flavor
// they play, and a catalog slug equal to it reads as that game (#545).
func TestLoadKnownGames_RetailIsWowRetail(t *testing.T) {
	games, err := LoadKnownGames(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "wow-retail", games["wow"].Slug)
	for code, info := range games {
		assert.NotEqual(t, "wow", info.Slug, code)
	}
}

// A battlenet-games.yaml lmm cannot read or parse is a warning, not a
// failed scan: detection carries on with the embedded catalog.
func TestDetectGames_BadOverrideFallsBackToTheEmbeddedCatalog(t *testing.T) {
	for name, write := range map[string]func(t *testing.T, path string){
		"unparsable": func(t *testing.T, path string) { writeFile(t, path, "wowf: [not, a, map\n") },
		"invalid entry": func(t *testing.T, path string) {
			writeFile(t, path, "wow:\n  slug: mine\n  name: Mine\n  mod_path: x\n  sources: {curseforge: \"1\"}\nwowf:\n  slug: ''\n")
		},
		"unreadable": func(t *testing.T, path string) { require.NoError(t, os.MkdirAll(path, 0o755)) },
	} {
		t.Run(name, func(t *testing.T) {
			home := sandbox(t)
			umuPrefix(t, filepath.Join(home, "Games", "battlenet"))
			installProton(t, home, "GE-Proton11-11")
			configDir := t.TempDir()
			write(t, filepath.Join(configDir, "battlenet-games.yaml"))

			games, warnings, err := DetectGames(configDir, DetectOptions{})
			require.NoError(t, err)
			require.Len(t, games, 1)
			assert.Equal(t, "wow-forever", games[0].Slug, "the embedded catalog's entry, not a half-merged override")
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0], "battlenet-games.yaml")
		})
	}
}

// Under IncludeUnknown, every product a root's .build.info names is a row,
// not just the first.
func TestDetectGames_EveryUnknownProductInARoot(t *testing.T) {
	home := sandbox(t)
	prefix := filepath.Join(home, ".wine")
	winePrefix(t, prefix)
	root := filepath.Join(prefix, "drive_c", "Program Files (x86)", "Some Game")
	writeFile(t, filepath.Join(root, ".build.info"), "Branch!STRING:0|Active!DEC:1|Product!STRING:0\nus|1|aaa\nus|1|bbb\n")

	games, _, err := DetectGames(t.TempDir(), DetectOptions{IncludeUnknown: true})
	require.NoError(t, err)
	var refs []string
	for _, g := range games {
		refs = append(refs, g.Ref())
	}
	assert.Equal(t, []string{"battlenet:aaa", "battlenet:bbb"}, refs)
}

// Package battlenet detects Battle.net games installed under Wine or
// Proton (#545), the way internal/source/steam detects Steam's: it finds
// the prefixes on this machine, the game roots inside each, and confirms
// each game by the product code in its root's .build.info, then configures
// the ones its catalog (battlenet-games.yaml) knows.
//
// It reads no launcher's private config - not Lutris' database, not
// Bottles' or Battle.net's own: a prefix's system.reg (Wine's plain-text
// registry, for the Uninstall keys) and the game's .build.info are files
// Wine and the game itself keep, whatever launcher made the prefix.
package battlenet

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
)

// DetectOptions tunes a DetectGames scan; it mirrors the half of
// steam.DetectOptions that means anything here.
type DetectOptions struct {
	// IncludeUnknown adds every other product a .build.info names as an
	// unknown candidate - its name, its install path, an EMPTY mod path
	// and no sources - except the deny-listed ones, which never appear.
	IncludeUnknown bool
	// Logger receives the scan's notices (see steam.DetectOptions.Logger).
	// Nil discards.
	Logger *slog.Logger
}

// logger is opts.Logger, or a discarding one.
func (o DetectOptions) logger() *slog.Logger {
	if o.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return o.Logger
}

// deniedProducts are the product codes of competitive titles that run
// anti-cheat: Overwatch, Hearthstone, Heroes of the Storm and Call of
// Duty. lmm must never offer to put files into one, so they are never
// returned - not under IncludeUnknown, not when a user's own
// battlenet-games.yaml lists one. The codes are each title's live, PTR and
// beta products as Battle.net names them.
var deniedProducts = map[string]bool{
	"pro": true, "prot": true, "prob": true, "prodev": true, // Overwatch 2
	"wtcg": true, "hsb": true, // Hearthstone
	"hero": true, "herot": true, "storm": true, // Heroes of the Storm
	"viper": true, "odin": true, "lazr": true, "zeus": true, "fore": true, "auks": true, // Call of Duty
}

// deniedTitles back the codes up by NAME, for a product code this list
// does not know yet - a new Call of Duty ships under a new one every year.
// A name matching one of these, anywhere in it, is denied: hiding a game
// that merely mentions one is the safe way to be wrong.
var deniedTitles = []string{"overwatch", "hearthstone", "heroes of the storm", "call of duty"}

// denied reports whether a product - by code, or by the display name or
// folder name it was found under - is on the deny-list.
func denied(code, name string) bool {
	if deniedProducts[strings.ToLower(code)] {
		return true
	}
	name = strings.ToLower(name)
	for _, title := range deniedTitles {
		if strings.Contains(name, title) {
			return true
		}
	}
	return false
}

// FindPrefixes returns the Wine prefixes on this machine, in search order,
// each once however many paths reach it: $WINEPREFIX, ~/.wine, Lutris'
// default ~/Games/*, Bottles' bottles (native and Flatpak), and every
// Steam library's compatdata/*/pfx - where a non-Steam game added to Steam
// keeps its prefix. A directory is a prefix when it holds both system.reg
// and drive_c.
func FindPrefixes() []string {
	home, _ := os.UserHomeDir()
	var candidates []string
	if p := os.Getenv("WINEPREFIX"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, filepath.Join(home, ".wine"))
	for _, parent := range []string{
		filepath.Join(home, "Games"),
		filepath.Join(home, ".local", "share", "bottles", "bottles"),
		filepath.Join(home, ".var", "app", "com.usebottles.bottles", "data", "bottles", "bottles"),
	} {
		candidates = append(candidates, children(parent)...)
	}
	for _, root := range steam.FindSteamRoots() {
		libraries, err := steam.GetLibraryPaths(root)
		if err != nil {
			continue
		}
		for _, lib := range libraries {
			for _, dir := range children(filepath.Join(lib, "steamapps", "compatdata")) {
				candidates = append(candidates, filepath.Join(dir, "pfx"))
			}
		}
	}

	var out []string
	seen := make(map[string]bool)
	for _, p := range candidates {
		if !isPrefix(p) {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, p)
	}
	return out
}

// children lists dir's subdirectories (symlinks to directories included),
// sorted; none when dir cannot be read.
func children(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// isPrefix reports whether dir is a Wine prefix: system.reg and drive_c.
func isPrefix(dir string) bool {
	reg, err := os.Stat(filepath.Join(dir, "system.reg"))
	if err != nil || !reg.Mode().IsRegular() {
		return false
	}
	drive, err := os.Stat(filepath.Join(dir, "drive_c"))
	return err == nil && drive.IsDir()
}

// gameRoot is a directory in a prefix that may hold a .build.info, and the
// name an Uninstall key gives it, when one does.
type gameRoot struct {
	path string
	name string
}

// gameRoots lists the directories in prefix that may be a game's install
// root: every Uninstall key's InstallLocation, then every folder directly
// under drive_c's two Program Files - Battle.net's default install
// location, and the only place to look for a game Battle.net installed
// without writing an Uninstall key (World of Warcraft, in the prefix #545
// was built against). The .build.info is what confirms a root; this only
// proposes them. Each directory comes once.
//
// A system.reg it cannot read is returned as the error, beside the roots
// the Program Files scan still found: that scan needs no registry, and is
// the one that finds a game with no Uninstall key.
func gameRoots(prefix string) ([]gameRoot, error) {
	entries, regErr := readUninstallEntries(filepath.Join(prefix, "system.reg"))
	var out []gameRoot
	seen := make(map[string]bool)
	add := func(path, name string) {
		real, err := filepath.EvalSymlinks(path)
		if err != nil || seen[real] {
			return
		}
		seen[real] = true
		out = append(out, gameRoot{path: path, name: name})
	}
	for _, e := range entries {
		if path := unixPath(prefix, e.InstallLocation); path != "" {
			add(path, e.DisplayName)
		}
	}
	for _, pf := range []string{"Program Files (x86)", "Program Files"} {
		for _, dir := range children(filepath.Join(prefix, "drive_c", pf)) {
			add(dir, "")
		}
	}
	return out, regErr
}

// unixPath maps a Windows path in prefix ("C:\Program Files (x86)\Game")
// to the directory it names: C: is the prefix's drive_c, and any other
// drive is where its dosdevices/<letter>: link points. "" when the path
// names no drive the prefix has.
func unixPath(prefix, winPath string) string {
	winPath = strings.TrimSpace(winPath)
	if len(winPath) < 2 || winPath[1] != ':' {
		return ""
	}
	letter := strings.ToLower(winPath[:1])
	rest := filepath.FromSlash(strings.Trim(strings.ReplaceAll(winPath[2:], `\`, "/"), "/"))
	// A path that climbs out of its drive names nothing in the prefix.
	if clean := filepath.Clean(rest); clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return ""
	}
	if letter == "c" {
		return filepath.Join(prefix, "drive_c", rest)
	}
	devices := filepath.Join(prefix, "dosdevices")
	target, err := os.Readlink(filepath.Join(devices, letter+":"))
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(devices, target)
	}
	return filepath.Join(target, rest)
}

// DetectGames scans every Wine prefix for Battle.net games and returns the
// ones the catalog knows - every installed one under opts.IncludeUnknown -
// except the deny-listed titles, which it never returns. configDir holds
// the user's battlenet-games.yaml override. Warnings are non-fatal: a
// prefix that cannot be read, a known game whose folder is gone, a user
// catalog entry for a denied title.
//
// One root can hold several games: World of Warcraft installs every flavor
// under one root, and its .build.info has a row per flavor. Each becomes
// its own game at its own flavor folder (GameInfo.InstallDir), so no two
// share an install path.
func DetectGames(configDir string, opts DetectOptions) (games []domain.DetectedGame, warnings []string, err error) {
	catalog, warning, err := loadCatalog(configDir)
	if err != nil {
		return nil, nil, err
	}
	if warning != "" {
		warnings = append(warnings, warning)
	}
	takenSlugs := make(map[string]bool, len(catalog))
	for code, info := range catalog {
		if denied(code, info.Name) {
			warnings = append(warnings, fmt.Sprintf("%s: %s (%s) is a competitive title with anti-cheat; lmm never configures it", overrideFile, code, info.Name))
			delete(catalog, code)
			continue
		}
		takenSlugs[info.Slug] = true
	}

	home, _ := os.UserHomeDir()
	seenSlug := make(map[string]bool)
	seenInstall := make(map[string]bool)
	protonWarned := make(map[string]bool)
	for _, prefix := range FindPrefixes() {
		roots, err := gameRoots(prefix)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: reading system.reg: %v", prefix, err))
		}
		for _, root := range roots {
			products, err := readBuildInfoProducts(filepath.Join(root.path, ".build.info"))
			if err != nil {
				if !os.IsNotExist(err) {
					warnings = append(warnings, fmt.Sprintf("%s: %v", root.path, err))
				}
				continue
			}
			name := root.name
			if name == "" {
				name = filepath.Base(root.path)
			}
			for _, code := range products {
				info, known := catalog[code]
				if denied(code, name) || denied(code, info.Name) {
					continue
				}
				if !known && !opts.IncludeUnknown {
					continue
				}
				g, ok, warning := detected(prefix, root.path, code, name, info, known)
				if warning != "" {
					warnings = append(warnings, warning)
				}
				if !ok {
					continue
				}
				real, err := filepath.EvalSymlinks(g.InstallPath)
				if err != nil {
					real = g.InstallPath
				}
				if !known {
					// An unknown product has no folder of its own to tell it
					// from another in the same root.
					real += "\x00" + code
				}
				if seenInstall[real] || (known && seenSlug[g.Slug]) {
					opts.logger().Info("skipping a Battle.net game already found in another prefix",
						"product", code, "install", g.InstallPath)
					continue
				}
				seenInstall[real] = true
				if known {
					seenSlug[g.Slug] = true
				} else {
					g.Slug = uniqueSlug(name, code, takenSlugs)
					takenSlugs[g.Slug] = true
				}
				var protonWarning string
				g.Launch, protonWarning = launchFor(prefix, home, info.LaunchCode)
				if protonWarning != "" && !protonWarned[prefix] {
					protonWarned[prefix] = true
					warnings = append(warnings, protonWarning)
				}
				games = append(games, g)
			}
		}
	}
	return games, warnings, nil
}

// detected builds one candidate for product code in root. ok is false when
// its install folder is gone, or its Documents-relative mod path names no
// one user's Documents (#546) - which warns for a known game, the one the
// user means to configure, and is skipped silently otherwise.
func detected(prefix, root, code, name string, info GameInfo, known bool) (domain.DetectedGame, bool, string) {
	g := domain.DetectedGame{
		Detector: domain.DetectorBattleNet, DetectorID: code,
		Name: name, InstallPath: root, WinePrefix: prefix,
	}
	if !known {
		return g, true, ""
	}
	g.InstallPath = filepath.Join(root, filepath.FromSlash(info.InstallDir))
	if st, err := os.Stat(g.InstallPath); err != nil || !st.IsDir() {
		return g, false, fmt.Sprintf("%s: %s is listed in .build.info but its folder %s is missing", root, code, g.InstallPath)
	}
	base := g.InstallPath
	if info.ModPathBase == ModPathBaseDocuments {
		docs, err := documentsDir(prefix, documentsGameFolder(info.ModPath))
		if err != nil {
			return g, false, fmt.Sprintf("%s: %s keeps its mods in a prefix user's Documents, but %v", root, info.Name, err)
		}
		base = docs
	}
	g.Slug, g.Name, g.Known = info.Slug, info.Name, true
	g.ModPath = filepath.Join(base, filepath.FromSlash(info.ModPath))
	g.Adapter = info.Adapter
	g.Sources = make(map[string]string, len(info.Sources))
	for k, v := range info.Sources {
		g.Sources[k] = v
	}
	if cf, err := domain.NewGameCurseForge(info.CurseForgeVersionTypes); err == nil {
		g.CurseForge = cf
	}
	return g, true, ""
}

// launchFor is the launch a detected game is prefilled with: through the
// Battle.net client in its prefix (domain.LaunchViaBattleNet), with the
// launch code the catalog knows. A prefix under the home directory is
// written with ~/, as a user would. A Proton prefix last run by GE-Proton
// keeps the build that made it (protonFor); warning is non-empty when it
// cannot.
func launchFor(prefix, home, launchCode string) (launch *domain.GameLaunch, warning string) {
	path := prefix
	if home != "" {
		if rel, err := filepath.Rel(home, prefix); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			path = "~/" + filepath.ToSlash(rel)
		}
	}
	bn := &domain.BattleNetLaunch{Prefix: path, Product: launchCode}
	bn.Proton, warning = protonFor(prefix, home)
	return &domain.GameLaunch{Via: domain.LaunchViaBattleNet, BattleNet: bn}, warning
}

// protonFor is the PROTONPATH a GE-Proton prefix is prefilled with: the
// exact build named in its version file ("GE-Proton11-11") when that build
// is installed where umu-run looks for one by name, since running a prefix
// with a different Proton than made it upgrades or downgrades it. A build
// umu-run cannot find falls back to "GE-Proton" - umu's name for the
// NEWEST GE-Proton, which it downloads - and warning says so. "" for a
// plain Wine prefix or one another Proton made: umu-run's own default.
func protonFor(prefix, home string) (proton, warning string) {
	version, ok := protonVersion(prefix)
	if !ok || !strings.HasPrefix(version, "GE-Proton") {
		return "", ""
	}
	if version == "GE-Proton" {
		return version, ""
	}
	// A build name is one path element; anything else names no build.
	if filepath.Base(version) == version && version != ".." {
		for _, dir := range protonToolDirs(home) {
			if st, err := os.Stat(filepath.Join(dir, version)); err == nil && st.IsDir() {
				return version, ""
			}
		}
	}
	return "GE-Proton", fmt.Sprintf("%s: made by %s, which is not installed where umu-run looks for it (%s); launch.battlenet.proton is prefilled as GE-Proton, the newest GE-Proton, which may upgrade the prefix - install %s there or set launch.battlenet.proton yourself",
		prefix, version, strings.Join(protonToolDirs(home), ", "), version)
}

// protonToolDirs are the folders umu-run finds a Proton build named in
// PROTONPATH in: umu's own compatibilitytools and Steam's
// compatibilitytools.d, under the XDG data home and under ~/.local/share.
func protonToolDirs(home string) []string {
	var dataHomes []string
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		dataHomes = append(dataHomes, d)
	}
	if home != "" {
		if d := filepath.Join(home, ".local", "share"); !slices.Contains(dataHomes, d) {
			dataHomes = append(dataHomes, d)
		}
	}
	var out []string
	for _, d := range dataHomes {
		out = append(out, filepath.Join(d, "umu", "compatibilitytools"), filepath.Join(d, "Steam", "compatibilitytools.d"))
	}
	return out
}

// protonVersion is the Proton build that last ran prefix, from the version
// file Proton keeps beside its pfx folder - in the prefix itself for one
// umu made (pfx -> .), in its parent for Steam's compatdata/<id>/pfx. ok is
// false for a plain Wine prefix.
func protonVersion(prefix string) (string, bool) {
	for _, dir := range []string{prefix, filepath.Dir(prefix)} {
		if _, err := os.Stat(filepath.Join(dir, "pfx")); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "version"))
		if err != nil {
			continue
		}
		first, _, _ := strings.Cut(string(data), "\n")
		return strings.TrimSpace(first), true
	}
	return "", false
}

// uniqueSlug derives an unknown game's slug from its name - lower-case
// ASCII letters and digits, every other run a single dash - falling back
// to "battlenet-<code>" when that is empty or already spoken for.
func uniqueSlug(name, code string, taken map[string]bool) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	slug := b.String()
	if slug == "" || taken[slug] {
		slug = "battlenet-" + strings.ReplaceAll(code, "_", "-")
	}
	base := slug
	for n := 2; taken[slug]; n++ {
		slug = fmt.Sprintf("%s-%d", base, n)
	}
	return slug
}

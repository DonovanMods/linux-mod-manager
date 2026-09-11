// Package core: this file holds the BepInEx archive-root NORMALISER (#358) -
// the rules that turn a BepInEx plugin archive's member list into the
// game-directory-relative paths it should deploy to.
//
// Almost all of them are pure rules over that member list. The one
// exception is bepinexGameOwnedRoot, which reads the game's own install
// directory, because one question genuinely cannot be answered from the
// archive alone: for a BepInEx game mod_path IS the game root, so an
// archive root directory and a directory the GAME owns are the same kind of
// name, and only the game directory itself can say which this is (#424).
//
// It sits beside archive_listing.go's member normalisation on purpose, and
// for the same reason that file exists: the plan (PlanImportArchive) and
// every ingest (importWithIdentity, extractIntoStaging) must apply ONE set
// of rules, or a preview promises a layout the deploy does not produce.
// It is its own file rather than more of archive_listing.go because the
// rules are a closed, self-contained table with no dependency on the
// listing machinery - the same separation internal/source/icarus/exmodz.go
// gives #237's `.EXMODZ` wrapper strip, which is this normaliser's direct
// precedent (an archive whose payload sits one directory too deep).
//
// WHY it is needed at all: BepInEx installs into the GAME ROOT, which lmm
// already expresses with a game whose mod_path IS its install path (the
// same absolute path twice - never mod_path: "", which is joined verbatim
// and deploys relative to the working directory). With that, the common
// archive shape - BepInEx/plugins/Foo.dll - deploys correctly through the
// existing linker with no new deploy-rule type. What does NOT work
// unassisted is the other real shapes - a BepInEx-relative root, a loose
// assembly, and the NexusMods plugin FOLDER meant to be dropped into
// BepInEx/plugins/ whole (#424) - and the metadata every Thunderstore
// package carries at its root.
//
// See docs/plans/2026-09-09-bepinex-spike.md §1.3 (the observed shapes) and
// §3 (the deploy mapping) for the evidence behind each rule.
package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
)

// ErrBepInExFrameworkPack is the refusal for an archive that IS BepInEx
// rather than a mod for it: a payload under BepInEx/core/ is the loader,
// which is a per-game prerequisite living in the game root and surviving a
// profile switch - not a profile member (spike §2). Installing it as a mod
// would put the preloader under lmm's deployed-files bookkeeping, where the
// next uninstall or profile switch tears the loader out from under every
// plugin.
var ErrBepInExFrameworkPack = errors.New("this archive is the BepInEx loader itself, not a mod")

// bepinexShape is which of the observed archive layouts an archive has.
type bepinexShape int

// The bepinexShape values. bepinexShapeNone means "not a layout this
// normaliser recognises" - the archive is left exactly as it arrived.
const (
	// bepinexShapeNone is an archive the rules do not recognise. Nothing is
	// rewritten and nothing is dropped: "warns, never guesses".
	bepinexShapeNone bepinexShape = iota
	// bepinexShapeRooted is shape A: BepInEx/ at the archive root, which
	// deploys as-is (only the root metadata is dropped).
	bepinexShapeRooted
	// bepinexShapeWrapped is shape C: one wrapper directory containing
	// BepInEx/, whose single leading segment is stripped.
	bepinexShapeWrapped
	// bepinexShapeRelative is shape B: a root of bare plugins/ patchers/
	// monomod/ config/ directories, prefixed with BepInEx/.
	bepinexShapeRelative
	// bepinexShapePlugin is a loose root .dll (with no directory at all),
	// which becomes BepInEx/plugins/<ModName>/<file>.
	bepinexShapePlugin
	// bepinexShapePluginFolder is shape F (#424): a root of one or more
	// DIRECTORIES that each hold an assembly somewhere inside, which is
	// what a NexusMods Unity mod page ships - a folder meant to be dropped
	// into BepInEx/plugins/ whole. Each root directory is prefixed with
	// BepInEx/plugins/ and otherwise kept verbatim.
	bepinexShapePluginFolder
)

// String returns the shape's diagnostic name. Not a wire value: no document
// carries a shape, and the warnings quote the archive's own paths instead.
func (s bepinexShape) String() string {
	switch s {
	case bepinexShapeRooted:
		return "game-root-relative"
	case bepinexShapeWrapped:
		return "wrapped in a single directory"
	case bepinexShapeRelative:
		return "BepInEx-relative"
	case bepinexShapePlugin:
		return "a loose plugin assembly"
	case bepinexShapePluginFolder:
		return "a plugin folder"
	default:
		return "unrecognised"
	}
}

// bepinexDirName is BepInEx's own spelling of its game-root directory, and
// the CANONICAL one every rewrite produces. An archive may spell it any way
// its author's filesystem allowed (`bepinex/`, `BEPINEX/`); lmm folds case
// when it recognises the directory and writes this spelling when it deploys,
// because the loader reads a fixed path and a safety rule that turns on one
// character is not a safety rule.
const bepinexDirName = "BepInEx"

// bepinexRootDirs are the directories BepInEx itself owns under BepInEx/,
// and therefore the root names that identify shape B. patchers and monomod
// are preload-time; plugins is the ordinary case; config is seeded
// configuration (see isBepInExConfigMember).
var bepinexRootDirs = []string{"plugins", "patchers", "monomod", "config"}

// bepinexOwnedDirs are every directory BepInEx itself owns under BepInEx/:
// the shape-B roots above, plus the two the loader keeps for itself - core
// (the framework's own assemblies, which the framework refusal turns on) and
// cache. bepinexCanonicalRoot folds these as well as the BepInEx/ directory
// above them, because the rules below it compare a TWO-segment prefix and
// the loader reads all of them at a fixed spelling.
var bepinexOwnedDirs = append([]string{"core", "cache"}, bepinexRootDirs...)

// bepinexMetadataStems are the root FILES a Thunderstore package carries and
// lmm must never deploy: they describe the package to the website, and
// deploying them scatters a manifest.json and an icon.png into the game root
// of every game a plugin is installed into (spike §1.3).
//
// Matched on the STEM, case-insensitively, rather than on a fixed set of
// full names: README.txt, LICENSE and CHANGELOG.txt are as common on
// Thunderstore as their .md spellings, and an extension-exact list quietly
// deployed those into the Steam install directory. The shapes were also
// observed from Windows-authored archives, where README.md and Readme.md
// are the same file to their authors.
var bepinexMetadataStems = []string{"manifest", "icon", "readme", "changelog", "license"}

// bepinexLayout is the normaliser's answer about one archive: the shape it
// recognised, the rewrite each member takes, and any warning worth showing
// the user. A layout that does not apply (Applies false) rewrites nothing -
// callers use the archive's own paths unchanged.
type bepinexLayout struct {
	// Shape is the recognised layout, or bepinexShapeNone.
	Shape bepinexShape
	// Warnings are user-facing diagnostics: today, exactly one, for an
	// archive a BepInEx game gave the normaliser that it could not place.
	Warnings []string

	// rewrites maps an archive member (slash-separated, as listed) to the
	// game-dir-relative path it deploys to. A member absent from the map
	// with applies set is DROPPED (the metadata above).
	rewrites map[string]string
	applies  bool
}

// Applies reports whether this layout rewrites anything. False means the
// archive's members deploy exactly as they are listed - either because no
// rule matched, or because the shape that matched needs a loader
// declaration the game does not carry.
func (l *bepinexLayout) Applies() bool { return l != nil && l.applies }

// Rewrite maps one archive member to its deploy-relative path. kept is
// false for a member this layout drops (a Thunderstore metadata file).
//
// A nil or non-applying layout is the identity, so a caller can hold one
// unconditionally instead of branching at every member.
func (l *bepinexLayout) Rewrite(member string) (dest string, kept bool) {
	if !l.Applies() {
		return member, true
	}
	dest, kept = l.rewrites[member]
	return dest, kept
}

// bepinexNormalise applies the archive-root rules to members (the archive's
// file listing, slash-separated and archive-relative), naming a loose
// plugin's directory after modName.
//
// loaderDeclared is the game's `loader: kind: bepinex` block (#359). It is
// the gate on the two AMBIGUOUS shapes, and the gating rule is the one
// decision the spike left to the implementation:
//
//	shape A (BepInEx/ at the root), shape C (one wrapper dir containing
//	BepInEx/) and a framework pack are recognised for EVERY game, because
//	an archive that names the directory `BepInEx` is not plausibly anything
//	else;
//
//	shape B (a bare plugins/ patchers/ monomod/ config/ root), shape F (a
//	root of plugin FOLDERS, #424) and a loose root .dll are recognised
//	ONLY for a game that declares the loader, because `plugins/`, `*.dll`
//	and "a directory with an assembly in it" are ordinary shapes that
//	other games' mods use - a 7 Days to Die archive rooted at `plugins/`
//	or at `Mods/<Mod>/<Mod>.dll` must keep deploying to <mod_path>, and
//	silently moving it under a BepInEx/ directory the game has never heard
//	of would break a working install with no error to read.
//
// That is the "OR" form of the requirement, split per shape rather than
// applied wholesale: "unmistakably BepInEx-shaped" is a property of the
// individual shape, not of the archive as a category.
//
// gameRoot is the game's install directory, which for a BepInEx game is
// also where these paths deploy. It is consulted by exactly one rule -
// shape F's game-owned refusal (bepinexGameOwnedRoot) - and "" means "no
// game directory to consult", which every pure-rule unit test passes and
// which leaves the member list as the only evidence.
//
// The error is ErrBepInExFrameworkPack, and only that: an unrecognised
// archive is a warning on the returned layout, never a failure. A layout is
// always returned alongside a nil error, so a caller may hold it
// unconditionally.
func bepinexNormalise(members []string, modName string, loaderDeclared bool, gameRoot string) (*bepinexLayout, error) {
	// Every rule below is asked of the CLEANED path - forward slashes, no
	// "./" noise - while origins[i] keeps the archive's own spelling,
	// because that is the key Rewrite(member) is called with. Judging the
	// raw member instead read a Windows-authored `BepInEx\plugins\Foo.dll`
	// as one path segment (hence "a loose assembly", wrapped in a plugin
	// directory under its raw name) and read a "./"-prefixed listing as a
	// wrapper directory named "." whose metadata was never at the root.
	origins := make([]string, 0, len(members))
	payload := make([]string, 0, len(members))
	for _, m := range members {
		c := strings.TrimPrefix(path.Clean(strings.ReplaceAll(m, `\`, "/")), "/")
		if c == "." || c == "" {
			continue
		}
		origins = append(origins, m)
		payload = append(payload, c)
	}

	layout := &bepinexLayout{rewrites: make(map[string]string, len(payload))}

	// Step 1: drop the package metadata every Thunderstore archive carries
	// at its root, and take stock of what is left.
	keptOrigins, keptPayload := origins[:0], payload[:0]
	for i, m := range payload {
		if isBepInExMetadata(m) {
			continue
		}
		keptOrigins = append(keptOrigins, origins[i])
		keptPayload = append(keptPayload, m)
	}
	origins, payload = keptOrigins, keptPayload

	// Step 2: a single root directory that itself contains BepInEx/ is
	// shape C's wrapper, and its one leading segment comes off. Done before
	// every other test, so the stripped tree is judged as shape A.
	strip, wrapped := bepinexWrapperDir(payload)
	shape := bepinexShapeRooted
	if wrapped {
		shape = bepinexShapeWrapped
	}

	stripped := make([]string, len(payload))
	for i, m := range payload {
		stripped[i] = bepinexCanonicalRoot(strings.TrimPrefix(m, strip))
	}

	// Step 2b: drop the metadata AGAIN, now against the stripped paths.
	// Step 1's pass is still necessary - the wrapper probe needs a single
	// root entry, which root metadata would defeat - but it is not
	// sufficient: a real Thunderstore wrapped package carries manifest.json,
	// icon.png, README.md and CHANGELOG.md INSIDE the wrapper (that is how
	// the site builds one), so they are not root metadata until the strip
	// has happened. Dropping only before it promoted all four to the root of
	// the deploy paths, and for a BepInEx game mod_path IS the game root -
	// so they landed in the Steam install directory of every game a wrapped
	// plugin was installed into.
	if wrapped {
		keptOrigins, keptPayload := origins[:0], payload[:0]
		keptStripped := stripped[:0]
		for i, s := range stripped {
			if isBepInExMetadata(s) {
				continue
			}
			keptOrigins = append(keptOrigins, origins[i])
			keptPayload = append(keptPayload, payload[i])
			keptStripped = append(keptStripped, s)
		}
		origins, payload, stripped = keptOrigins, keptPayload, keptStripped
	}

	// Step 3: classify what the (possibly stripped, always canonically
	// spelled) root now holds.
	prefix := ""
	mixedLoaderRoot := false
	switch {
	case bepinexRootHas(stripped, bepinexDirName):
		// shape A, or shape C after the strip: deploys as-is - unless the
		// root carries something BESIDE BepInEx/, which is the same
		// half-recognised archive the three sibling refusals below already
		// report rather than guess at (#424 review, finding 2). Noted
		// rather than returned, so step 4's framework refusal still runs:
		// a safety check that can be walked past by adding one file to the
		// archive is not a safety check.
		mixedLoaderRoot = bepinexHasNonLoaderRoot(stripped)
	case bepinexRelativeRoot(stripped):
		shape, prefix = bepinexShapeRelative, "BepInEx/"
	case bepinexLoosePluginRoot(stripped):
		shape, prefix = bepinexShapePlugin, "BepInEx/plugins/"+modName+"/"
	case bepinexPluginFolderRoot(stripped, gameRoot):
		shape, prefix = bepinexShapePluginFolder, "BepInEx/plugins/"
	default:
		layout.Shape = bepinexShapeNone
		if loaderDeclared && len(payload) > 0 {
			layout.Warnings = append(layout.Warnings, bepinexUnrecognisedWarning(modName, payload))
		}
		return layout, nil
	}

	// Step 4: the framework refusal, judged on the FINAL paths so a wrapped
	// pack (BepInExPack/BepInEx/core/...) is caught alongside a bare one.
	// Before the gate, deliberately: a framework pack is unmistakable, and
	// silently installing one into a game with no loader declared is the
	// worst outcome available - the loader lands under lmm's deployed-files
	// bookkeeping and the next profile switch removes it.
	for _, m := range stripped {
		if strings.HasPrefix(bepinexCanonicalRoot(prefix+m), bepinexDirName+"/core/") {
			return nil, fmt.Errorf("%w: it installs BepInEx/core/, which lmm configures per game as a loader rather than tracking as a profile member - install BepInEx into the game directory yourself and declare it with `lmm game edit <game> --loader bepinex` (`lmm game show <game>` then prints the launch option to paste)", ErrBepInExFrameworkPack)
		}
	}

	// Step 4b: the mixed loader root, refused after the framework check and
	// before the gate - `BepInEx` is a name no other game's mod plausibly
	// uses, so this refusal is not one the declaration could make safe.
	if mixedLoaderRoot {
		layout.Shape = bepinexShapeNone
		layout.Warnings = append(layout.Warnings, bepinexUnrecognisedWarning(modName, payload))
		return layout, nil
	}

	// Step 5: the gate. An ambiguous shape needs the game's declaration.
	if !loaderDeclared && shape != bepinexShapeRooted && shape != bepinexShapeWrapped {
		return &bepinexLayout{Shape: bepinexShapeNone}, nil
	}

	// Step 6: build the rewrite map, refusing a collision rather than
	// letting one member silently overwrite another at deploy time.
	taken := make(map[string]string, len(stripped))
	for i, m := range origins {
		// Canonicalised once more with the prefix ON: a shape-B root
		// spelled `Config/` only becomes a BepInEx-owned path here, and it
		// is this path isBepInExConfigMember and the deploy both read.
		dest := bepinexCanonicalRoot(prefix + stripped[i])
		if prev, dup := taken[dest]; dup {
			return nil, fmt.Errorf("normalising the BepInEx layout of %s: members %q and %q both deploy to %q", modName, prev, m, dest)
		}
		taken[dest] = m
		layout.rewrites[m] = dest
	}
	layout.Shape, layout.applies = shape, true
	return layout, nil
}

// isBepInExMetadata reports whether member is one of the package-metadata
// files at the archive ROOT. Only the root: a plugin that legitimately
// ships its own README under BepInEx/plugins/Foo/README.md keeps it.
func isBepInExMetadata(member string) bool {
	if strings.ContainsAny(member, `/\`) {
		return false
	}
	stem := strings.ToLower(strings.TrimSuffix(member, path.Ext(member)))
	for _, f := range bepinexMetadataStems {
		if stem == f {
			return true
		}
	}
	return false
}

// bepinexCanonicalRoot rewrites a member's leading BepInEx directory - AND
// the directory BepInEx owns immediately below it - to the project's own
// spelling, so a case-variant archive deploys where the loader actually
// looks and every rule below it (the shape-A test, the framework refusal,
// isBepInExConfigMember) can go on comparing exactly.
//
// Both segments, not just the first: the framework refusal and
// isBepInExConfigMember each test a TWO-segment prefix, so folding only the
// first left `BepInEx/Core/` classified as an ordinary plugin archive and
// installed as a mod, and left a `BepInEx/Config/` file deployed as a
// symlink into the cache (re-review R1/R2). A safety refusal reached by a
// one-character difference is not a safety refusal.
//
// No deeper than that: what a plugin names its own subdirectories under
// BepInEx/plugins/Foo/ is that plugin's business, and so is a directory
// under BepInEx/ that BepInEx does not own.
func bepinexCanonicalRoot(member string) string {
	name, rest, nested := strings.Cut(member, "/")
	if !nested || !strings.EqualFold(name, bepinexDirName) {
		return member
	}
	sub, tail, deeper := strings.Cut(rest, "/")
	for _, dir := range bepinexOwnedDirs {
		if strings.EqualFold(sub, dir) {
			if deeper {
				return bepinexDirName + "/" + dir + "/" + tail
			}
			return bepinexDirName + "/" + dir
		}
	}
	return bepinexDirName + "/" + rest
}

// bepinexWrapperDir reports shape C: exactly one root entry, that entry a
// directory, and that directory containing BepInEx/ (any spelling of it -
// bepinexCanonicalRoot fixes the spelling once the strip has happened).
// strip is the prefix to remove ("Wrapper/"), empty when there is no wrapper.
func bepinexWrapperDir(members []string) (strip string, wrapped bool) {
	roots := bepinexRootNames(members)
	if len(roots) != 1 {
		return "", false
	}
	strip = roots[0] + "/"
	for _, m := range members {
		inner, _, nested := strings.Cut(strings.TrimPrefix(m, strip), "/")
		if nested && strings.EqualFold(inner, bepinexDirName) {
			return strip, true
		}
	}
	return "", false
}

// bepinexRootNames lists the distinct first path segments of members,
// sorted, so a diagnostic naming an archive's root is stable.
func bepinexRootNames(members []string) []string {
	seen := map[string]bool{}
	for _, m := range members {
		name, _, _ := strings.Cut(m, "/")
		if name != "" {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// bepinexRootHas reports whether members place anything under the root
// DIRECTORY named dir. Compared exactly: callers pass paths that
// bepinexCanonicalRoot has already put into BepInEx's own spelling.
func bepinexRootHas(members []string, dir string) bool {
	for _, m := range members {
		if strings.HasPrefix(m, dir+"/") {
			return true
		}
	}
	return false
}

// bepinexRelativeRoot reports shape B: every root entry is a DIRECTORY
// named in bepinexRootDirs. "Every", not "any": one recognised directory
// beside an unrecognised one is an archive whose author meant something
// this normaliser cannot see, and prefixing half of it would be a guess.
func bepinexRelativeRoot(members []string) bool {
	if len(members) == 0 {
		return false
	}
	for _, m := range members {
		name, rest, nested := strings.Cut(m, "/")
		if !nested || rest == "" {
			return false // a root FILE: not shape B
		}
		found := false
		for _, dir := range bepinexRootDirs {
			if strings.EqualFold(name, dir) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// bepinexHasNonLoaderRoot reports whether the root carries an entry other
// than `BepInEx` itself - the test behind step 3's mixed-loader-root
// refusal (#424 review, finding 2).
//
// `BepInEx` is the one name BepInEx owns that the shape refusals never
// reached, because bepinexRootHas short-circuits the classification switch
// before any of them is asked. A root of `BepInEx/patchers/Pre.dll` beside
// `Jotunn/Jotunn.dll` therefore read as shape A, and the plugin folder
// deployed verbatim into the game root - #424's own bug, on a game that
// DOES declare the loader, and with no warning on the plan.
//
// Every sibling, not only the ambiguous ones: a `plugins/` root beside
// `BepInEx/` deploys to the game root just as wrongly as a plugin folder
// does, and a loose file beside `BepInEx/` is the author saying something
// about that file this normaliser cannot read. Package metadata has
// already been dropped by the time this is asked, so a Thunderstore
// package's manifest and icon are not siblings.
func bepinexHasNonLoaderRoot(members []string) bool {
	for _, m := range members {
		name, _, _ := strings.Cut(m, "/")
		if !strings.EqualFold(name, bepinexDirName) {
			return true
		}
	}
	return false
}

// bepinexUnrecognisedWarning is the one sentence every refusal to guess
// shares: what lmm saw, what it did instead, and what to change. Named once
// because the two refusal sites must say the same thing.
func bepinexUnrecognisedWarning(modName string, payload []string) string {
	return fmt.Sprintf(
		"lmm did not recognise %s as a BepInEx layout (its root holds %s), so its files deploy exactly as the archive lists them; move them under BepInEx/plugins/ inside the archive if that is wrong",
		modName, strings.Join(bepinexRootNames(payload), ", "))
}

// bepinexLoosePluginRoot reports the loose-plugin shape: every remaining
// member is a root FILE, and at least one of them is an assembly. The
// non-.dll files come along (a plugin shipping a .dll beside its own
// .cfg or asset bundle), because splitting them would deploy half a mod.
func bepinexLoosePluginRoot(members []string) bool {
	if len(members) == 0 {
		return false
	}
	dll := false
	for _, m := range members {
		if strings.Contains(m, "/") {
			return false
		}
		if strings.EqualFold(path.Ext(m), ".dll") {
			dll = true
		}
	}
	return dll
}

// bepinexPluginFolderRoot reports shape F (#424): every root entry is a
// DIRECTORY that holds at least one assembly somewhere inside it, none of
// them is a name BepInEx owns, and none of them is a directory the GAME
// owns.
//
// This is what a NexusMods Unity mod page ships and what its install
// instructions describe - "drop the folder into BepInEx/plugins/" - so the
// whole directory moves under that prefix and keeps its own name, which is
// also how the .pdb, .xml and README beside the assembly stay beside it.
//
// Three conditions, and each of them is a refusal to guess:
//
//	EVERY root entry is a directory. A root that also carries loose files
//	is an author saying something about those files that this normaliser
//	cannot read, and moving the directories while leaving the files where
//	they are would deploy half a mod to each of two places.
//
//	EVERY root directory contains an assembly. One that does not is not a
//	plugin folder - it is an asset directory, a patcher payload, or
//	something else entirely - and BepInEx/plugins/ is not where it goes.
//
//	NO root directory is a name BepInEx owns. bepinexRelativeRoot (shape
//	B) has first refusal on those, and it only answers when they are the
//	WHOLE root; a root mixing `plugins/` with `Jotunn/` is the same
//	half-recognised archive shape B already refuses, so it is reported
//	rather than prefixed.
//
//	NO root directory is one the GAME owns. That is the one condition the
//	member list cannot answer, so bepinexGameOwnedRoot asks the game
//	directory - see its own doc comment for why a name list will not do.
func bepinexPluginFolderRoot(members []string, gameRoot string) bool {
	if len(members) == 0 {
		return false
	}
	hasDLL := map[string]bool{}
	order := make([]string, 0, len(members))
	for _, m := range members {
		name, rest, nested := strings.Cut(m, "/")
		if !nested || rest == "" {
			return false // a root FILE: not a plugin folder
		}
		for _, dir := range append([]string{bepinexDirName}, bepinexOwnedDirs...) {
			if strings.EqualFold(name, dir) {
				return false
			}
		}
		if _, seen := hasDLL[name]; !seen {
			order = append(order, name)
		}
		if !hasDLL[name] {
			hasDLL[name] = strings.EqualFold(path.Ext(m), ".dll")
		}
	}
	for _, name := range order {
		if !hasDLL[name] {
			return false
		}
		// Asked last, because it is the only rule that touches disk.
		if bepinexGameOwnedRoot(gameRoot, name, members) {
			return false
		}
	}
	return true
}

// bepinexGameOwnedRoot reports whether the archive-root directory name is
// one the GAME itself owns - the fourth of shape F's refusals, and the only
// one that cannot be decided from the member list alone (#424 review,
// finding 1).
//
// For a BepInEx game mod_path IS the game root, so the archive root and the
// game root are ONE namespace: <Game>_Data/ is a root entry whose Managed/
// holds assemblies, and so, in their own way, are MonoBleedingEdge/,
// unstripped_corlib/, doorstop_libs/ and whatever else the engine or a
// second loader keeps beside the executable. Every one of them satisfies
// shape F's other three conditions exactly, and prefixing one with
// BepInEx/plugins/ takes a working game-data patch out of the tree the
// ENGINE reads and buries it where nothing looks.
//
// The test is what the game directory actually CONTAINS rather than a list
// of names, which would have to grow with every engine, launcher and loader
// lmm meets. Two halves, and the second is what keeps the rule from
// swallowing the case shape F exists for:
//
//	the game root has a directory of this name (matched case-insensitively,
//	because the archive was very likely authored on Windows); AND
//
//	that directory holds at least one file this member list does not
//	account for.
//
// The second half is the difference between "the game owns this" and "lmm
// put this here". A plugin folder lmm misdeployed into the game root (the
// #424 state `verify --fix` exists to repair, and the state a re-import
// walks into) holds EXACTLY the members being classified, so it is not the
// game's; <Game>_Data/ holds the whole engine besides, so it is. It also
// answers the question the repair actually needs - would this directory
// survive an undeploy - without consulting deployed_files, which the plan
// and the download ingest cannot read for a profile they were never given.
//
// Unreadable in any way - a walk error, a permission refusal, a symlink
// where a directory was expected - counts as the game's. A refusal to
// classify deploys the archive verbatim with a warning, which is the safe
// direction for an archive lmm genuinely cannot read.
func bepinexGameOwnedRoot(gameRoot, name string, members []string) bool {
	if gameRoot == "" || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return false
	}
	entries, err := os.ReadDir(gameRoot)
	if err != nil {
		return false // no game root to consult: the member list is all there is
	}
	actual := ""
	for _, e := range entries {
		if strings.EqualFold(e.Name(), name) {
			actual = e.Name()
			break
		}
	}
	if actual == "" {
		return false
	}
	dir := filepath.Join(gameRoot, actual)
	// Stat, not the DirEntry's own type: a game whose <Game>_Data is a
	// symlink (a split install, a case-folding overlay) still owns it.
	if info, serr := os.Stat(dir); serr != nil || !info.IsDir() {
		return serr != nil
	}

	accounted := make(map[string]bool, len(members))
	for _, m := range members {
		root, rest, nested := strings.Cut(m, "/")
		if !nested || !strings.EqualFold(root, name) {
			continue
		}
		accounted[strings.ToLower(rest)] = true
	}

	owned := false
	werr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			owned = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || !accounted[strings.ToLower(filepath.ToSlash(rel))] {
			owned = true
			return filepath.SkipAll
		}
		return nil
	})
	return owned || werr != nil
}

// bepinexConfigPrefix is the one directory under BepInEx/ whose contents
// are the USER's after the first deploy.
const bepinexConfigPrefix = "BepInEx/config/"

// isBepInExConfigMember reports whether a deploy-relative path is seeded
// BepInEx configuration rather than linked mod content (#358 (b)).
//
// BepInEx generates these files on first run and users hand-edit them
// afterwards; a mod that ships one is seeding a DEFAULT. Deploying it as a
// symlink like every other member would make the user's edit either fail
// (a read-only cache) or silently write back INTO the cache, where the next
// re-download overwrites it and every other profile sharing the entry
// inherits it. So they take profile-config-override semantics instead
// (overrides.go): a real file, copied on first deploy, never overwriting
// what is already there, and never entered into deployed_files - the same
// treatment applyProfileOverrides gives an INI a profile pins.
//
// The test is the PATH, not the game's loader declaration: the path only
// exists after normalisation has already decided this archive is BepInEx's,
// and a game whose mods genuinely keep content at BepInEx/config/ without
// a loader is not a shape that exists.
func isBepInExConfigMember(deployPath string) bool {
	return strings.HasPrefix(filepath.ToSlash(deployPath), bepinexConfigPrefix)
}

// normalizeBepInExTree applies bepinexNormalise's rules to a PRISTINE
// extracted tree at root, in place, and returns the layout it recognised so
// the caller can surface its warnings.
//
// "Pristine" is load-bearing and is what both ingest sites already produce:
// extractIntoStaging extracts into a sibling directory precisely so this
// archive's members can be told apart from an earlier file's, and
// importWithIdentity extracts into a fresh staging directory before moving
// it into the cache. Rewriting a SEEDED tree would move an earlier file's
// members too, whose paths this archive's listing says nothing about.
//
// It is one function shared by both sites rather than a transformation each
// applies to its own walk, for archive_listing.go's reason: two copies of
// these rules would drift the first time either side changed, and the whole
// contract is that a plan, a download and an archive import place a plugin
// at the same path.
//
// A dropped member is removed and a moved one renamed; the directories a
// move empties are cleaned up, so no phantom wrapper survives into `lmm mod
// files` or a plan readout. Nothing is touched at all when the layout does
// not apply, and nothing is touched when it errors - a framework pack is
// refused with the tree exactly as it arrived, so the caller's own cleanup
// has a coherent directory to remove.
func normalizeBepInExTree(root, modName string, loaderDeclared bool, gameRoot string) (*bepinexLayout, error) {
	members, err := relativeFileMembers(root)
	if err != nil {
		return nil, fmt.Errorf("listing extracted members: %w", err)
	}
	slash := make([]string, len(members))
	for i, m := range members {
		slash[i] = filepath.ToSlash(m)
	}

	layout, err := bepinexNormalise(slash, modName, loaderDeclared, gameRoot)
	if err != nil {
		return nil, err
	}
	if !layout.Applies() {
		return layout, nil
	}

	// vacated is every member this function moved away or dropped, spelled
	// as it arrived. It is the bound CleanupEmptyDirs needs (#415): a
	// directory can only have been emptied HERE by one of these leaving it,
	// so every candidate is on one of their ancestor chains, and nothing
	// else in the extracted tree is lmm's to tidy.
	vacated := make([]string, 0, len(slash))
	for i, member := range slash {
		src := filepath.Join(root, members[i])
		dest, kept := layout.Rewrite(member)
		if !kept {
			if err := os.Remove(src); err != nil {
				return nil, fmt.Errorf("dropping archive metadata %s: %w", member, err)
			}
			vacated = append(vacated, members[i])
			continue
		}
		if dest == member {
			continue
		}
		dst := filepath.Join(root, filepath.FromSlash(dest))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, fmt.Errorf("preparing %s: %w", dest, err)
		}
		if err := os.Rename(src, dst); err != nil {
			return nil, fmt.Errorf("normalising %s to %s: %w", member, dest, err)
		}
		vacated = append(vacated, members[i])
	}

	// The wrapper directory a shape-C strip emptied, and the bare
	// plugins/patchers/ roots a shape-B prefix emptied, would otherwise
	// survive as empty directories in the cache entry. The sweep runs after
	// the whole loop, so a directory several members shared is only empty -
	// and only removed - once the last of them has left it.
	linker.CleanupEmptyDirs(root, vacated)
	return layout, nil
}

// warnings is Warnings with a nil-safe receiver, so a caller holding a
// layout it never resolved (a non-extract import kind) can splice the
// diagnostics in without a branch.
func (l *bepinexLayout) warnings() []string {
	if l == nil {
		return nil
	}
	return l.Warnings
}

// bepinexGate answers the one question every rule in this file is gated on:
// is this a BepInEx game? And, when it is, did the DECLARATION say so or did
// the disk?
//
// Two sources, because they answer different halves of the same fact and a
// user has only ever supplied one of them (#424). The `loader:` block is a
// statement of intent lmm asks for; BepInEx/core/BepInEx.Preloader.dll in
// the install directory is a fact lmm can read, and nothing else plausibly
// puts that file there. A Valheim entry added before the catalog declared
// the loader (#416) has the second and not the first, and treating that as
// "not a BepInEx game" is what let a plugin extract verbatim into a Steam
// install directory and report success.
//
// DetectedOnly is what the caller owes the user for acting on the disk
// rather than on their configuration: one notice naming the command that
// makes the answer permanent (bepinexUndeclaredNotice). It is never set for
// a declaring game, because there is nothing for that user to do.
type bepinexGate struct {
	// Gated reports that the BepInEx layout rules apply to this game.
	Gated bool
	// DetectedOnly reports that Gated is true because of the install
	// directory alone - the game declares no loader.
	DetectedOnly bool
}

// bepinexGateFor resolves the gate for game. A nil game is not a BepInEx
// game, so every caller can ask without a guard.
func bepinexGateFor(game *domain.Game) bepinexGate {
	if game.DeclaresBepInEx() {
		return bepinexGate{Gated: true}
	}
	if game != nil && regularFileAt(game.InstallPath, bepinexPreloaderPath) {
		return bepinexGate{Gated: true, DetectedOnly: true}
	}
	return bepinexGate{}
}

// noteUndeclaredBepInEx appends the one notice a detected-but-undeclared
// game gets, to a layout that actually DID something on the strength of that
// detection.
//
// On the layout's own Warnings rather than through a channel of its own, so
// it rides every route #358's "layout lmm cannot place" warning already
// takes: the import PLAN both frontends render before committing, and each
// ingest's log. Nothing on the wire grows a field for it.
//
// Only when the layout applies: a game whose BepInEx install lmm noticed
// while importing a mod that is not a BepInEx mod at all has been told
// nothing useful, and saying it anyway would put the notice on every import
// into that game forever.
func noteUndeclaredBepInEx(layout *bepinexLayout, game *domain.Game, gate bepinexGate) {
	if !gate.DetectedOnly || !layout.Applies() {
		return
	}
	layout.Warnings = append(layout.Warnings, bepinexUndeclaredNotice(game))
}

// bepinexUndeclaredNotice is that notice's exact text, named once because
// both ingests and the plan must say the same thing.
func bepinexUndeclaredNotice(game *domain.Game) string {
	return fmt.Sprintf("BepInEx found in %s; declare it with `lmm game edit %s --loader bepinex`",
		game.InstallPath, game.ID)
}

// requireDeclaredLoader is #359's precondition: an archive whose layout this
// normaliser RECOGNISED is a BepInEx mod, so a game with no BepInEx loader
// cannot usefully take it.
//
// It is asked at the earliest point each flow can answer it - plan time for
// an archive import (whose listing is available before anything touches
// disk), and ingest time for a download (whose shape is not knowable until
// the archive is extracted, which is why PlanInstall cannot answer it; a
// cache fill is not a mutation of managed state, Ruling 1, and the refusal
// still lands before any deploy or DB write).
//
// Only a layout that APPLIES infers a requirement. That is deliberately
// narrower than "looks vaguely BepInEx-ish": the two ambiguous shapes do not
// apply for an undeclared game (bepinexNormalise's gate), so a mod for
// another game rooted at `plugins/` infers nothing and its owner is never
// told to install a loader they do not need.
//
// #424 widened WHAT satisfies it from the declaration to the gate: a game
// with BepInEx actually installed has the loader, whatever games.yaml says,
// and refusing the plugin would be refusing on a paperwork technicality
// while the thing the paperwork describes is right there on disk. Such a
// user gets the notice instead (noteUndeclaredBepInEx), which is the same
// remedy this error's Setup steps end with.
func requireDeclaredLoader(game *domain.Game, modName string, layout *bepinexLayout, gate bepinexGate) error {
	if !layout.Applies() || gate.Gated {
		return nil
	}
	return newLoaderRequiredError(game, modName, layout.Shape.String())
}

// Package bepinex: this file holds the archive-root NORMALISER (#358) - the
// rules that turn a BepInEx plugin archive's member list into the
// game-directory-relative paths it should deploy to.
//
// It moved here from internal/core/bepinex_layout.go in U3 (#413) with one
// change: the `loaderDeclared bool` parameter is gone. It existed to ask
// "is this a BepInEx game?" of a normaliser internal/core ran for EVERY
// game; an adapter is only ever asked about the game it is the adapter for,
// so the question is answered by selection and the ambiguous shapes are
// simply recognised (design §2, decision 12). The one caller that still
// needs the ungated answer - ClaimArchive, asked about a game this is NOT
// the adapter for - passes gated=false to the shared implementation below.
//
// Almost every rule is a pure function of the member list. The one
// exception is shape F's adapter.GameOwnsDir probe, which reads the game's
// own install directory, because one question genuinely cannot be answered
// from the archive alone:
// for a BepInEx game mod_path IS the game root, so an archive root
// directory and a directory the GAME owns are the same kind of name, and
// only the game directory itself can say which this is (#424).
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
package bepinex

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// frameworkPackRefusal is the message wrapped around adapter.ErrNotAMod for
// an archive that IS BepInEx rather than a mod for it: a payload under
// BepInEx/core/ is the loader, which is a per-game prerequisite living in
// the game root and surviving a profile switch (spike §2). Installing it as
// a mod would put the preloader under lmm's deployed-files bookkeeping,
// where the next uninstall or profile switch tears the loader out from
// under every plugin.
const frameworkPackRefusal = "it installs BepInEx/core/, which lmm configures per game as a loader rather than tracking as a profile member - install BepInEx into the game directory yourself and declare it with `lmm game edit <game> --loader bepinex` (`lmm game show <game>` then prints the launch option to paste)"

// shape is which of the observed archive layouts an archive has.
type shape int

// The shape values. shapeNone means "not a layout this normaliser
// recognises" - the archive is left exactly as it arrived.
const (
	// shapeNone is an archive the rules do not recognise. Nothing is
	// rewritten and nothing is dropped: "warns, never guesses".
	shapeNone shape = iota
	// shapeRooted is shape A: BepInEx/ at the archive root, which deploys
	// as-is (only the root metadata is dropped).
	shapeRooted
	// shapeWrapped is shape C: one wrapper directory containing BepInEx/,
	// whose single leading segment is stripped.
	shapeWrapped
	// shapeRelative is shape B: a root of bare plugins/ patchers/ monomod/
	// config/ directories, prefixed with BepInEx/.
	shapeRelative
	// shapePlugin is a loose root .dll (with no directory at all), which
	// becomes BepInEx/plugins/<ModName>/<file>.
	shapePlugin
	// shapePluginFolder is shape F (#424): a root of one or more
	// DIRECTORIES that each hold an assembly somewhere inside, which is
	// what a NexusMods Unity mod page ships - a folder meant to be dropped
	// into BepInEx/plugins/ whole. Each root directory is prefixed with
	// BepInEx/plugins/ and otherwise kept verbatim.
	shapePluginFolder
)

// String returns the shape's diagnostic name. It is adapter.Layout.Kind and
// LoaderRequiredError's evidence, never a wire value a frontend branches on.
func (s shape) String() string {
	switch s {
	case shapeRooted:
		return "game-root-relative"
	case shapeWrapped:
		return "wrapped in a single directory"
	case shapeRelative:
		return "BepInEx-relative"
	case shapePlugin:
		return "a loose plugin assembly"
	case shapePluginFolder:
		return "a plugin folder"
	default:
		return "unrecognised"
	}
}

// dirName is BepInEx's own spelling of its game-root directory, and the
// CANONICAL one every rewrite produces. An archive may spell it any way its
// author's filesystem allowed (`bepinex/`, `BEPINEX/`); lmm folds case when
// it recognises the directory and writes this spelling when it deploys,
// because the loader reads a fixed path and a safety rule that turns on one
// character is not a safety rule.
const dirName = "BepInEx"

// rootDirs are the directories BepInEx itself owns under BepInEx/, and
// therefore the root names that identify shape B. patchers and monomod are
// preload-time; plugins is the ordinary case; config is seeded
// configuration (see route.go).
var rootDirs = []string{"plugins", "patchers", "monomod", "config"}

// ownedDirs are every directory BepInEx itself owns under BepInEx/: the
// shape-B roots above, plus the two the loader keeps for itself - core (the
// framework's own assemblies, which the framework refusal turns on) and
// cache. canonicalRoot folds these as well as the BepInEx/ directory above
// them, because the rules below it compare a TWO-segment prefix and the
// loader reads all of them at a fixed spelling.
var ownedDirs = append([]string{"core", "cache"}, rootDirs...)

// metadataStems are the root FILES a Thunderstore package carries and lmm
// must never deploy: they describe the package to the website, and
// deploying them scatters a manifest.json and an icon.png into the game
// root of every game a plugin is installed into (spike §1.3).
//
// Matched on the STEM, case-insensitively, rather than on a fixed set of
// full names: README.txt, LICENSE and CHANGELOG.txt are as common on
// Thunderstore as their .md spellings, and an extension-exact list quietly
// deployed those into the Steam install directory. The shapes were also
// observed from Windows-authored archives, where README.md and Readme.md
// are the same file to their authors.
var metadataStems = []string{"manifest", "icon", "readme", "changelog", "license"}

// layout is the normaliser's INTERNAL answer about one archive, before it
// is handed to core as an adapter.Layout: the shape it recognised, the
// rewrite each member takes, and any warning worth showing the user.
//
// It exists beside adapter.Layout rather than being replaced by it because
// two of its fields are this package's own business - the recognised Shape,
// which ClaimArchive reports as evidence, and loaderRoot, which is how a
// MIXED root (BepInEx/ beside a sibling) says "unrecognised as a layout,
// but still unmistakably BepInEx" (#424 re-review, finding A).
type layout struct {
	// Shape is the recognised layout, or shapeNone.
	Shape shape
	// Warnings are user-facing diagnostics, surfaced verbatim.
	Warnings []string
	// rewrites maps an archive member to the cache-entry-relative path it
	// deploys to, in adapter.Layout's convention: "" means DROP, and an
	// absent member keeps its own name.
	rewrites map[string]string
	// applies reports that the rules recognised something.
	applies bool
	// loaderRoot records that the archive root held BepInEx/ even though
	// the layout ended up unrecognised (a mixed root, step 4b): the loader
	// requirement is still inferable, so ClaimArchive reads it.
	loaderRoot bool
}

// asAdapterLayout converts the internal answer into the seam's own type.
// A layout that does not apply becomes the identity Layout, which still
// carries the warnings - an archive lmm could not place is left alone AND
// reported.
func (l *layout) asAdapterLayout() adapter.Layout {
	if !l.applies {
		return adapter.Layout{Kind: l.Shape.String(), Warnings: l.Warnings}
	}
	out := adapter.NewLayout(l.Shape.String(), l.rewrites)
	out.Warnings = l.Warnings
	return out
}

// NormalizeArchive is the adapter's rule table for a BepInEx game: it maps
// the archive's members to the game-root-relative paths they deploy to, and
// touches no disk except for shape F's game-owned refusal
// (adapter.GameOwnsDir).
//
// The gate `loaderDeclared` used to carry is structural now - core only
// resolves this adapter for a game that HAS BepInEx - so every shape is
// recognised here, including the two that used to need the declaration.
//
// The only error is the framework-pack refusal (adapter.ErrNotAMod). An
// archive the rules do not recognise is a WARNING on an identity Layout,
// never a failure, so a caller may hold the result unconditionally.
func (a *Adapter) NormalizeArchive(req adapter.NormalizeRequest) (adapter.Layout, error) {
	gameRoot := ""
	if req.Game != nil {
		gameRoot = req.Game.InstallPath
	}
	l, err := normalise(req.Members, req.ModName, true, gameRoot)
	if err != nil {
		return adapter.Layout{}, err
	}
	noteUndeclared(l, req.Game)
	return l.asAdapterLayout(), nil
}

// noteUndeclared appends the one notice a DETECTED-but-undeclared game gets
// (#424): lmm acted on BepInEx it found in the install directory rather than
// on the user's configuration, so it names the command that makes the answer
// permanent.
//
// It rides the layout's own Warnings rather than a channel of its own, so it
// takes every route #358's "layout lmm cannot place" warning already takes:
// the import PLAN both frontends render before committing, and each ingest's
// log. Nothing on the wire grows a field for it.
//
// Only when the layout APPLIES: a game whose BepInEx install lmm noticed
// while ingesting a mod that is not a BepInEx mod at all has been told
// nothing useful, and saying it anyway would put the notice on every import
// into that game forever. And never for a DECLARING game, because there is
// nothing for that user to do.
func noteUndeclared(l *layout, game *domain.Game) {
	if game == nil || game.DeclaresBepInEx() || !l.applies {
		return
	}
	l.Warnings = append(l.Warnings, undeclaredNotice(game))
}

// undeclaredNotice is that notice's exact text.
func undeclaredNotice(game *domain.Game) string {
	return fmt.Sprintf("BepInEx found in %s; declare it with `lmm game edit %s --loader bepinex`",
		game.InstallPath, game.ID)
}

// normalise applies the archive-root rules to members (the archive's file
// listing, slash-separated and archive-relative), naming a loose plugin's
// directory after modName.
//
// gated is TRUE for the game's own adapter, where every shape is
// recognised, and FALSE for ClaimArchive - asked about a game this is not
// the adapter for, where only the UNMISTAKABLE shapes may be claimed:
//
//	shape A (BepInEx/ at the root), shape C (one wrapper dir containing
//	BepInEx/) and a framework pack are recognised either way, because an
//	archive that names the directory `BepInEx` is not plausibly anything
//	else;
//
//	shape B (a bare plugins/ patchers/ monomod/ config/ root), shape F (a
//	root of plugin FOLDERS, #424) and a loose root .dll are recognised only
//	when gated, because `plugins/`, `*.dll` and "a directory with an
//	assembly in it" are ordinary shapes that other games' mods use - a
//	7 Days to Die archive rooted at `plugins/` or at `Mods/<Mod>/<Mod>.dll`
//	must keep deploying to <mod_path>, and silently moving it under a
//	BepInEx/ directory the game has never heard of would break a working
//	install with no error to read.
//
// That is the "OR" form of the requirement, split per shape rather than
// applied wholesale: "unmistakably BepInEx-shaped" is a property of the
// individual shape, not of the archive as a category.
//
// gameRoot is the game's install directory, which for a BepInEx game is
// also where these paths deploy. It is consulted by exactly one rule -
// shape F's game-owned refusal (adapter.GameOwnsDir) - and "" means "no game
// directory to consult", which every pure-rule unit test passes and which
// leaves the member list as the only evidence.
func normalise(members []string, modName string, gated bool, gameRoot string) (*layout, error) {
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

	l := &layout{rewrites: make(map[string]string, len(payload))}
	// dropped collects the members steps 1 and 2b remove, so they can be
	// written into the rewrite table as explicit drops. adapter.Layout's
	// convention is the inverse of the one this normaliser grew up with -
	// there, a member ABSENT from the table keeps its own name and a
	// member mapped to "" is dropped - which is what lets core hold one
	// table for every adapter.
	var dropped []string

	// Step 1: drop the package metadata every Thunderstore archive carries
	// at its root, and take stock of what is left.
	keptOrigins, keptPayload := origins[:0], payload[:0]
	for i, m := range payload {
		if isMetadata(m) {
			dropped = append(dropped, origins[i])
			continue
		}
		keptOrigins = append(keptOrigins, origins[i])
		keptPayload = append(keptPayload, m)
	}
	origins, payload = keptOrigins, keptPayload

	// Step 2: a single root directory that itself contains BepInEx/ is
	// shape C's wrapper, and its one leading segment comes off. Done before
	// every other test, so the stripped tree is judged as shape A.
	strip, wrapped := wrapperDir(payload)
	sh := shapeRooted
	if wrapped {
		sh = shapeWrapped
	}

	stripped := make([]string, len(payload))
	for i, m := range payload {
		stripped[i] = canonicalRoot(strings.TrimPrefix(m, strip))
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
			if isMetadata(s) {
				dropped = append(dropped, origins[i])
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
	case rootHas(stripped, dirName):
		// shape A, or shape C after the strip: deploys as-is - unless the
		// root carries something BESIDE BepInEx/, which is the same
		// half-recognised archive the three sibling refusals below already
		// report rather than guess at (#424 review, finding 2). Noted
		// rather than returned, so step 4's framework refusal still runs:
		// a safety check that can be walked past by adding one file to the
		// archive is not a safety check.
		mixedLoaderRoot = hasNonLoaderRoot(stripped)
	case relativeRoot(stripped):
		sh, prefix = shapeRelative, "BepInEx/"
	case loosePluginRoot(stripped):
		sh, prefix = shapePlugin, "BepInEx/plugins/"+modName+"/"
	case pluginFolderRoot(stripped, gameRoot):
		sh, prefix = shapePluginFolder, "BepInEx/plugins/"
	default:
		l.Shape = shapeNone
		if gated && len(payload) > 0 {
			l.Warnings = append(l.Warnings, unrecognisedWarning(modName, payload))
		}
		return l, nil
	}

	// Step 4: the framework refusal, judged on the FINAL paths so a wrapped
	// pack (BepInExPack/BepInEx/core/...) is caught alongside a bare one.
	// Before the gate, deliberately: a framework pack is unmistakable, and
	// silently installing one into a game with no loader is the worst
	// outcome available - the loader lands under lmm's deployed-files
	// bookkeeping and the next profile switch removes it.
	for _, m := range stripped {
		if strings.HasPrefix(canonicalRoot(prefix+m), dirName+"/core/") {
			return nil, fmt.Errorf("%w: %s", adapter.ErrNotAMod, frameworkPackRefusal)
		}
	}

	// Step 4b: the mixed loader root, refused after the framework check and
	// before the gate - `BepInEx` is a name no other game's mod plausibly
	// uses, so this refusal is not one the declaration could make safe.
	if mixedLoaderRoot {
		l.Shape, l.loaderRoot = shapeNone, true
		l.Warnings = append(l.Warnings, unrecognisedWarning(modName, payload))
		return l, nil
	}

	// Step 5: the gate. An ambiguous shape is only this adapter's to claim
	// when it is answering about its OWN game.
	if !gated && sh != shapeRooted && sh != shapeWrapped {
		return &layout{Shape: shapeNone}, nil
	}

	// Step 6: build the rewrite map, refusing a collision rather than
	// letting one member silently overwrite another at deploy time.
	taken := make(map[string]string, len(stripped))
	for i, m := range origins {
		// Canonicalised once more with the prefix ON: a shape-B root
		// spelled `Config/` only becomes a BepInEx-owned path here, and it
		// is this path RouteFile and the deploy both read.
		dest := canonicalRoot(prefix + stripped[i])
		if prev, dup := taken[dest]; dup {
			return nil, fmt.Errorf("normalising the BepInEx layout of %s: members %q and %q both deploy to %q", modName, prev, m, dest)
		}
		taken[dest] = m
		l.rewrites[m] = dest
	}
	for _, m := range dropped {
		l.rewrites[m] = ""
	}
	l.Shape, l.applies = sh, true
	return l, nil
}

// isMetadata reports whether member is one of the package-metadata files at
// the archive ROOT. Only the root: a plugin that legitimately ships its own
// README under BepInEx/plugins/Foo/README.md keeps it.
func isMetadata(member string) bool {
	if strings.ContainsAny(member, `/\`) {
		return false
	}
	stem := strings.ToLower(strings.TrimSuffix(member, path.Ext(member)))
	for _, f := range metadataStems {
		if stem == f {
			return true
		}
	}
	return false
}

// canonicalRoot rewrites a member's leading BepInEx directory - AND the
// directory BepInEx owns immediately below it - to the project's own
// spelling, so a case-variant archive deploys where the loader actually
// looks and every rule below it (the shape-A test, the framework refusal,
// RouteFile) can go on comparing exactly.
//
// Both segments, not just the first: the framework refusal and RouteFile
// each test a TWO-segment prefix, so folding only the first left
// `BepInEx/Core/` classified as an ordinary plugin archive and installed as
// a mod, and left a `BepInEx/Config/` file deployed as a symlink into the
// cache (re-review R1/R2). A safety refusal reached by a one-character
// difference is not a safety refusal.
//
// No deeper than that: what a plugin names its own subdirectories under
// BepInEx/plugins/Foo/ is that plugin's business, and so is a directory
// under BepInEx/ that BepInEx does not own.
func canonicalRoot(member string) string {
	name, rest, nested := strings.Cut(member, "/")
	if !nested || !strings.EqualFold(name, dirName) {
		return member
	}
	sub, tail, deeper := strings.Cut(rest, "/")
	for _, dir := range ownedDirs {
		if strings.EqualFold(sub, dir) {
			if deeper {
				return dirName + "/" + dir + "/" + tail
			}
			return dirName + "/" + dir
		}
	}
	return dirName + "/" + rest
}

// wrapperDir reports shape C: exactly one root entry, that entry a
// directory, and that directory containing BepInEx/ (any spelling of it -
// canonicalRoot fixes the spelling once the strip has happened). strip is
// the prefix to remove ("Wrapper/"), empty when there is no wrapper.
func wrapperDir(members []string) (strip string, wrapped bool) {
	roots := rootNames(members)
	if len(roots) != 1 {
		return "", false
	}
	strip = roots[0] + "/"
	for _, m := range members {
		inner, _, nested := strings.Cut(strings.TrimPrefix(m, strip), "/")
		if nested && strings.EqualFold(inner, dirName) {
			return strip, true
		}
	}
	return "", false
}

// rootNames lists the distinct first path segments of members, sorted, so a
// diagnostic naming an archive's root is stable.
func rootNames(members []string) []string {
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

// rootHas reports whether members place anything under the root DIRECTORY
// named dir. Compared exactly: callers pass paths that canonicalRoot has
// already put into BepInEx's own spelling.
func rootHas(members []string, dir string) bool {
	for _, m := range members {
		if strings.HasPrefix(m, dir+"/") {
			return true
		}
	}
	return false
}

// relativeRoot reports shape B: every root entry is a DIRECTORY named in
// rootDirs. "Every", not "any": one recognised directory beside an
// unrecognised one is an archive whose author meant something this
// normaliser cannot see, and prefixing half of it would be a guess.
func relativeRoot(members []string) bool {
	if len(members) == 0 {
		return false
	}
	for _, m := range members {
		name, rest, nested := strings.Cut(m, "/")
		if !nested || rest == "" {
			return false // a root FILE: not shape B
		}
		found := false
		for _, dir := range rootDirs {
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

// hasNonLoaderRoot reports whether the root carries an entry other than
// `BepInEx` itself - the test behind step 3's mixed-loader-root refusal
// (#424 review, finding 2).
//
// `BepInEx` is the one name BepInEx owns that the shape refusals never
// reached, because rootHas short-circuits the classification switch before
// any of them is asked. A root of `BepInEx/patchers/Pre.dll` beside
// `Jotunn/Jotunn.dll` therefore read as shape A, and the plugin folder
// deployed verbatim into the game root - #424's own bug, on a game that
// DOES declare the loader, and with no warning on the plan.
//
// Every sibling, not only the ambiguous ones: a `plugins/` root beside
// `BepInEx/` deploys to the game root just as wrongly as a plugin folder
// does, and a loose file beside `BepInEx/` is the author saying something
// about that file this normaliser cannot read. Package metadata has already
// been dropped by the time this is asked, so a Thunderstore package's
// manifest and icon are not siblings.
func hasNonLoaderRoot(members []string) bool {
	for _, m := range members {
		name, _, _ := strings.Cut(m, "/")
		if !strings.EqualFold(name, dirName) {
			return true
		}
	}
	return false
}

// unrecognisedWarning is the one sentence every refusal to guess shares:
// what lmm saw, what it did instead, and what to change. Named once because
// the two refusal sites must say the same thing.
func unrecognisedWarning(modName string, payload []string) string {
	return fmt.Sprintf(
		"lmm did not recognise %s as a BepInEx layout (its root holds %s), so its files deploy exactly as the archive lists them; move them under BepInEx/plugins/ inside the archive if that is wrong",
		modName, strings.Join(rootNames(payload), ", "))
}

// loosePluginRoot reports the loose-plugin shape: every remaining member is
// a root FILE, and at least one of them is an assembly. The non-.dll files
// come along (a plugin shipping a .dll beside its own .cfg or asset
// bundle), because splitting them would deploy half a mod.
func loosePluginRoot(members []string) bool {
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

// pluginFolderRoot reports shape F (#424): every root entry is a DIRECTORY
// that holds at least one assembly somewhere inside it, none of them is a
// name BepInEx owns, and none of them is a directory the GAME owns.
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
//	NO root directory is a name BepInEx owns. relativeRoot (shape B) has
//	first refusal on those, and it only answers when they are the WHOLE
//	root; a root mixing `plugins/` with `Jotunn/` is the same
//	half-recognised archive shape B already refuses, so it is reported
//	rather than prefixed.
//
//	NO root directory is one the GAME owns. That is the one condition the
//	member list cannot answer, so adapter.GameOwnsDir asks the game
//	directory - see its own doc comment for why a name list will not do.
func pluginFolderRoot(members []string, gameRoot string) bool {
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
		for _, dir := range append([]string{dirName}, ownedDirs...) {
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
		if adapter.GameOwnsDir(gameRoot, name, members) {
			return false
		}
	}
	return true
}

// Package core: this file holds the BepInEx archive-root NORMALISER (#358) -
// the pure rules that turn a BepInEx plugin archive's member list into the
// game-directory-relative paths it should deploy to.
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
// unassisted is the other two real shapes, and the metadata every
// Thunderstore package carries at its root.
//
// See docs/plans/2026-09-09-bepinex-spike.md §1.3 (the observed shapes) and
// §3 (the deploy mapping) for the evidence behind each rule.
package core

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
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
	default:
		return "unrecognised"
	}
}

// bepinexRootDirs are the directories BepInEx itself owns under BepInEx/,
// and therefore the root names that identify shape B. patchers and monomod
// are preload-time; plugins is the ordinary case; config is seeded
// configuration (see isBepInExConfigMember).
var bepinexRootDirs = []string{"plugins", "patchers", "monomod", "config"}

// bepinexMetadataFiles are the root FILES a Thunderstore package always
// carries and lmm must never deploy: they describe the package to the
// website, and deploying them scatters a manifest.json and an icon.png into
// the game root of every game a plugin is installed into (spike §1.3).
// Matched case-insensitively on the base name - the shapes were observed
// from Windows-authored archives, and README.md/Readme.md are the same
// file to their authors.
var bepinexMetadataFiles = []string{"manifest.json", "icon.png", "readme.md", "changelog.md"}

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
//	shape B (a bare plugins/ patchers/ monomod/ config/ root) and a loose
//	root .dll are recognised ONLY for a game that declares the loader,
//	because `plugins/` and `*.dll` are ordinary names that other games'
//	mods use - a 7 Days to Die archive rooted at `plugins/` must keep
//	deploying to <mod_path>/plugins, and silently moving it under a
//	BepInEx/ directory the game has never heard of would break a working
//	install with no error to read.
//
// That is the "OR" form of the requirement, split per shape rather than
// applied wholesale: "unmistakably BepInEx-shaped" is a property of the
// individual shape, not of the archive as a category.
//
// The error is ErrBepInExFrameworkPack, and only that: an unrecognised
// archive is a warning on the returned layout, never a failure. A layout is
// always returned alongside a nil error, so a caller may hold it
// unconditionally.
func bepinexNormalise(members []string, modName string, loaderDeclared bool) (*bepinexLayout, error) {
	clean := make([]string, 0, len(members))
	for _, m := range members {
		if c := path.Clean(strings.ReplaceAll(m, `\`, "/")); c != "." && c != "/" {
			clean = append(clean, m)
		}
	}

	layout := &bepinexLayout{rewrites: make(map[string]string, len(clean))}

	// Step 1: drop the package metadata every Thunderstore archive carries
	// at its root, and take stock of what is left.
	payload := make([]string, 0, len(clean))
	for _, m := range clean {
		if isBepInExMetadata(m) {
			continue
		}
		payload = append(payload, m)
	}

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
		stripped[i] = strings.TrimPrefix(m, strip)
	}

	// Step 3: classify what the (possibly stripped) root now holds.
	prefix := ""
	switch {
	case bepinexRootHas(stripped, "BepInEx"):
		// shape A, or shape C after the strip: deploys as-is.
	case bepinexRelativeRoot(stripped):
		shape, prefix = bepinexShapeRelative, "BepInEx/"
	case bepinexLoosePluginRoot(stripped):
		shape, prefix = bepinexShapePlugin, "BepInEx/plugins/"+modName+"/"
	default:
		layout.Shape = bepinexShapeNone
		if loaderDeclared && len(payload) > 0 {
			layout.Warnings = append(layout.Warnings, fmt.Sprintf(
				"lmm did not recognise %s as a BepInEx layout (its root holds %s), so its files deploy exactly as the archive lists them; move them under BepInEx/plugins/ inside the archive if that is wrong",
				modName, strings.Join(bepinexRootNames(payload), ", ")))
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
		if strings.HasPrefix(prefix+m, "BepInEx/core/") {
			return nil, fmt.Errorf("%w: it installs BepInEx/core/, which lmm configures per game as a loader rather than tracking as a profile member - install BepInEx into the game directory yourself and declare it with `lmm game edit <game> --loader bepinex` (`lmm game show <game>` then prints the launch option to paste)", ErrBepInExFrameworkPack)
		}
	}

	// Step 5: the gate. An ambiguous shape needs the game's declaration.
	if !loaderDeclared && shape != bepinexShapeRooted && shape != bepinexShapeWrapped {
		return &bepinexLayout{Shape: bepinexShapeNone}, nil
	}

	// Step 6: build the rewrite map, refusing a collision rather than
	// letting one member silently overwrite another at deploy time.
	taken := make(map[string]string, len(stripped))
	for i, m := range payload {
		dest := prefix + stripped[i]
		if prev, dup := taken[dest]; dup {
			return nil, fmt.Errorf("normalising the BepInEx layout of %s: members %q and %q both deploy to %q", modName, prev, payload[i], dest)
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
	name := strings.ToLower(member)
	for _, f := range bepinexMetadataFiles {
		if name == f {
			return true
		}
	}
	return false
}

// bepinexWrapperDir reports shape C: exactly one root entry, that entry a
// directory, and that directory containing BepInEx/. strip is the prefix to
// remove ("Wrapper/"), empty when there is no wrapper.
func bepinexWrapperDir(members []string) (strip string, wrapped bool) {
	roots := bepinexRootNames(members)
	if len(roots) != 1 {
		return "", false
	}
	strip = roots[0] + "/"
	for _, m := range members {
		if strings.HasPrefix(m, strip+"BepInEx/") {
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
// DIRECTORY named dir.
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

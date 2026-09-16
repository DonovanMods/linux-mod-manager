// Package core: this file holds the archive LISTING and the pure
// member -> deployable-path normalisation that `lmm import <archive>`'s plan
// (PlanImportArchive) and its ingest (importWithIdentity) SHARE (#314).
//
// The sharing is the point. A plan that answered "what would this archive
// contribute" with its own reimplementation of the ingest's rules would drift
// the first time either side changed - so the classification (which of the
// four ingest branches an archive takes), the mod-name derivation, and the
// member-name normalisation each have exactly one implementation, called from
// both sides. The listing itself is the only thing the plan does differently:
// it READS the archive's table of contents (archive/zip natively, `7z l -slt`
// for 7z/rar) instead of extracting it, so planning leaves nothing on disk.
package core

import (
	"archive/zip"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// importArchiveKind is which of importWithIdentity's four branches an archive
// takes, and therefore what it contributes to the game directory. It is
// derived ONCE (classifyImportArchive) and consulted by both the plan and the
// ingest, so the two cannot disagree about a given archive.
type importArchiveKind int

const (
	// importKindExtract is the default branch: the archive is extracted and
	// every member becomes a deployable file.
	importKindExtract importArchiveKind = iota
	// importKindCopy is a DeployCopy game: the archive file itself is copied
	// into the cache and deployed as-is.
	importKindCopy
	// importKindMergeSource is a DeployCompile game's NATIVE merge source
	// (mc.IsNativeMergeSource, e.g. ".exmodz"): validate+retain only, so it
	// deploys nothing of its own and reaches the game through the merged
	// artifact instead (#197).
	importKindMergeSource
	// importKindConvertPak is a DeployCompile game's convertible artifact
	// (mc.IsConvertibleArtifact with game.ConvertPaks): retained AND kept as
	// a deployable copy, so the default state is raw deploy until the first
	// successful merge flips the manifest (#221).
	importKindConvertPak
)

// classifyImportArchive reports which branch importing filename into game
// takes. mc is the game's resolved compile ADAPTER, or nil for a game that
// needs none (every non-DeployCompile game): the format questions
// - is this the game's native merge source, is it a convertible artifact -
// are the adapter's to answer (#256/#412), and a DeployCompile game with no
// resolvable compiler never reaches here (importWithIdentity fails loud
// first).
func classifyImportArchive(game *domain.Game, mc adapter.MergeCompiler, filename string) importArchiveKind {
	if game.DeployMode == domain.DeployCompile {
		switch {
		case mc != nil && mc.IsNativeMergeSource(filename):
			return importKindMergeSource
		case mc != nil && isConvertEligibleArtifact(game, mc, filename):
			return importKindConvertPak
		}
	}
	if game.DeployMode == domain.DeployCopy {
		return importKindCopy
	}
	return importKindExtract
}

// archiveMember is one entry in an archive's table of contents: its
// archive-internal name exactly as recorded, and whether it is a directory.
// Directories are listed rather than dropped because they still participate
// in the reserved-namespace check and in the sole-top-level-directory
// question DetectModName answers from the extracted tree.
type archiveMember struct {
	Path string
	Dir  bool
}

// importMemberRelPath normalizes one archive member's name to the
// cache-entry-relative path extraction would write it to.
//
// It applies EXACTLY the rules the extractor applies per member, by calling
// the extractor's own sanitizePath against a synthetic root: lmm's reserved
// cache namespace is refused (a member named ".lmm-file-<id>" would forge a
// completion marker), a zip-slip escape is refused, and the name is cleaned.
// Sharing that function rather than restating its rules is what keeps a
// plan's file list from promising a member the ingest will reject.
func importMemberRelPath(name string) (string, error) {
	// An absolute synthetic root, so the traversal check behaves exactly as
	// it does against a real destination directory. Never touched on disk -
	// sanitizePath is pure string work.
	const root = string(filepath.Separator) + "lmm-archive-root"
	dest, err := (&Extractor{}).sanitizePath(root, name)
	if err != nil {
		return "", err
	}
	return filepath.Rel(root, dest)
}

// importDeployablePaths is the game-dir-relative file list importing an
// archive of this kind would contribute, sorted.
//
//   - importKindMergeSource contributes nothing (validate+retain only).
//   - importKindCopy and importKindConvertPak contribute the archive file
//     itself, under its own name.
//   - importKindExtract contributes every non-directory member, normalized
//     through importMemberRelPath. Duplicates collapse (two members of one
//     name extract onto one file), and a member claiming a reserved or
//     escaping name fails the whole listing rather than being skipped -
//     the same refusal the extractor makes, at the same granularity.
//
// The cache entry's own layout IS the game-directory layout (Installer.
// Install links versionDir/<file> to ModPath/<file>), so these paths need no
// further translation.
//
// The ADAPTER's own rewrite is deliberately NOT applied here (#413): this
// is the archive's raw contribution, and PlanImportArchive hands exactly
// this list to the adapter and then to rewritePlannedPaths, so the member
// list the adapter is asked about and the paths the plan renders are the
// same strings. Two rewrites - one here from a layout resolved separately,
// one there - was how the BepInEx normaliser and the adapter seam briefly
// coexisted, and the pair could disagree about what it had been given.
func importDeployablePaths(kind importArchiveKind, filename string, members []archiveMember) ([]string, error) {
	switch kind {
	case importKindMergeSource:
		return []string{}, nil
	case importKindCopy, importKindConvertPak:
		return []string{filename}, nil
	}

	paths := make([]string, 0, len(members))
	for _, m := range members {
		rel, err := importMemberRelPath(m.Path)
		if err != nil {
			return nil, err
		}
		if m.Dir {
			continue
		}
		paths = append(paths, rel)
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

// importedModName is the mod name an import records for archivePath's
// filename - the value both importWithIdentity and PlanImportArchive use, so a
// plan's readout names the mod the ingest will actually record. game answers
// isLoaderStructure's "is this game a BepInEx one" question (#450) - nil for
// a caller (a bare listing test) that has none.
//
// An extract-mode import takes the name from the archive's CONTENT (a sole
// top-level directory names the mod; anything else falls back to the archive
// base name), which is what DetectModName reads off the extracted tree and
// what modNameFromMembers derives from the listing. Every other kind names
// the mod after the archive file with its version suffix trimmed.
func importedModName(game *domain.Game, kind importArchiveKind, filename, version string, members []archiveMember) string {
	if kind == importKindExtract {
		return modNameFromMembers(game, members, filename)
	}
	return trimVersionSuffix(filename, version)
}

// modNameFromMembers is DetectModName's rule applied to a listing instead of
// an extracted directory: exactly one top-level entry, and that entry a
// directory, names the mod; anything else (several entries, a single
// top-level file, an empty archive) falls back to the archive's base name.
//
// A sole top-level BepInEx/ is not a name (#450): every BepInEx-rooted
// plugin package would import as "BepInEx". Its well-known subdirectories -
// plugins/, patchers/, monomod/, config/, core/ - sitting bare at the root
// are structure too, but ONLY for a game that actually loads mods through
// BepInEx (isLoaderStructure) - a mod genuinely named "Core" or "Plugins"
// for a game with no BepInEx involved keeps that name, the old rule. The
// rule then looks beneath the structure (payloadName), and falls back to
// the archive's base name when that says nothing.
func modNameFromMembers(game *domain.Game, members []archiveMember, archiveFilename string) string {
	top := topLevelEntries(members)
	if len(top) != 1 || !top[0].Dir {
		return stripExtension(archiveFilename)
	}
	if !isLoaderStructure(game, top[0].Path) {
		return top[0].Path
	}
	if name, ok := payloadName(members); ok {
		return name
	}
	return archiveBaseName(archiveFilename)
}

// archiveBaseName is modNameFromMembers' loader-structure fallback when
// nothing beneath the structure names the mod (a loose plugin file, or more
// than one payload folder): a NexusMods-style filename's own BaseName -
// "Jotunn" out of "Jotunn-1138-2-12-1-1700000000.zip" - is a real mod name,
// where the whole download-manager filename is not (#450). Anything else
// keeps the plain archive basename, unchanged from before.
func archiveBaseName(archiveFilename string) string {
	if parsed := ParseNexusModsFilename(archiveFilename); parsed != nil {
		return parsed.BaseName
	}
	return stripExtension(archiveFilename)
}

// topLevelEntries is members' distinct first path segments, each marked a
// directory when any member lies beneath it.
func topLevelEntries(members []archiveMember) []archiveMember {
	var top []archiveMember
	index := map[string]int{}
	for _, m := range members {
		name, rest, nested := strings.Cut(filepath.ToSlash(filepath.Clean(m.Path)), "/")
		dir := m.Dir || (nested && rest != "")
		if i, ok := index[name]; ok {
			top[i].Dir = top[i].Dir || dir
			continue
		}
		index[name] = len(top)
		top = append(top, archiveMember{Path: name, Dir: dir})
	}
	return top
}

// bepinexLoaderDir is BepInEx's own directory at the game root.
const bepinexLoaderDir = "bepinex"

// bepinexPayloadDirs are the BepInEx subdirectories a package drops its
// plugins into; bepinexStructureDirs adds the ones that hold anything else.
// Matched case-insensitively, as a Linux game directory built by hand often
// differs from the loader's own capitalisation.
var (
	bepinexPayloadDirs   = []string{"plugins", "patchers", "monomod"}
	bepinexStructureDirs = append([]string{"config", "core"}, bepinexPayloadDirs...)
)

// isLoaderStructure reports whether a sole top-level directory is the
// loader's structure rather than a mod's own folder.
//
// A literal BepInEx/ is ALWAYS structure, whatever the game: nothing else
// plausibly ships a top-level directory by that exact name. Its bare
// subdirectories (plugins/, patchers/, monomod/, config/, core/) are far
// more ambiguous - "Core-1.0.zip" containing "Core/..." is a real mod name
// for a game that has nothing to do with BepInEx - so those count as
// structure only for a game gameLoadsBepInEx says actually is one (#450).
func isLoaderStructure(game *domain.Game, name string) bool {
	name = strings.ToLower(name)
	if name == bepinexLoaderDir {
		return true
	}
	return gameLoadsBepInEx(game) && slices.Contains(bepinexStructureDirs, name)
}

// gameLoadsBepInEx reports whether game is a BepInEx game for the purpose of
// isLoaderStructure's bare-subdirectory rule (#450): hasBepInEx's own
// declared-or-installed test, widened by an explicit `adapter: bepinex` -
// a choice the user has typed but that neither declares the loader block
// nor (yet) has BepInEx laid out on disk. Not folded into hasBepInEx itself:
// that helper's contract elsewhere (the derivation, the verify/bypass
// checks) is deliberately declared-or-installed only, and widening it would
// change those call sites' meaning along with this one.
func gameLoadsBepInEx(game *domain.Game) bool {
	return hasBepInEx(game) || (game != nil && game.Adapter == bepinexAdapterID)
}

// payloadName is the one plugin directory beneath the loader's structure -
// BepInEx/plugins/<Name>/..., plugins/<Name>/..., and the patchers/ and
// monomod/ equivalents - or false when there is not exactly one, or when a
// plugin file sits loose beside it. Settings under config/ and the loader's
// own core/ are not a payload and never decide the name: a package's
// BepInEx/config/<guid>.cfg sits beside its plugin folder as a matter of
// course.
func payloadName(members []archiveMember) (string, bool) {
	var names []string
	for _, m := range members {
		parts := strings.Split(filepath.ToSlash(filepath.Clean(m.Path)), "/")
		if len(parts) > 0 && strings.EqualFold(parts[0], bepinexLoaderDir) {
			parts = parts[1:]
		}
		if len(parts) < 2 || !slices.Contains(bepinexPayloadDirs, strings.ToLower(parts[0])) {
			continue
		}
		rest := parts[1:]
		if len(rest) == 1 && !m.Dir {
			return "", false // a loose plugin file: nothing beneath names the mod
		}
		if !slices.Contains(names, rest[0]) {
			names = append(names, rest[0])
		}
	}
	if len(names) != 1 {
		return "", false
	}
	return names[0], true
}

// trimVersionSuffix drops filename's extension and, when version is a real
// version the base name ends with, that trailing "-<version>" too. Extracted
// from importWithIdentity, which applied it identically in its compile and copy
// branches.
func trimVersionSuffix(filename, version string) string {
	modName := strings.TrimSuffix(filename, filepath.Ext(filename))
	if version != "" && version != "unknown" {
		if idx := strings.LastIndex(modName, version); idx > 0 {
			modName = strings.TrimRight(modName[:idx], "-_ ")
		}
	}
	return modName
}

// listArchiveMembers reads archivePath's table of contents WITHOUT extracting
// it, in archive order. Format detection is the extractor's own
// (detectFormatFromPath, content sniffing included), so a plan and an ingest
// always agree about what an archive is; an unsupported format fails with the
// extractor's own wording.
//
// .7z and .rar are listed through the system `7z l -slt`, so a machine
// without p7zip gets the same "install p7zip-full" error the extraction path
// gives it - planning an import it could never perform is worse than failing
// early.
func listArchiveMembers(ctx context.Context, e *Extractor, archivePath string) ([]archiveMember, error) {
	switch e.detectFormatFromPath(archivePath) {
	case "zip":
		return listZipMembers(archivePath)
	case "7z", "rar":
		return list7zMembers(ctx, archivePath)
	default:
		if ext := filepath.Ext(archivePath); ext != "" {
			return nil, fmt.Errorf("unsupported archive format: %s", ext)
		}
		return nil, fmt.Errorf("unsupported archive format for path: %s", archivePath)
	}
}

// listZipMembers lists a zip's members with archive/zip's central directory -
// no member is decompressed and nothing is written.
func listZipMembers(archivePath string) (members []archiveMember, err error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("opening zip: %w", err)
	}
	defer func() {
		if cerr := r.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing zip: %w", cerr)
		}
	}()

	members = make([]archiveMember, 0, len(r.File))
	for _, f := range r.File {
		members = append(members, archiveMember{Path: f.Name, Dir: f.FileInfo().IsDir()})
	}
	return members, nil
}

// list7zMembers lists a .7z/.rar archive through `7z l -slt`, which prints
// one blank-line-separated block per entry after a "----------" separator.
// Only "Path" and "Attributes" are read: an entry whose attributes carry the
// DOS directory bit is a directory, everything else a file. The same timeout
// the extraction path uses guards a corrupt archive.
func list7zMembers(ctx context.Context, archivePath string) ([]archiveMember, error) {
	if _, err := exec.LookPath("7z"); err != nil {
		return nil, fmt.Errorf("7z command not found: install p7zip-full to extract .7z and .rar files")
	}

	ctx, cancel := context.WithTimeout(ctx, extract7zTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, "7z", "l", "-slt", archivePath).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("7z listing timed out after %v", extract7zTimeout)
		}
		return nil, fmt.Errorf("7z listing failed: %w\nOutput: %s", err, string(output))
	}
	return parse7zListing(string(output)), nil
}

// parse7zListing turns `7z l -slt` output into members. Everything before the
// "----------" separator is the ARCHIVE's own header block (which carries a
// "Path =" line naming the archive itself) and is skipped.
func parse7zListing(out string) []archiveMember {
	var members []archiveMember
	var current archiveMember
	started, inEntry := false, false

	flush := func() {
		if inEntry && current.Path != "" {
			members = append(members, current)
		}
		current, inEntry = archiveMember{}, false
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if !started {
			started = strings.TrimSpace(line) == "----------"
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Path":
			current.Path = value
			inEntry = true
		case "Attributes":
			// "D drwxr-xr-x" for a directory, "A -rw-r--r--" for a file:
			// the DOS attribute letters are the leading word.
			attrs, _, _ := strings.Cut(value, " ")
			current.Dir = strings.Contains(attrs, "D")
		}
	}
	flush()
	return members
}

// installHookNames lists the install.* hooks a pass would run, in run order -
// the vocabulary `lmm install`, `lmm deploy` and `lmm import <archive>` share.
// Only configured hooks are named, and skipHooks (the CLI's --no-hooks)
// suppresses every one of them.
func installHookNames(hooks *ResolvedHooks, skipHooks bool) []string {
	if skipHooks {
		return nil
	}
	var names []string
	for _, h := range []struct{ name, command string }{
		{"install.before_all", hooks.GetInstallBeforeAll()},
		{"install.before_each", hooks.GetInstallBeforeEach()},
		{"install.after_each", hooks.GetInstallAfterEach()},
		{"install.after_all", hooks.GetInstallAfterAll()},
	} {
		if h.command != "" {
			names = append(names, h.name)
		}
	}
	return names
}

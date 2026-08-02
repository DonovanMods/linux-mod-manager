package icarus

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// Compile reads exmodzPath's .EXMOD diff, applies it to the game's base data
// tables, bundles in any pre-built assets the .EXMODZ carries, and writes the
// result as a new pak at outputPakPath ready to deploy as-is.
//
// Base tables are read directly out of basePakPath — the installed game's own
// Content/Data/data.pak — so they are always week-correct by construction and
// the whole operation is offline. That pak stores 40 tables uncompressed and
// compresses the other 258 with Zlib, all of which internal/unrealpak reads
// with the standard library (#175). basePakPath is also what resolves a bare,
// hyphen-flattened CurrentFile to a real mount path.
//
// There is no ctx parameter: every step is local file I/O over a ~2 MB pak,
// with no network call and no long-running loop to cancel. The
// source.MergeCompiler interface still takes one, for implementations that
// need it (MergeCompile, this package's own N-mod entry point, is one).
//
// The compiled pak's mount point and table-entry paths (icarusContentMountPoint,
// icarusDataTablePrefix below) are Icarus-specific and deliberately live here
// rather than in internal/unrealpak, which stays game-agnostic — see
// unrealpak.Writer's WithMountPoint. They are not guessed: both were
// confirmed against two real, working prebuilt Icarus mod paks (#178; see
// docs/plans/2026-08-01-icarus-zlib-pivot.md's pak-divergence-report.md).
func Compile(basePakPath, exmodzPath, outputPakPath string) (err error) {
	exmodzData, err := os.ReadFile(exmodzPath)
	if err != nil {
		return fmt.Errorf("icarus: reading %s: %w", exmodzPath, err)
	}
	bundle, err := ParseExmodz(exmodzData)
	if err != nil {
		return fmt.Errorf("icarus: %s: %w", exmodzPath, err)
	}

	base, err := unrealpak.Open(basePakPath)
	if err != nil {
		return fmt.Errorf("icarus: opening base pak %s: %w", basePakPath, err)
	}
	defer base.Close() //nolint:errcheck

	out, err := unrealpak.Create(outputPakPath, unrealpak.WithMountPoint(icarusContentMountPoint))
	if err != nil {
		return fmt.Errorf("icarus: creating %s: %w", outputPakPath, err)
	}
	// unrealpak.Create opens the file eagerly, so any error from here on
	// leaves a partial/incomplete pak at outputPakPath unless removed — a
	// hazard, since it could be picked up and deployed. unrealpak.Writer has
	// no way to abort without finalizing (Close always serializes and writes
	// whatever was buffered), so removing the file is the only way to keep
	// the fail-loud-and-clean contract on this path; the success path
	// (err == nil here) is untouched. The writer is closed (best-effort,
	// error ignored — whatever it wrote is about to be deleted anyway)
	// before the remove so the fd never leaks and the remove itself works on
	// platforms (Windows) that refuse to delete a still-open file.
	defer func() {
		if err == nil {
			return
		}
		_ = out.Close() //nolint:errcheck
		if rmErr := os.Remove(outputPakPath); rmErr != nil && !os.IsNotExist(rmErr) {
			err = fmt.Errorf("%w (additionally, removing partial output %s failed: %v)", err, outputPakPath, rmErr)
		}
	}()

	for _, row := range bundle.Diff.Rows {
		if row.CurrentFile == endOfModSentinel {
			// A known .EXMOD ecosystem terminator row: no File_Items, no
			// corresponding data table. Not a row to resolve or patch.
			continue
		}
		if len(row.FileItems) == 0 {
			return fmt.Errorf("icarus: %s: row has no File_Items to apply (malformed .EXMOD manifest)", row.CurrentFile)
		}
		mountPath, err := resolveCurrentFile(base, row.CurrentFile)
		if err != nil {
			return err
		}
		baseData, err := base.ReadFile(mountPath)
		if err != nil {
			return fmt.Errorf("icarus: reading base data table %s: %w", mountPath, err)
		}
		patched, err := ApplyRowPatch(baseData, row)
		if err != nil {
			return err
		}
		tablePath := icarusDataTablePrefix + mountPath
		if err := out.AddFile(tablePath, patched); err != nil {
			return fmt.Errorf("icarus: writing patched %s: %w", tablePath, err)
		}
	}

	for assetPath, data := range bundle.Assets {
		safePath, err := sanitizeAssetPath(assetPath)
		if err != nil {
			return err
		}
		// No icarusDataTablePrefix here: bundled assets are content packages,
		// not JSON data-table overrides. They need only icarusContentMountPoint
		// (via the Writer's mount point) to land under Icarus/Content/ at
		// their own namespace path — confirmed against a real asset-only
		// prebuilt mod pak (TurretVariants; see pak-divergence-report.md).
		if err := out.AddFile(safePath, data); err != nil {
			return fmt.Errorf("icarus: writing bundled asset %s: %w", safePath, err)
		}
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("icarus: finalizing %s: %w", outputPakPath, err)
	}
	return nil
}

// icarusContentMountPoint is the mount point a compiled _P.pak must declare
// for Icarus's own data-table mod loader to find it. "../../../" (this
// package's own default — see unrealpak.defaultMountPoint) resolves to the
// Steam install's outer game folder; real Icarus mods redescend from there
// with a literal "Icarus/Content/" — the game's UProject-root folder name is
// itself "Icarus" (confirmed both by real prebuilt mods' own mount strings
// and by data.pak's own on-disk nesting: .../Icarus/Icarus/Content/Data/data.pak).
// Confirmed against two independent real, working prebuilt mod paks
// (FloofLevelCap, Intreeg's 4XP) — see pak-divergence-report.md.
const icarusContentMountPoint = "../../../Icarus/Content/"

// icarusDataTablePrefix is prepended to a patched base table's own
// mount-relative path (as read from data.pak, e.g. "Experience/D_ExperienceEvents.json")
// before it is written into the compiled pak. Real prebuilt mods land their
// table overrides at "Icarus/Content/data/<same-path>" — confirmed
// byte-for-byte against FloofLevelCap.pak and Intreeg's 4XP.pak. It must NOT
// be applied to bundled assets (see the asset loop below): a single pak
// can't correctly address both classes with the same prefix.
const icarusDataTablePrefix = "data/"

// endOfModSentinel is a known .EXMOD ecosystem terminator row: real-world
// manifests end their Rows array with {"CurrentFile":"EndOfMod"} and no
// File_Items key at all. It targets no data table and carries no patch, so
// Compile skips it rather than trying (and failing) to resolve it.
const endOfModSentinel = "EndOfMod"

// resolveCurrentFile finds the base-pak file a row's bare CurrentFile refers
// to. The .EXMOD schema flattens the mount-relative directory path into
// CurrentFile by replacing every "/" with "-" (e.g. the real base pak path
// "Audio/MusicConditions/D_MusicLocationConditions.json" is recorded as
// "Audio-MusicConditions-D_MusicLocationConditions.json"); reversing that
// substitution reconstructs the mount path exactly. This was verified
// against a real install and a real .EXMODZ: none of Icarus's 298 real base
// table paths contain a literal hyphen, so the reverse mapping is
// unambiguous. Fails loudly on zero or multiple matches — see this task's
// header note; guessing which one is correct is exactly the kind of silent
// fallback repo precedent #95 forbids.
func resolveCurrentFile(base *unrealpak.Reader, currentFile string) (string, error) {
	files := base.Files()
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return matchMountPath(paths, currentFile)
}

// matchMountPath resolves currentFile against paths, isolated from
// *unrealpak.Reader so the zero/ambiguous-match error paths can be tested
// directly without needing a base pak with (unreachable in valid data)
// duplicate mount entries.
func matchMountPath(paths []string, currentFile string) (string, error) {
	candidate := strings.ReplaceAll(currentFile, "-", "/")
	var matches []string
	for _, p := range paths {
		if p == candidate {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("icarus: %s: no matching file in base pak "+
			"(expected mount path %s, from CurrentFile with '-' converted to '/')", currentFile, candidate)
	default:
		return "", fmt.Errorf("icarus: %s: ambiguous, matches %v", currentFile, matches)
	}
}

// sanitizeAssetPath validates a bundled asset's mount path before it is
// written into the output pak. .EXMODZ archives are third-party zip files,
// and ParseExmodz (Task 11) carries each entry's raw zip name through
// unchanged as the Assets map key. Without this gate, a crafted entry name
// (a "../" parent traversal, an absolute path, or a Windows drive path) could
// escape the mod's own namespace once the pak is deployed or unpacked
// elsewhere — the pak equivalent of a zip-slip. Rejecting it here, before
// AddFile, keeps that malformed-archive class of input a loud compile
// failure rather than a written-then-discovered problem.
func sanitizeAssetPath(rawZipName string) (string, error) {
	normalized := strings.ReplaceAll(rawZipName, `\`, "/")
	if strings.Contains(normalized, "\x00") {
		return "", fmt.Errorf("icarus: bundled asset %q: contains a NUL byte", rawZipName)
	}
	if strings.HasPrefix(normalized, "/") || isWindowsDriveAbsolute(normalized) {
		return "", fmt.Errorf("icarus: bundled asset %q: absolute paths are not allowed", rawZipName)
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("icarus: bundled asset %q: escapes the mod's own path", rawZipName)
	}
	return cleaned, nil
}

// isWindowsDriveAbsolute reports whether p starts with a Windows drive letter
// (e.g. "C:/evil"). Checked on the slash-normalized form, since a zip entry
// written by a Windows tool may carry "C:\evil" — backslashes normalize to
// forward slashes before this check runs.
func isWindowsDriveAbsolute(p string) bool {
	return len(p) >= 2 && p[1] == ':' &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

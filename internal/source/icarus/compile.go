package icarus

import (
	"context"
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
// The base tables come from the community per-week dump (Task 12a), not from
// basePakPath: 258 of the 298 tables in a real data.pak are Oodle-compressed
// and cannot be read with the stdlib. basePakPath is still opened, for two
// things it alone can answer — which tables the installed game actually has
// (so a bare, hyphen-flattened CurrentFile resolves to a real mount path),
// and whether the dump
// is for the installed week (DumpForBuild byte-checks it against the tables
// the pak stores uncompressed). A dump that does not match fails the whole
// compile; see Task 12a.
//
// localDumpDir is the game's optional data_dump_path: when set, base tables
// are read from that directory instead of being fetched. It is validated
// identically, so a stale local directory fails just as loudly.
func Compile(ctx context.Context, dumps *DumpStore, basePakPath, localDumpDir, exmodzPath, outputPakPath string) (err error) {
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

	// Loaded and validated before anything is written, so a week mismatch or
	// an offline machine fails before a half-built pak exists on disk.
	dump, err := dumps.DumpForBuild(ctx, basePakPath, localDumpDir)
	if err != nil {
		return err
	}

	out, err := unrealpak.Create(outputPakPath)
	if err != nil {
		return fmt.Errorf("icarus: creating %s: %w", outputPakPath, err)
	}
	// unrealpak.Create opens the file eagerly, so any error from here on
	// leaves a partial/incomplete pak at outputPakPath unless removed — a
	// hazard, since it could be picked up and deployed. unrealpak.Writer has
	// no way to abort without finalizing (Close always serializes and writes
	// whatever was buffered), so removing the file is the only way to keep
	// the fail-loud-and-clean contract on this path; the success path
	// (err == nil here) is untouched.
	defer func() {
		if err == nil {
			return
		}
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
		baseData, ok := dump.Table(mountPath)
		if !ok {
			return fmt.Errorf("icarus: base data table %s is present in the installed game "+
				"but missing from the base-table dump", mountPath)
		}
		patched, err := ApplyRowPatch(baseData, row)
		if err != nil {
			return err
		}
		if err := out.AddFile(mountPath, patched); err != nil {
			return fmt.Errorf("icarus: writing patched %s: %w", mountPath, err)
		}
	}

	for assetPath, data := range bundle.Assets {
		safePath, err := sanitizeAssetPath(assetPath)
		if err != nil {
			return err
		}
		if err := out.AddFile(safePath, data); err != nil {
			return fmt.Errorf("icarus: writing bundled asset %s: %w", safePath, err)
		}
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("icarus: finalizing %s: %w", outputPakPath, err)
	}
	return nil
}

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

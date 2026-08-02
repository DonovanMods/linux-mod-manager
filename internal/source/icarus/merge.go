package icarus

import (
	"context"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// MergeSource is a type alias (not a distinct type) for source.MergeSource
// (Step 3 above). internal/core must NOT import this icarus package
// directly (established #136/#196 precedent - see
// service_icarus_compile_test.go's fakeCompilerSource doc comment), so it
// can only ever construct/consume source.MergeSource values - aliasing it
// here, rather than defining a second, structurally-similar type, is what
// lets *Icarus's MergeCompile method (Step 6) satisfy source.MergeCompiler
// at all: Go interface satisfaction requires identical types, and a type
// alias IS the same type, not a look-alike.
type MergeSource = source.MergeSource

// ValidateSource parses exmodzPath without compiling anything - the
// ingest-time check (#197 design: "install still parses/validates the
// .exmodz early"). A malformed archive fails loud immediately, at
// download/import time, rather than at the next merge (which may not run
// until a later mutation).
func ValidateSource(exmodzPath string) error {
	data, err := os.ReadFile(exmodzPath)
	if err != nil {
		return fmt.Errorf("icarus: reading %s: %w", exmodzPath, err)
	}
	if _, err := ParseExmodz(data); err != nil {
		return fmt.Errorf("icarus: validating %s: %w", exmodzPath, err)
	}
	return nil
}

// MergeCompile applies every source's .EXMOD row upserts, IN ORDER, against
// the same evolving base tables - a merge is just Compile with N diffs
// instead of 1. Table conflicts compose at the FIELD level for free:
// ApplyRowPatch always shallow-merges an item's fields into whatever the
// target row currently holds, so feeding mod A's patched bytes back in as
// the "base" for mod B's row (instead of re-reading the pristine base table
// each time) is the entire merge algorithm - two mods patching DIFFERENT
// fields of the same row, or entirely different rows of the same table,
// both survive; only a genuine same-row-same-field write is last-wins (an
// ordinary, expected upsert outcome, not something to warn about). Bundled
// ASSET files cannot compose this way - a same-path asset collision is
// necessarily last-wins, so it is reported as a warning instead.
//
// ctx is accepted only to satisfy source.MergeCompiler and is never read -
// every step here is local file I/O over small files (mirrors Compile's own
// doc comment, internal/source/icarus/compile.go:23-25).
//
// A non-nil error always means outputPakPath does not exist (or does not
// contain a fully-written pak) - see the removal defer below, mirroring
// Compile's own fail-clean contract.
func MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) (warnings []string, err error) {
	base, err := unrealpak.Open(basePakPath)
	if err != nil {
		return nil, fmt.Errorf("icarus: opening base pak %s: %w", basePakPath, err)
	}
	defer base.Close() //nolint:errcheck

	tableState := make(map[string][]byte) // mountPath -> current (possibly already patched) JSON bytes
	assets := make(map[string][]byte)     // final asset path -> data (last source wins)
	assetOwner := make(map[string]string) // asset path -> ModRef that last set it

	for _, src := range sources {
		exmodzData, rerr := os.ReadFile(src.ExmodzPath)
		if rerr != nil {
			return warnings, fmt.Errorf("icarus: reading %s: %w", src.ExmodzPath, rerr)
		}
		bundle, perr := ParseExmodz(exmodzData)
		if perr != nil {
			return warnings, fmt.Errorf("icarus: %s: %w", src.ExmodzPath, perr)
		}

		for _, row := range bundle.Diff.Rows {
			if row.CurrentFile == endOfModSentinel {
				continue
			}
			if len(row.FileItems) == 0 {
				return warnings, fmt.Errorf("icarus: %s: row has no File_Items to apply (malformed .EXMOD manifest)", row.CurrentFile)
			}
			mountPath, merr := resolveCurrentFile(base, row.CurrentFile)
			if merr != nil {
				return warnings, merr
			}
			current, seen := tableState[mountPath]
			if !seen {
				current, merr = base.ReadFile(mountPath)
				if merr != nil {
					return warnings, fmt.Errorf("icarus: reading base data table %s: %w", mountPath, merr)
				}
			}
			patched, perr2 := ApplyRowPatch(current, row)
			if perr2 != nil {
				return warnings, perr2
			}
			tableState[mountPath] = patched
		}

		for assetPath, data := range bundle.Assets {
			safePath, serr := sanitizeAssetPath(assetPath)
			if serr != nil {
				return warnings, serr
			}
			if owner, exists := assetOwner[safePath]; exists && owner != src.ModRef {
				warnings = append(warnings, fmt.Sprintf(
					"asset %q is bundled by both %s and %s - %s wins (last-applied, per profile load order)",
					safePath, owner, src.ModRef, src.ModRef))
			}
			assets[safePath] = data
			assetOwner[safePath] = src.ModRef
		}
	}

	out, cerr := unrealpak.Create(outputPakPath, unrealpak.WithMountPoint(icarusContentMountPoint))
	if cerr != nil {
		return warnings, fmt.Errorf("icarus: creating %s: %w", outputPakPath, cerr)
	}
	defer func() {
		if err == nil {
			return
		}
		_ = out.Close() //nolint:errcheck
		if rmErr := os.Remove(outputPakPath); rmErr != nil && !os.IsNotExist(rmErr) {
			err = fmt.Errorf("%w (additionally, removing partial output %s failed: %v)", err, outputPakPath, rmErr)
		}
	}()

	for mountPath, data := range tableState {
		tablePath := icarusDataTablePrefix + mountPath
		if err = out.AddFile(tablePath, data); err != nil {
			return warnings, fmt.Errorf("icarus: writing merged %s: %w", tablePath, err)
		}
	}
	for assetPath, data := range assets {
		if err = out.AddFile(assetPath, data); err != nil {
			return warnings, fmt.Errorf("icarus: writing bundled asset %s: %w", assetPath, err)
		}
	}

	if err = out.Close(); err != nil {
		return warnings, fmt.Errorf("icarus: finalizing %s: %w", outputPakPath, err)
	}
	return warnings, nil
}

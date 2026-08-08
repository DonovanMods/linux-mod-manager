package icarus

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/go-unrealpak"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
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

// ValidateSource parses sourceFilePath without compiling anything - the
// ingest-time check. .exmodz archives fully parse (#197); .pak files (#221)
// open + enumerate only - full conversion is checked at merge time BY
// DESIGN (the result depends on the current base pak, which changes weekly).
func ValidateSource(sourceFilePath string) error {
	if strings.HasSuffix(strings.ToLower(sourceFilePath), ".pak") {
		r, err := unrealpak.Open(sourceFilePath)
		if err != nil {
			return fmt.Errorf("icarus: validating %s: %w", sourceFilePath, err)
		}
		defer r.Close() //nolint:errcheck
		if len(r.Files()) == 0 {
			return fmt.Errorf("icarus: validating %s: pak contains no entries", sourceFilePath)
		}
		return nil
	}
	data, err := os.ReadFile(sourceFilePath)
	if err != nil {
		return fmt.Errorf("icarus: reading %s: %w", sourceFilePath, err)
	}
	if _, err := ParseExmodz(data); err != nil {
		return fmt.Errorf("icarus: validating %s: %w", sourceFilePath, err)
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
func MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) (warnings []string, failed []source.MergeFailure, err error) {
	base, err := unrealpak.Open(basePakPath)
	if err != nil {
		return nil, nil, fmt.Errorf("icarus: opening base pak %s: %w", basePakPath, err)
	}
	defer base.Close() //nolint:errcheck

	// Build the fold index once per merge; used by convertPakToBundle for
	// case-insensitive base table path resolution in Tier 2 conversions.
	baseFold := buildBaseFoldIndex(base)

	tableState := make(map[string][]byte)     // mountPath -> current (possibly already patched) JSON bytes
	assets := make(map[string][]byte)         // final asset path -> data (last source wins)
	assetOwner := make(map[string]mergeOwner) // asset path -> the mod that last set it

	for _, src := range sources {
		// Warnings are user-facing: prefer the display name; MergeFailure
		// keeps ModRef as the machine identity core keys on.
		label := src.ModName
		if label == "" {
			label = src.ModRef
		}
		if src.Kind == source.MergeSourcePak {
			bundle, convWarnings, cerr := convertPakToBundle(src.SourcePath, base, baseFold)
			if cerr != nil {
				failed = append(failed, source.MergeFailure{ModRef: src.ModRef, Reason: cerr.Error()})
				warnings = append(warnings, fmt.Sprintf("mod %s: pak conversion failed: %v - deploying raw", label, cerr))
				continue
			}
			for _, w := range convWarnings {
				warnings = append(warnings, fmt.Sprintf("mod %s: %s", label, w))
			}
			// Apply on scratch copies: a Tier 1 row can still fail
			// resolveCurrentFile against the CURRENT base (the manifest may
			// reference a since-removed table), and a half-applied mod must
			// not pollute the merge. All three maps are copied so a
			// mid-application failure cannot leave stray assets behind
			// either - strictly transactional per mod.
			scratchTables := make(map[string][]byte, len(tableState))
			for k, v := range tableState {
				scratchTables[k] = v
			}
			scratchAssets := make(map[string][]byte, len(assets))
			for k, v := range assets {
				scratchAssets[k] = v
			}
			scratchOwner := make(map[string]mergeOwner, len(assetOwner))
			for k, v := range assetOwner {
				scratchOwner[k] = v
			}
			applyWarnings, aerr := applyBundle(base, scratchTables, scratchAssets, scratchOwner, bundle, src.ModRef, label)
			if aerr != nil {
				failed = append(failed, source.MergeFailure{ModRef: src.ModRef, Reason: aerr.Error()})
				warnings = append(warnings, fmt.Sprintf("mod %s: pak conversion failed: %v - deploying raw", label, aerr))
				continue
			}
			warnings = append(warnings, applyWarnings...)
			tableState = scratchTables
			assets = scratchAssets
			assetOwner = scratchOwner
			continue
		}

		// Exmodz source: policy unchanged - any error is fatal (#197).
		exmodzData, rerr := os.ReadFile(src.SourcePath)
		if rerr != nil {
			return warnings, failed, fmt.Errorf("icarus: reading %s: %w", src.SourcePath, rerr)
		}
		bundle, perr := ParseExmodz(exmodzData)
		if perr != nil {
			return warnings, failed, fmt.Errorf("icarus: %s: %w", src.SourcePath, perr)
		}
		applyWarnings, aerr := applyBundle(base, tableState, assets, assetOwner, bundle, src.ModRef, label)
		if aerr != nil {
			return warnings, failed, aerr
		}
		warnings = append(warnings, applyWarnings...)
	}

	out, cerr := unrealpak.Create(outputPakPath, unrealpak.WithMountPoint(icarusContentMountPoint))
	if cerr != nil {
		return warnings, failed, fmt.Errorf("icarus: creating %s: %w", outputPakPath, cerr)
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
			return warnings, failed, fmt.Errorf("icarus: writing merged %s: %w", tablePath, err)
		}
	}
	for assetPath, data := range assets {
		if err = out.AddFile(assetPath, data); err != nil {
			return warnings, failed, fmt.Errorf("icarus: writing bundled asset %s: %w", assetPath, err)
		}
	}

	if err = out.Close(); err != nil {
		return warnings, failed, fmt.Errorf("icarus: finalizing %s: %w", outputPakPath, err)
	}
	return warnings, failed, nil
}

// mergeOwner tracks which mod last set an asset path: ref is the stable
// identity collision detection compares, label the display name warnings
// render.
type mergeOwner struct {
	ref   string
	label string
}

// applyBundle applies one bundle's row upserts and assets to the merge
// state. Asset collisions across mods warn (last-applied wins); any other
// error is returned to the caller, which decides fatality by source kind.
// Collisions are detected by modRef but rendered with modLabel (the display
// name, or ModRef when unnamed); identical labels get refs appended so the
// warning still distinguishes the two parties.
func applyBundle(base *unrealpak.Reader, tableState map[string][]byte, assets map[string][]byte, assetOwner map[string]mergeOwner, bundle *ExmodzBundle, modRef, modLabel string) (warnings []string, err error) {
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
			var rerr error
			current, rerr = base.ReadFile(mountPath)
			if rerr != nil {
				return warnings, fmt.Errorf("icarus: reading base data table %s: %w", mountPath, rerr)
			}
		}
		patched, perr := ApplyRowPatch(current, row)
		if perr != nil {
			return warnings, perr
		}
		tableState[mountPath] = patched
	}

	for assetPath, data := range bundle.Assets {
		safePath, serr := sanitizeAssetPath(assetPath)
		if serr != nil {
			return warnings, serr
		}
		if owner, exists := assetOwner[safePath]; exists && owner.ref != modRef {
			ownerDisp, curDisp := owner.label, modLabel
			if ownerDisp == curDisp {
				ownerDisp = fmt.Sprintf("%s (%s)", owner.label, owner.ref)
				curDisp = fmt.Sprintf("%s (%s)", modLabel, modRef)
			}
			warnings = append(warnings, fmt.Sprintf(
				"asset %q is bundled by both %s and %s - %s wins (last-applied, per profile load order)",
				safePath, ownerDisp, curDisp, curDisp))
		}
		assets[safePath] = data
		assetOwner[safePath] = mergeOwner{ref: modRef, label: modLabel}
	}
	return warnings, nil
}

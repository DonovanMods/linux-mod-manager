package icarus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/go-unrealpak"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// This file implements source.MergeCompiler's format-vocabulary methods
// (#256): everything lmm needs to know about Unreal paks and Icarus's disk
// layout to orchestrate merges, moved here from internal/core so that core
// asks the source instead of knowing the format itself. A second
// DeployCompile game supplies its own answers to these questions in its own
// source package; core never changes.

// Merge-source kinds (#221; moved here from internal/source in #256 - they
// are Icarus vocabulary, and core now treats kinds as opaque strings it
// obtains from ClassifyMergeSource). An empty Kind means MergeSourceExmodz:
// every pre-#221 constructor built exmodz-only sources and never set a kind,
// and stored merge fingerprints from that era carry no Kind field.
const (
	MergeSourceExmodz = "exmodz"
	MergeSourcePak    = "pak"
)

// mergedPakFileName names the single merged output pak. It sorts LAST among
// files UE mounts from a profile's mods directory: paks mount in
// filename-sort order and a later mount wins same-path conflicts (this
// package's own icarusContentMountPoint doc comment, and #197's issue body,
// both note this) - "zzz" is a long-standing UE-modding convention for
// "load last, highest priority", so the merged pak's authoritative combined
// table state can never be silently shadowed by a plain prebuilt .pak mod
// that happens to also carry a table override. "LMM" makes the file
// greppable as lmm-owned; "_P" is UE's override-pak suffix convention,
// carried by the real prebuilt Icarus mod paks this package was built
// against (#178).
const mergedPakFileName = "zzz_LMM_Merged_P.pak"

// ResolveBaseArtifact locates the currently-installed game's base pak - the
// artifact every merge applies against. One known pak filename pattern: the
// relative path below is Task 1's empirically-confirmed finding
// (docs/plans/icarus-pak-format-findings.md), recorded before this function
// was written, not an assumption made here: the JSON data tables live in
// Content/Data/data.pak, NOT in the Content/Paks pakchunks, which carry
// only cooked .uasset/.uexp assets and no JSON at all.
//
// This pak is also the direct source of base table *content* (#175):
// MergeCompile reads each patched table straight out of it, so a compile is
// always week-correct by construction (there's no separate dump to go stale
// relative to the install) and works entirely offline.
func (s *Icarus) ResolveBaseArtifact(game *domain.Game) (string, error) {
	candidate := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("locating base pak for %q: %w", game.ID, err)
	}
	return candidate, nil
}

// FingerprintBase opens the base pak and returns its footer IndexHash
// (#196) as an opaque fingerprint - cheap (footer + primary-index region
// only; unrealpak.Open never reads a pak's actual file payloads), matching
// the base pak MergeCompile itself already opens to read patched tables
// from, so this adds no new I/O pattern to the compile path.
func (s *Icarus) FingerprintBase(baseArtifactPath string) (string, error) {
	r, err := unrealpak.Open(baseArtifactPath)
	if err != nil {
		return "", fmt.Errorf("reading base pak for compile fingerprint: %w", err)
	}
	defer r.Close() //nolint:errcheck
	return r.IndexHash(), nil
}

// IsNativeMergeSource reports whether fileName names this source's NATIVE
// merge-source format - a ".exmodz" archive (case-insensitive), the diff
// format MergeCompile consumes directly without conversion. Pure format
// test, the exact mirror of IsConvertibleArtifact (pre-#256 core's
// isExmodzFile); core owns the DeployCompile policy gates and the
// "native archive from a non-compile-capable source" hard error.
func (s *Icarus) IsNativeMergeSource(fileName string) bool {
	return strings.HasSuffix(strings.ToLower(fileName), ".exmodz")
}

// IsConvertibleArtifact reports whether fileName names a prebuilt .pak this
// source can convert into a merge source (#221). Pure format test
// (case-insensitive ".pak" suffix, mirroring pre-#256 core's
// isConvertEligiblePakFile) - core owns the DeployCompile/ConvertPaks
// policy gates that decide whether a convertible file actually enters the
// merge-convert pipeline.
func (s *Icarus) IsConvertibleArtifact(fileName string) bool {
	return strings.HasSuffix(strings.ToLower(fileName), ".pak")
}

// ClassifyMergeSource maps a retained-source identity to its merge-source
// kind (#221) plus whether that kind is convertible (a raw prebuilt pak, as
// opposed to a native .exmodz diff). id is a retained-source fileID -
// download-path icarus fileIDs are literally "pak"/"exmodz", import-path
// fileIDs are the archive's own filename - or a Kind string previously
// recorded on a merge fingerprint (kind strings classify as themselves, and
// a legacy pre-#221 entry's empty Kind classifies as exmodz, the only kind
// that existed then). Unknown ids default to exmodz for the same reason.
func (s *Icarus) ClassifyMergeSource(id string) (kind string, convertible bool) {
	lower := strings.ToLower(id)
	if lower == "pak" || strings.HasSuffix(lower, ".pak") {
		return MergeSourcePak, true
	}
	return MergeSourceExmodz, false
}

// MergedArtifactName names the single merged output artifact deployed into
// the game's mods directory - see mergedPakFileName for why this exact name
// is a deploy contract.
func (s *Icarus) MergedArtifactName() string { return mergedPakFileName }

// MergedArtifactLabel is the user-facing display name for the merged
// artifact's synthetic mod row (verify/update output).
func (s *Icarus) MergedArtifactLabel() string { return "Icarus Merged Pak" }

// RestoredArtifactName names the deployable raw-fallback copy synthesized
// when core heals a prune-damaged cache entry whose original artifact name
// is unrecoverable (#250: download-path fileIDs never carried it - the
// name came from the download URL's basename at ingest, and the convert
// flip erased the manifest that recorded it). A deterministic mod-scoped
// name in the "_P.pak" override convention (see mergedPakFileName's doc)
// keeps healed installs stable: the same mod always restores to the same
// name, which is on-disk state existing installs already depend on.
func (s *Icarus) RestoredArtifactName(modID string) string { return modID + "_P.pak" }

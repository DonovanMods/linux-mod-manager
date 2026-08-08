package core_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/go-unrealpak"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// fakeMergeFormat supplies source.MergeCompiler's format-vocabulary methods
// (#256) for this package's compile-source fakes, mirroring the icarus
// conventions the fixtures already encode (writeFakeBasePak's
// Icarus/Content/Data/data.pak path, "pak"/"exmodz" fileIDs, the
// zzz_LMM_Merged_P.pak artifact name) without pulling the icarus package
// into internal/core's tests (fakeCompilerSource's own precedent). Embed it
// in any fake that needs to satisfy source.MergeCompiler.
type fakeMergeFormat struct{}

func (fakeMergeFormat) ResolveBaseArtifact(game *domain.Game) (string, error) {
	candidate := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("locating base pak for %q: %w", game.ID, err)
	}
	return candidate, nil
}

func (fakeMergeFormat) FingerprintBase(basePakPath string) (string, error) {
	r, err := unrealpak.Open(basePakPath)
	if err != nil {
		return "", fmt.Errorf("reading base pak for compile fingerprint: %w", err)
	}
	defer r.Close() //nolint:errcheck
	return r.IndexHash(), nil
}

func (fakeMergeFormat) IsNativeMergeSource(fileName string) bool {
	return strings.HasSuffix(strings.ToLower(fileName), ".exmodz")
}

func (fakeMergeFormat) IsConvertibleArtifact(fileName string) bool {
	return strings.HasSuffix(strings.ToLower(fileName), ".pak")
}

func (fakeMergeFormat) ClassifyMergeSource(id string) (string, bool) {
	lower := strings.ToLower(id)
	if lower == "pak" || strings.HasSuffix(lower, ".pak") {
		return "pak", true
	}
	return "exmodz", false
}

func (fakeMergeFormat) MergedArtifactName() string  { return "zzz_LMM_Merged_P.pak" }
func (fakeMergeFormat) MergedArtifactLabel() string { return "Icarus Merged Pak" }

func (fakeMergeFormat) RestoredArtifactName(modID string) string { return modID + "_P.pak" }

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/go-unrealpak"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// fakeMergeFormat supplies adapter.MergeCompiler's format-vocabulary methods
// (#256) for this package's compile-source fakes, mirroring the icarus
// conventions the fixtures already encode (writeFakeBasePak's
// Icarus/Content/Data/data.pak path, "pak"/"exmodz" fileIDs, the
// zzz_LMM_Merged_P.pak artifact name). Embed it in any fake that needs to
// satisfy adapter.MergeCompiler. Duplicated per test package by design -
// mirrors internal/core's identical helper.
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

// testCompileAdapter presents one of this package's MergeCompiler fakes as a
// game ADAPTER (#412). Before U2 core walked a game's SOURCES looking for
// one that compiled, so a fake source implementing MergeCompiler was the
// whole fixture; now compilation is a property of the game, and the fake has
// to be registered where core actually looks.
//
// It is a wrapper rather than a rewrite of each fake because the fakes are
// still ModSources too - the flows download from them - and the one thing
// that changed is WHO core asks about the format.
type testCompileAdapter struct {
	adapter.MergeCompiler
}

// testCompileAdapterID is the id every fake compile adapter registers under.
// It is "icarus" on purpose: Service.AdapterName derives exactly that name
// for a `deploy_mode: compile` game carrying no explicit `adapter:` key, so
// a fixture written before the seam existed keeps working untouched - the
// same migration a real user's games.yaml takes.
const testCompileAdapterID = "icarus"

// ID implements adapter.GameAdapter.
func (testCompileAdapter) ID() string { return testCompileAdapterID }

// Label implements adapter.GameAdapter.
func (testCompileAdapter) Label() string { return "Test compile adapter" }

// NormalizeArchive is the identity, like the real Icarus adapter's: a
// compile game's archives land in the cache entry exactly as they arrive.
func (testCompileAdapter) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}

// registerCompileSource registers src as a mod source AND, when it carries
// the compile capability, as the game adapter that supplies it. One call
// replaces the two-step fixture U2 (#412) made necessary: a compile fake
// used to be reached by walking the game's sources, and is now reached
// through the game's adapter.
func registerCompileSource(svc *core.Service, src source.ModSource) {
	svc.RegisterSource(src)
	if mc, ok := src.(adapter.MergeCompiler); ok {
		svc.RegisterAdapter(testCompileAdapter{MergeCompiler: mc})
	}
}

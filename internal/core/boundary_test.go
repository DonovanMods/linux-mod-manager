package core_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImportBoundary_NeverImportsConcreteSources guards Rulings 8 (v2 Phase 2
// Task 21) and 12 (v2 Phase 3 Task 1, #300 -
// docs/plans/2026-08-27-v2-core-refactor-design.md decisions log): core must
// not import any concrete source package - it consumes sources only through
// internal/source's ModSource interface and its capability interfaces
// (LocalFileServer, MergeCompiler, ...), never a concrete implementation
// directly. Steam scanning is exposed by internal/app (app.DetectGames), and
// the file:// local-ingest gate asserts source.LocalFileServer instead of
// *custom.Directory. go list -deps walks the full transitive dependency
// graph, not just this package's direct imports, so a future helper reaching
// for one of these indirectly still trips this.
//
// internal/source itself and internal/source/httpclient are exempt: they
// hold the interfaces/shared HTTP plumbing core is allowed to depend on, not
// a concrete source.
func TestImportBoundary_NeverImportsConcreteSources(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err, "go list -deps ./internal/core")

	deps := make(map[string]bool)
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		deps[dep] = true
	}

	const base = "github.com/DonovanMods/linux-mod-manager/internal/source/"
	forbidden := []string{"nexusmods", "curseforge", "custom", "custom/metadata", "steam", "icarus"}
	for _, pkg := range forbidden {
		require.False(t, deps[base+pkg],
			"internal/core must not import "+base+pkg+" - core consumes concrete sources only through internal/source's interfaces")
	}
}

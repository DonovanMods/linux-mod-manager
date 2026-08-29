package core_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImportBoundary_NeverImportsSteam guards Ruling 8 (v2 Phase 2 Task 21,
// docs/plans/2026-08-27-v2-core-refactor-design.md decisions log): Steam
// scanning is exposed by internal/app (app.DetectGames), not internal/core -
// core consumes domain.DetectedGame instead of importing the concrete
// source, unlike cmd/lmm's boundaryAllowList ratchet (which governs
// frontend imports, not core's). go list -deps walks the full transitive
// dependency graph, not just this package's direct imports, so a future
// helper reaching for steam indirectly still trips this.
func TestImportBoundary_NeverImportsSteam(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err, "go list -deps ./internal/core")

	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, dep := range deps {
		require.NotEqual(t, "github.com/DonovanMods/linux-mod-manager/internal/source/steam", dep,
			"internal/core must not import internal/source/steam - Steam scanning belongs behind internal/app (Ruling 8)")
	}
}

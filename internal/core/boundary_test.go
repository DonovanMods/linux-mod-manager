package core_test

import (
	"io/fs"
	"os/exec"
	"path/filepath"
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
// a concrete source. The forbidden list itself is discovered by walking
// internal/source (phase-end review Minor 3, Unit N M1 unfixed) rather than
// hardcoded, so a future internal/source/gamebanana is picked up
// automatically instead of silently unguarded by the very ratchet that
// exists to catch it.
func TestImportBoundary_NeverImportsConcreteSources(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err, "go list -deps ./internal/core")

	deps := make(map[string]bool)
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		deps[dep] = true
	}

	const base = "github.com/DonovanMods/linux-mod-manager/internal/source/"
	forbidden := concreteSourcePackages(t)
	for _, pkg := range forbidden {
		require.False(t, deps[base+pkg],
			"internal/core must not import "+base+pkg+" - core consumes concrete sources only through internal/source's interfaces")
	}
}

// concreteSourcePackages walks ../source and returns the import-path suffix
// of every concrete source package found there (e.g. "nexusmods",
// "custom/metadata"), excluding internal/source itself and
// internal/source/httpclient - the interfaces and shared HTTP plumbing core
// is allowed to depend on. A directory only counts as a package when it
// holds at least one .go file (internal/source/steam/data is embedded YAML,
// not a package).
func concreteSourcePackages(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "source")

	var pkgs []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." || rel == "httpclient" || strings.HasPrefix(rel, "httpclient"+string(filepath.Separator)) {
			return nil
		}
		matches, err := filepath.Glob(filepath.Join(path, "*.go"))
		if err != nil {
			return err
		}
		if len(matches) > 0 {
			pkgs = append(pkgs, filepath.ToSlash(rel))
		}
		return nil
	}))
	require.NotEmpty(t, pkgs, "expected to find concrete source packages under ../source")
	return pkgs
}

package testutil

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestExportedIdentifiersHaveDocComments is the doc-comment ratchet
// (v2 Phase 3 Task 20, #305): every exported identifier declared in each
// listed package's non-test files must carry a leading doc comment, so
// `go doc` (and a future pkg.go.dev listing) never has a blank entry. One
// table here replaces a copy of this test in each package; a package
// joins the ratchet by gaining a row.
func TestExportedIdentifiersHaveDocComments(t *testing.T) {
	packages := []string{
		"internal/core",
		"internal/domain",
		"internal/app",
		"internal/serve",
		"internal/adapter",
		"internal/adapter/bepinex",
		"internal/adapter/d2r",
		"internal/adapter/icarus",
		"internal/fomod",
		"internal/wineprefix",
	}
	for _, pkg := range packages {
		t.Run(pkg, func(t *testing.T) {
			// This package's directory is internal/testutil.
			dir := filepath.Join("..", "..", pkg)
			sources, err := filepath.Glob(filepath.Join(dir, "*.go"))
			if err != nil {
				t.Fatal(err)
			}
			if len(sources) == 0 {
				t.Fatalf("%s has no Go files: a moved package must move its row too", pkg)
			}

			offenders, err := UndocumentedExports(dir)
			if err != nil {
				t.Fatalf("parsing %s: %v", pkg, err)
			}
			if len(offenders) > 0 {
				t.Fatalf("%d exported identifier(s) missing a doc comment:\n%s", len(offenders), strings.Join(offenders, "\n"))
			}
		})
	}
}

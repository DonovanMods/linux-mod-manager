package serve_test

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modulePrefix is the import-path prefix of every package in this module.
const modulePrefix = "github.com/DonovanMods/linux-mod-manager/v2/"

// allowedImports are the intra-module packages internal/serve may depend on.
// Anything else means logic that belongs closer to core (or a layering
// violation) has leaked into the HTTP frontend - mirrors cmd/lmm's own
// allow-list ratchet (cmd/lmm/boundary_test.go), extended to this package
// per docs/plans/2026-08-30-serve-design.md §Architecture: "internal/serve
// imports only app/core/domain".
var allowedImports = []string{
	"internal/app",
	"internal/core",
	"internal/domain",
}

// checkBoundary returns one message per intra-module import that is not in
// allowed. Non-module imports are ignored.
func checkBoundary(imports []string, allowed []string) []string {
	var problems []string
	for _, imp := range imports {
		rel, ok := strings.CutPrefix(imp, modulePrefix)
		if !ok {
			continue
		}
		if slices.Contains(allowed, rel) {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"internal/serve imports %s, which is outside its allowed dependency set", rel))
	}
	slices.Sort(problems)
	return problems
}

func TestCheckBoundary(t *testing.T) {
	allowed := []string{"internal/core", "internal/domain"}
	tests := []struct {
		name    string
		imports []string
		want    []string // substrings, one per expected problem, in sorted order
	}{
		{
			name:    "allowed and third-party imports pass",
			imports: []string{modulePrefix + "internal/core", modulePrefix + "internal/domain", "context", "net/http"},
		},
		{
			// The seeded violation: a source package is never allowed inside
			// internal/serve (it must go through internal/core's ModSource
			// abstraction), so this reproduces the exact shape a future
			// accidental import would take.
			name:    "a disallowed intra-module import fails",
			imports: []string{modulePrefix + "internal/core", modulePrefix + "internal/source/nexusmods"},
			want:    []string{"imports internal/source/nexusmods"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkBoundary(tt.imports, allowed)
			require.Len(t, got, len(tt.want), "problems: %v", got)
			for i, sub := range tt.want {
				assert.Contains(t, got[i], sub)
			}
		})
	}
}

// goBinary locates the go tool for the live check. The test binary was built
// by a go toolchain, and `go test` puts that toolchain's bin dir on PATH, so
// a missing tool means an unusual invocation worth failing loudly on.
func goBinary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool not found on PATH: %v", err)
	}
	return p
}

// TestImportBoundary is the ratchet: internal/serve's non-test imports must
// stay within allowedImports.
func TestImportBoundary(t *testing.T) {
	out, err := exec.Command(goBinary(t), "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	require.NoError(t, err, "go list ./internal/serve")
	imports := strings.Split(strings.TrimSpace(string(out)), "\n")

	problems := checkBoundary(imports, allowedImports)
	if len(problems) > 0 {
		t.Fatalf("import boundary violated:\n  %s", strings.Join(problems, "\n  "))
	}
}

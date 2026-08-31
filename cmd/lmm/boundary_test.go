package main

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

// allowedImports are the intra-module packages a frontend may depend on.
// Anything else means logic that belongs in internal/core has leaked into a
// command. See docs/plans/2026-08-27-v2-core-refactor-design.md §1. This is
// the hard rule (v2 Phase 3 Task 20, #305): there is no allow-list escape
// hatch, so a new dependency either belongs on this list (with a
// design-doc reference) or the logic it needs moves into internal/core.
var allowedImports = []string{
	"internal/app",
	"internal/core",
	"internal/domain",
	"internal/source", // the interface package only; its subpackages are not allowed
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
			"cmd/lmm imports %s, which is not a frontend-facing package: move the logic into internal/core", rel))
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
			imports: []string{modulePrefix + "internal/core", modulePrefix + "internal/domain", "fmt", "github.com/spf13/cobra"},
		},
		{
			name:    "a disallowed intra-module import fails",
			imports: []string{modulePrefix + "internal/core", modulePrefix + "internal/storage/db"},
			want:    []string{"imports internal/storage/db"},
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
// by a go toolchain, and `go test` puts that toolchain's bin dir on PATH, so a
// missing tool means an unusual invocation worth failing loudly on.
func goBinary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool not found on PATH: %v", err)
	}
	return p
}

// TestImportBoundary is the ratchet: cmd/lmm's non-test imports must stay
// within allowedImports.
func TestImportBoundary(t *testing.T) {
	out, err := exec.Command(goBinary(t), "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	require.NoError(t, err, "go list ./cmd/lmm")
	imports := strings.Split(strings.TrimSpace(string(out)), "\n")

	problems := checkBoundary(imports, allowedImports)
	if len(problems) > 0 {
		t.Fatalf("import boundary violated:\n  %s", strings.Join(problems, "\n  "))
	}
}

package adapter_test

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

// adapterPackages are the packages this ratchet checks: the seam itself and
// every concrete adapter under it. A new adapter package added without a
// line here is caught by TestEveryAdapterPackageIsChecked below.
var adapterPackages = []string{
	"internal/adapter",
}

// allowedImports are the intra-module packages an adapter package may
// depend on (docs/plans/2026-09-10-game-adapter-design.md §4).
//
// internal/domain is the vocabulary; internal/adapter is the seam itself
// (a concrete adapter subpackage imports it; the identity adapter IS it).
// internal/source is on the list ONLY for the merge primitives U1 aliases
// rather than moves (adapter.MergeCompiler/MergeSource/MergeFailure are
// still source's declarations, so that internal/source/icarus keeps
// satisfying the interface untouched until U2 moves it); a concrete adapter
// must not reach for a source of its own - a source is where bytes come
// from, an adapter is what a game does with them.
//
// internal/core is the violation this whole design exists to prevent, and
// TestAdapterPackagesNeverImportCore asserts its absence by name so the
// failure message says WHY rather than just "outside the allowed set".
var allowedImports = []string{
	"internal/adapter",
	"internal/domain",
	"internal/source",
}

// checkBoundary returns one message per intra-module import of pkg that is
// not in allowed. Non-module imports are ignored.
func checkBoundary(pkg string, imports []string, allowed []string) []string {
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
			"%s imports %s, which is outside its allowed dependency set", pkg, rel))
	}
	slices.Sort(problems)
	return problems
}

func TestCheckBoundary(t *testing.T) {
	allowed := []string{"internal/adapter", "internal/domain"}
	tests := []struct {
		name    string
		imports []string
		want    []string // substrings, one per expected problem, in sorted order
	}{
		{
			name:    "allowed and third-party imports pass",
			imports: []string{modulePrefix + "internal/adapter", modulePrefix + "internal/domain", "context", "strings"},
		},
		{
			// The seeded violation: an adapter reaching back into core is
			// the exact shape #353 exists to make impossible.
			name:    "an import of core fails",
			imports: []string{modulePrefix + "internal/adapter", modulePrefix + "internal/core"},
			want:    []string{"imports internal/core"},
		},
		{
			name:    "an import of a concrete source fails",
			imports: []string{modulePrefix + "internal/source/nexusmods"},
			want:    []string{"imports internal/source/nexusmods"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkBoundary("internal/adapter/x", tt.imports, allowed)
			require.Len(t, got, len(tt.want), "problems: %v", got)
			for i, sub := range tt.want {
				assert.Contains(t, got[i], sub)
			}
		})
	}
}

// goBinary locates the go tool for the live checks below.
func goBinary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool not found on PATH: %v", err)
	}
	return p
}

// listImports runs a live `go list` for one module package.
func listImports(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command(goBinary(t), "list", "-f", `{{join .Imports "\n"}}`, modulePrefix+pkg).Output()
	require.NoError(t, err, "go list %s", pkg)
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// TestImportBoundary is the ratchet: every adapter package's non-test
// imports must stay within allowedImports.
func TestImportBoundary(t *testing.T) {
	for _, pkg := range adapterPackages {
		t.Run(pkg, func(t *testing.T) {
			problems := checkBoundary(pkg, listImports(t, pkg), allowedImports)
			if len(problems) > 0 {
				t.Fatalf("import boundary violated:\n  %s", strings.Join(problems, "\n  "))
			}
		})
	}
}

// TestAdapterPackagesNeverImportCore states the design's load-bearing rule
// on its own, so the failure names the rule rather than the allow-list: an
// adapter that can reach internal/core is an adapter that can grow a side
// effect, and side effects are core's, not an adapter's.
func TestAdapterPackagesNeverImportCore(t *testing.T) {
	for _, pkg := range adapterPackages {
		t.Run(pkg, func(t *testing.T) {
			for _, imp := range listImports(t, pkg) {
				assert.NotEqual(t, modulePrefix+"internal/core", imp,
					"%s imports internal/core: adapters supply rules and reports, core owns every side effect", pkg)
			}
		})
	}
}

// TestEveryAdapterPackageIsChecked keeps adapterPackages honest: a new
// adapter added under internal/adapter without a line above would otherwise
// never be checked at all, which is the one way this ratchet could silently
// stop ratcheting.
func TestEveryAdapterPackageIsChecked(t *testing.T) {
	out, err := exec.Command(goBinary(t), "list", modulePrefix+"internal/adapter/...").Output()
	require.NoError(t, err, "go list internal/adapter/...")
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		rel, ok := strings.CutPrefix(strings.TrimSpace(line), modulePrefix)
		if !ok {
			continue
		}
		assert.Contains(t, adapterPackages, rel,
			"%s is not in adapterPackages, so the boundary ratchet never checks it", rel)
	}
}

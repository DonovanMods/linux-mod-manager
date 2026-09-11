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
	"internal/adapter/icarus",
}

// allowedImports are the intra-module packages an adapter package may
// depend on (docs/plans/2026-09-10-game-adapter-design.md §4).
//
// internal/domain is the vocabulary; internal/adapter is the seam itself
// (a concrete adapter subpackage imports it; the identity adapter IS it).
// That is the whole list. internal/source was on it for U1 only, while
// adapter.MergeCompiler/MergeSource/MergeFailure were still aliases of
// source's declarations; U2 (#412) moved those declarations here with the
// Icarus implementation, and the entry went with them. A concrete adapter
// must not reach for a source of its own - a source is where bytes come
// from, an adapter is what a game does with them, and the Icarus split is
// the proof they are different questions.
//
// internal/core is the violation this whole design exists to prevent, and
// TestAdapterPackagesNeverImportCore asserts its absence by name so the
// failure message says WHY rather than just "outside the allowed set".
var allowedImports = []string{
	"internal/adapter",
	"internal/domain",
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

// TestCoreImportsTheSeamNeverAnAdapter is the other half of design §4's
// rule, and the half nothing ratcheted (#411, M5): internal/core imports
// internal/adapter - NEVER internal/adapter/<name>. Core resolving a game
// through the registry is the whole point of the seam; core naming a
// concrete adapter would put a game's rules back inside core one import at
// a time.
//
// It lives here, beside the rule's other half, rather than in a
// core-side boundary test of its own: the two halves are one rule, and a
// reader who finds one should find the other. Since U2 (#412) there IS a
// subpackage to catch: internal/app registers internal/adapter/icarus, and
// core resolves it through the registry without naming it.
func TestCoreImportsTheSeamNeverAnAdapter(t *testing.T) {
	const seam = modulePrefix + "internal/adapter"

	imports := listImports(t, "internal/core")
	assert.Contains(t, imports, seam, "internal/core must reach adapters through the seam")

	for _, imp := range imports {
		if sub, ok := strings.CutPrefix(imp, seam+"/"); ok {
			t.Errorf("internal/core imports internal/adapter/%s: core resolves an adapter through the registry, it never names one", sub)
		}
	}
}

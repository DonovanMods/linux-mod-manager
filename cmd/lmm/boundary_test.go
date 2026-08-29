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
const modulePrefix = "github.com/DonovanMods/linux-mod-manager/"

// allowedImports are the intra-module packages a frontend may depend on.
// Anything else means logic that belongs in internal/core has leaked into a
// command. See docs/plans/2026-08-27-v2-core-refactor-design.md §1.
var allowedImports = []string{
	"internal/app",
	"internal/core",
	"internal/domain",
	"internal/source", // the interface package only; its subpackages are not allowed
}

// boundaryAllowList records today's violations, each with the design-doc
// Phase 2 step that removes it. An entry whose import disappears must be
// deleted from this map (TestImportBoundary fails otherwise) so the list only
// ever shrinks. When it is empty, delete the map and this comment.
var boundaryAllowList = map[string]string{
	"internal/source/custom":  "lmm source list/validate --probe re-derive definitions; Phase 2 #8",
	"internal/source/steam":   "lmm game detect scans Steam libraries in cmd; Phase 2 #7",
	"internal/storage/config": "profiles/games/config read directly by 7 commands; Phase 2 #7-#8",
}

// checkBoundary returns one message per problem: an intra-module import that
// is neither allowed nor allow-listed, or an allow-list entry whose import no
// longer exists (a stale ratchet entry). Non-module imports are ignored.
func checkBoundary(imports []string, allowed []string, allowList map[string]string) []string {
	var problems []string
	seen := map[string]bool{}
	for _, imp := range imports {
		rel, ok := strings.CutPrefix(imp, modulePrefix)
		if !ok {
			continue
		}
		seen[rel] = true
		if slices.Contains(allowed, rel) {
			continue
		}
		if _, ok := allowList[rel]; ok {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"cmd/lmm imports %s, which is not a frontend-facing package: move the logic into internal/core (a new allow-list entry needs a design-doc reference)", rel))
	}
	for rel, why := range allowList {
		if !seen[rel] {
			problems = append(problems, fmt.Sprintf(
				"allow-list entry %q (%s) is stale: cmd/lmm no longer imports it — delete the entry so the ratchet only tightens", rel, why))
		}
	}
	slices.Sort(problems)
	return problems
}

func TestCheckBoundary(t *testing.T) {
	allowed := []string{"internal/core", "internal/domain"}
	tests := []struct {
		name      string
		imports   []string
		allowList map[string]string
		want      []string // substrings, one per expected problem, in sorted order
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
		{
			name:      "an allow-listed import passes",
			imports:   []string{modulePrefix + "internal/storage/db"},
			allowList: map[string]string{"internal/storage/db": "reason"},
		},
		{
			name:      "a stale allow-list entry fails",
			imports:   []string{modulePrefix + "internal/core"},
			allowList: map[string]string{"internal/storage/db": "reason"},
			want:      []string{`allow-list entry "internal/storage/db" (reason) is stale`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkBoundary(tt.imports, allowed, tt.allowList)
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
// within allowedImports plus the shrinking boundaryAllowList.
func TestImportBoundary(t *testing.T) {
	out, err := exec.Command(goBinary(t), "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	require.NoError(t, err, "go list ./cmd/lmm")
	imports := strings.Split(strings.TrimSpace(string(out)), "\n")

	problems := checkBoundary(imports, allowedImports, boundaryAllowList)
	if len(problems) > 0 {
		t.Fatalf("import boundary violated:\n  %s", strings.Join(problems, "\n  "))
	}
}

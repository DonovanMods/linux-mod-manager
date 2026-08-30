package core

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/testutil"
)

// TestExportedIdentifiersHaveDocComments is the doc-comment ratchet
// (v2 Phase 3 Task 20, #305): every exported identifier declared in this
// package's non-test files must carry a leading doc comment, so `go doc
// core` (and a future pkg.go.dev listing) never has a blank entry. The
// walker itself lives in internal/testutil.UndocumentedExports (v2 Phase 3
// Task 20 review, Important #1 / #305), shared with internal/domain and
// internal/app's own ratchets.
func TestExportedIdentifiersHaveDocComments(t *testing.T) {
	offenders, err := testutil.UndocumentedExports(".")
	if err != nil {
		t.Fatalf("parsing internal/core: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("%d exported identifier(s) missing a doc comment:\n%s", len(offenders), strings.Join(offenders, "\n"))
	}
}

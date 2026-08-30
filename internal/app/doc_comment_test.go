package app

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/testutil"
)

// TestExportedIdentifiersHaveDocComments is internal/core's doc-comment
// ratchet (v2 Phase 3 Task 20, #305) extended to internal/app: every
// exported identifier declared in this package's non-test files must carry
// a leading doc comment. The walker itself lives in
// internal/testutil.UndocumentedExports (v2 Phase 3 Task 20 review,
// Important #1 / #305), shared with internal/core and internal/domain's
// own ratchets.
func TestExportedIdentifiersHaveDocComments(t *testing.T) {
	offenders, err := testutil.UndocumentedExports(".")
	if err != nil {
		t.Fatalf("parsing internal/app: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("%d exported identifier(s) missing a doc comment:\n%s", len(offenders), strings.Join(offenders, "\n"))
	}
}

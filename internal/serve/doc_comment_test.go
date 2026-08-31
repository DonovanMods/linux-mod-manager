package serve

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"
)

// TestExportedIdentifiersHaveDocComments is the doc-comment ratchet
// extended to internal/serve (docs/plans/2026-08-30-serve-impl.md §Global
// Constraints), matching internal/core, internal/domain, and internal/app's
// own copies: every exported identifier declared in this package's
// non-test files must carry a leading doc comment.
func TestExportedIdentifiersHaveDocComments(t *testing.T) {
	offenders, err := testutil.UndocumentedExports(".")
	if err != nil {
		t.Fatalf("parsing internal/serve: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("%d exported identifier(s) missing a doc comment:\n%s", len(offenders), strings.Join(offenders, "\n"))
	}
}

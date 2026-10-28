package adapter

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"
)

// TestExportedIdentifiersHaveDocComments is the doc-comment ratchet
// extended to this package (docs/plans/2026-09-10-game-adapter-design.md
// §4), matching internal/core, internal/domain, internal/app and
// internal/serve's own copies: every exported identifier declared in this
// package's non-test files must carry a leading doc comment.
func TestExportedIdentifiersHaveDocComments(t *testing.T) {
	offenders, err := testutil.UndocumentedExports(".")
	if err != nil {
		t.Fatalf("parsing this package: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("%d exported identifier(s) missing a doc comment:\n%s", len(offenders), strings.Join(offenders, "\n"))
	}
}

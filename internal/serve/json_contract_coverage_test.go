package serve

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"
)

// jsonWireCoverageExclusions lists internal/serve struct types that carry a
// json tag yet intentionally have no recorded golden. Empty today: every
// wire shape this package defines is pinned by TestServeJSONGoldens. An
// entry here must record WHY, the same way internal/domain's own exclusion
// list does.
var jsonWireCoverageExclusions = map[string]bool{}

// TestJSONWireContractCoverage is the go/ast ratchet extended to
// internal/serve (docs/plans/2026-08-30-serve-impl.md Task 7): a serve type
// that gains json tags without gaining a golden fails the build instead of
// slipping into the wire contract unpinned. Unlike internal/core's and
// internal/domain's copies it scans UNEXPORTED types too, because every
// document this package defines is package-internal - nothing outside
// internal/serve ever constructs one.
func TestJSONWireContractCoverage(t *testing.T) {
	testutil.AssertJSONWireCoverage(t, ".", jsonWireCoverageExclusions)
}

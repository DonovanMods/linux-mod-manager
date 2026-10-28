package app

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"
)

// TestJSONContractCoverage asserts that every exported internal/app struct
// type carrying a `json:` tag has a recorded golden, the same guarantee
// internal/core's own TestJSONContractCoverage gives its query types (final
// review, Minor #1 / #301): the report's original reasoning for skipping
// this - one wire type didn't justify a second ~40-line copy of the AST
// scanner - stopped being a good reason once the scanner moved to
// internal/testutil, shared with core's own test.
func TestJSONContractCoverage(t *testing.T) {
	testutil.AssertJSONContractCoverage(t, ".", nil)
}

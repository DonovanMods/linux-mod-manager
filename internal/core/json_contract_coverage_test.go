package core_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"
)

// contractCoverageExclusions lists exported internal/core struct types that
// carry at least one `json:` tag but are intentionally NOT required to have
// their own testdata/json/<name>.golden, because they're pinned elsewhere or
// are deliberately outside the wire contract (final review, Important #3 /
// #282).
var contractCoverageExclusions = map[string]bool{
	// Scope is embedded (flattened) into every event type below, and all
	// nine are pinned by testdata/events/*.golden (events_golden_test.go),
	// not testdata/json.
	"Scope":            true,
	"DownloadEvent":    true,
	"HookEvent":        true,
	"MergeEvent":       true,
	"ModEvent":         true,
	"StepEvent":        true,
	"UpdateCheckEvent": true,
	"VerifyEvent":      true,
	"WarningEvent":     true,
	// MergedFingerprintEntry keeps its own on-disk merge-fingerprint format
	// (PascalCase-by-default, #197/#256) and is deliberately outside the
	// JSON wire contract.
	"MergedFingerprintEntry": true,
}

// TestJSONContractCoverage asserts that every exported internal/core struct
// type carrying a `json:` tag has a recorded golden, so a Phase 2 type that
// gains tags without gaining a golden fails CI instead of passing in
// silence (final review, Important #3 / #282). The scanner itself lives in
// internal/testutil (final review, Minor #1 / #301), shared with
// internal/app's own coverage test.
func TestJSONContractCoverage(t *testing.T) {
	testutil.AssertJSONContractCoverage(t, ".", contractCoverageExclusions)
}

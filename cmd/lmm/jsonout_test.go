package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmitJSON_OneIndentedDocumentWithTrailingNewline pins emitJSON's framing
// contract: 2-space indent, exactly one document, exactly one trailing
// newline - the invariant every --json caller piping stdout to a parser
// relies on.
func TestEmitJSON_OneIndentedDocumentWithTrailingNewline(t *testing.T) {
	type sample struct {
		Name string `json:"name"`
	}

	out := captureStdout(t, func() error { return emitJSON(sample{Name: "mod-a"}) })

	assert.Equal(t, "{\n  \"name\": \"mod-a\"\n}\n", out)
}

// TestEmitJSON_DeterministicMapOrder pins that map keys marshal in sorted
// order (json.Deterministic(true)) rather than Go's randomized map
// iteration - required for byte-stable goldens.
func TestEmitJSON_DeterministicMapOrder(t *testing.T) {
	m := map[string]int{"zeta": 1, "alpha": 2, "mu": 3}

	out := captureStdout(t, func() error { return emitJSON(m) })

	assert.Equal(t, "{\n  \"alpha\": 2,\n  \"mu\": 3,\n  \"zeta\": 1\n}\n", out)
}

// TestEmitJSON_NilSliceEncodesEmptyArray pins that a nil slice field renders
// as JSON `[]`, never `null` - JSON carries data, and a --json consumer
// ranging over a list field should never have to nil-check it first.
func TestEmitJSON_NilSliceEncodesEmptyArray(t *testing.T) {
	type sample struct {
		Items []string `json:"items"`
	}

	out := captureStdout(t, func() error { return emitJSON(sample{}) })

	assert.Equal(t, "{\n  \"items\": []\n}\n", out)
}

// fakeDetailedError is a test-only stand-in for a typed error that carries
// structured data for the --json error envelope's "details" field via the
// unnamed `Details() any` interface errorDetails looks for. The real one is
// *core.ConflictError - see TestReportError_JSON_ConflictError below.
type fakeDetailedError struct{ details any }

func (e *fakeDetailedError) Error() string { return "fake detailed error" }
func (e *fakeDetailedError) Details() any  { return e.details }

func TestErrorDetails_ErrStalePlan_ReturnsNil(t *testing.T) {
	err := fmt.Errorf("checking plan: %w", core.ErrStalePlan)

	assert.Nil(t, errorDetails(err))
}

func TestErrorDetails_PlainError_ReturnsNil(t *testing.T) {
	assert.Nil(t, errorDetails(errors.New("some other failure")))
}

func TestErrorDetails_TypedErrorWithData_ReturnsItsDetails(t *testing.T) {
	want := map[string]any{"conflicts": []string{"a.esp"}}
	err := &fakeDetailedError{details: want}

	assert.Equal(t, want, errorDetails(err))
}

func TestErrorDetails_WrappedTypedErrorWithData_ReturnsItsDetails(t *testing.T) {
	want := map[string]any{"conflicts": []string{"a.esp"}}
	err := fmt.Errorf("installing: %w", &fakeDetailedError{details: want})

	assert.Equal(t, want, errorDetails(err))
}

// TestReportError_Plain_Unaffected pins that reportError's non-JSON branch
// is untouched by this task: "Error: <msg>" on stderr.
func TestReportError_Plain_Unaffected(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out, err := captureStderrErr(t, func() error { reportError(errors.New("boom")); return nil })
	require.NoError(t, err)
	assert.Equal(t, "Error: boom\n", stripANSI(out))
}

// TestReportError_JSON_NoDetails pins the envelope shape for an error
// errorDetails finds no data for: {"error": "..."} with no "details" key at
// all (omitempty), through emitJSON's 2-space indent.
func TestReportError_JSON_NoDetails(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error { reportError(errors.New("boom")); return nil })

	assert.Equal(t, "{\n  \"error\": \"boom\"\n}\n", out)
}

// TestReportError_JSON_WithDetails pins that when errorDetails returns
// non-nil data, the envelope grows a "details" key after "error" (struct
// field order, not alphabetical - Ruling 3).
func TestReportError_JSON_WithDetails(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	err := &fakeDetailedError{details: map[string]any{"conflicts": []string{"a.esp"}}}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n  \"error\": \"fake detailed error\",\n  \"details\": {\n    \"conflicts\": [\n      \"a.esp\"\n    ]\n  }\n}\n", out)
}

// TestReportError_JSON_ConflictError pins the real typed error the Details()
// extension point exists for: *core.ConflictError's Error() text and its
// details payload, each core.Conflict in its own snake_case wire shape
// (Ruling 3: details is present only for typed errors that carry data).
func TestReportError_JSON_ConflictError(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	err := &core.ConflictError{Conflicts: []core.Conflict{
		{RelativePath: "shared.esp", CurrentSourceID: "test-src", CurrentModID: "other"},
	}}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"file conflict detected: 1 file(s) would be overwritten\",\n"+
		"  \"details\": {\n"+
		"    \"conflicts\": [\n"+
		"      {\n"+
		"        \"relative_path\": \"shared.esp\",\n"+
		"        \"current_source_id\": \"test-src\",\n"+
		"        \"current_mod_id\": \"other\"\n"+
		"      }\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
	assert.ErrorIs(t, err, domain.ErrFileConflict, "the envelope text is the domain sentinel's, plus the count")
}

// TestReportError_JSON_SuppressesAlreadyReported and ErrCancelled's exit-2,
// no-JSON behaviour are pinned pre-existing in local_update_test.go
// (TestReportError_SuppressesAlreadyReported) and root_test.go
// (TestRoot_LogLevel_InvalidErrorTextIsExactEverywhere) respectively - this
// task does not change that wiring, only reportError's --json byte shape.

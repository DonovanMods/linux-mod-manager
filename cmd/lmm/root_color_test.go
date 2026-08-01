package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestReportError_PlainWhenColorDisabled is the byte-stability guard: the
// existing "Error: %v" format must be unchanged when color is off (the
// default).
func TestReportError_PlainWhenColorDisabled(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out, _ := captureStderrErr(t, func() error {
		reportError(errors.New("boom"))
		return nil
	})

	assert.Equal(t, "Error: boom\n", out)
}

// TestReportError_ColorPath extends reportError's "Error:" prefix with the
// existing colorRed convention (deploy.go/verify.go's error markers).
func TestReportError_ColorPath(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	resetColorFlags(t)
	withColorCapableStdout(t, true)
	out, _ := captureStderrErr(t, func() error {
		reportError(errors.New("boom"))
		return nil
	})

	assert.Equal(t, colorRed("Error:")+" boom\n", out)
}

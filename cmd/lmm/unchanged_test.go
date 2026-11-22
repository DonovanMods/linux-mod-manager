package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPrintUnchanged (#672): a check that answered mods from their record
// says how many, and the flag that asks about them all again.
func TestPrintUnchanged(t *testing.T) {
	out := captureStdout(t, func() error { printUnchanged(340, "lmm update --refresh"); return nil })
	assert.Equal(t, "340 mods unchanged since the last check, not asked again ('lmm update --refresh' asks about every mod).\n", out)
	out = captureStdout(t, func() error { printUnchanged(0, "lmm update --refresh"); return nil })
	assert.Empty(t, out)
}

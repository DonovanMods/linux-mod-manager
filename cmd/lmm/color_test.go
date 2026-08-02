package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withColorCapableStdout forces stdoutColorCapable() to report a
// color-capable TTY for the duration of the test, without needing a real
// pty - the seam tests override to simulate "running interactively".
func withColorCapableStdout(t *testing.T, capable bool) {
	t.Helper()
	old := stdoutColorCapable
	stdoutColorCapable = func() bool { return capable }
	t.Cleanup(func() { stdoutColorCapable = old })
}

func resetColorFlags(t *testing.T) {
	t.Helper()
	oldNoColor := noColor
	noColor = false
	t.Cleanup(func() { noColor = oldNoColor })

	oldEnv, hadEnv := os.LookupEnv("NO_COLOR")
	require.NoError(t, os.Unsetenv("NO_COLOR"))
	t.Cleanup(func() {
		if hadEnv {
			require.NoError(t, os.Setenv("NO_COLOR", oldEnv))
		} else {
			require.NoError(t, os.Unsetenv("NO_COLOR"))
		}
	})
}

func TestColorEnabled_DefaultsOffWhenStdoutIsNotATerminal(t *testing.T) {
	resetColorFlags(t)
	withColorCapableStdout(t, false)

	assert.False(t, colorEnabled(), "piped/redirected stdout must never emit color by default")
}

func TestColorEnabled_OnByDefaultWhenStdoutIsATerminal(t *testing.T) {
	resetColorFlags(t)
	withColorCapableStdout(t, true)

	assert.True(t, colorEnabled(), "a color-capable TTY with no opt-out should color by default")
}

func TestColorEnabled_NoColorFlagWinsOverTTY(t *testing.T) {
	resetColorFlags(t)
	withColorCapableStdout(t, true)
	oldNoColor := noColor
	noColor = true
	t.Cleanup(func() { noColor = oldNoColor })

	assert.False(t, colorEnabled(), "--no-color must disable color even on a real TTY")
}

func TestColorEnabled_NoColorEnvWinsOverTTY(t *testing.T) {
	resetColorFlags(t)
	withColorCapableStdout(t, true)
	require.NoError(t, os.Setenv("NO_COLOR", "1"))

	assert.False(t, colorEnabled(), "NO_COLOR must disable color even on a real TTY")
}

// TestColorEnabled_EmptyNoColorEnvStillDisables guards the no-color.org spec:
// NO_COLOR disables color when PRESENT, regardless of value - including the
// empty string. os.Getenv can't distinguish "unset" from "set to empty", so
// colorEnabled must use os.LookupEnv instead.
func TestColorEnabled_EmptyNoColorEnvStillDisables(t *testing.T) {
	resetColorFlags(t)
	withColorCapableStdout(t, true)
	require.NoError(t, os.Setenv("NO_COLOR", ""))

	assert.False(t, colorEnabled(), "NO_COLOR set to the empty string must still disable color (presence-only semantics)")
}

func TestColorHelpers_NoOpWhenColorDisabled(t *testing.T) {
	resetColorFlags(t)
	withColorCapableStdout(t, false)

	assert.Equal(t, "text", colorGreen("text"))
	assert.Equal(t, "text", colorRed("text"))
	assert.Equal(t, "text", colorYellow("text"))
	assert.Equal(t, "text", colorBold("text"))
	assert.Equal(t, "text", colorDim("text"))
	assert.Equal(t, "text", colorCyan("text"))
	assert.Equal(t, "text", colorHeader("text"))
}

func TestColorHelpers_WrapWhenColorEnabled(t *testing.T) {
	resetColorFlags(t)
	withColorCapableStdout(t, true)

	assert.Equal(t, ansiGreen+"text"+ansiReset, colorGreen("text"))
	assert.Equal(t, ansiRed+"text"+ansiReset, colorRed("text"))
	assert.Equal(t, ansiYellow+"text"+ansiReset, colorYellow("text"))
	assert.Equal(t, ansiBold+"text"+ansiReset, colorBold("text"))
	assert.Equal(t, ansiDim+"text"+ansiReset, colorDim("text"))
	assert.Equal(t, ansiCyan+"text"+ansiReset, colorCyan("text"))
}

// TestColorHeader_IsBoldAndCyan guards #193's richer header accent: bold
// alone read as nearly plain in smoke feedback, so a table/section header is
// now bold+cyan together, not bold-only.
func TestColorHeader_IsBoldAndCyan(t *testing.T) {
	resetColorFlags(t)
	withColorCapableStdout(t, true)

	got := colorHeader("text")
	assert.Contains(t, got, ansiBold)
	assert.Contains(t, got, ansiCyan)
	assert.Contains(t, got, "text")
	assert.Equal(t, "text", stripANSI(got))
}

// TestPrintTable_ColorNeverShiftsColumnAlignment guards the exact regression
// this feature risks: text/tabwriter computes column padding from raw byte
// length, so injecting ANSI escapes into a cell BEFORE it reaches the
// tabwriter would inflate that cell's measured width and misalign every
// column after it. printTable colors the ALREADY-flushed, plain-padded
// text instead - stripping ANSI from its output must reproduce the
// plain-mode text byte-for-byte, for any row-color choice.
func TestPrintTable_ColorNeverShiftsColumnAlignment(t *testing.T) {
	resetColorFlags(t)

	build := func() *bytes.Buffer {
		var buf bytes.Buffer
		buf.WriteString("ID\tNAME\tENABLED\n")
		buf.WriteString("--\t----\t-------\n")
		buf.WriteString("modA\tSome Mod\tyes\n")
		buf.WriteString("modB-longer-id\tAnother\tno\n")
		// tabwriter enforcement of column widths happens on Flush of a live
		// writer; simulate its already-flushed output directly since the
		// production code always calls printTable post-Flush.
		return &buf
	}

	plainBuf := build()
	var plainOut bytes.Buffer
	withColorCapableStdout(t, false)
	require.NoError(t, printTableTo(&plainOut, plainBuf, 2, nil))

	coloredBuf := build()
	var coloredOut bytes.Buffer
	withColorCapableStdout(t, true)
	rowColor := func(i int) func(string) string {
		if i == 1 {
			return colorRed
		}
		return colorGreen
	}
	require.NoError(t, printTableTo(&coloredOut, coloredBuf, 2, rowColor))

	stripped := stripANSI(coloredOut.String())
	assert.Equal(t, plainOut.String(), stripped, "color must not change the visible text or alignment")
	assert.Contains(t, coloredOut.String(), ansiBold, "header line should be bold+cyan when color is enabled")
	assert.Contains(t, coloredOut.String(), ansiCyan, "header line should be bold+cyan when color is enabled")
	assert.Contains(t, coloredOut.String(), ansiGreen)
	assert.Contains(t, coloredOut.String(), ansiRed)
}

func stripANSI(s string) string {
	for {
		start := strings.Index(s, "\x1b[")
		if start == -1 {
			return s
		}
		end := strings.Index(s[start:], "m")
		if end == -1 {
			return s
		}
		s = s[:start] + s[start+end+1:]
	}
}

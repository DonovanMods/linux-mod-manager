package serve

// The keyboard-shortcuts drift ratchet (issue 334's gate review, Important
// 3). The SPA's help modal and the README's Keyboard table document the
// same bindings, and the design's own rule for two surfaces saying the same
// thing is that there is one source: spa/app/shortcuts.js. This test is what
// makes that true rather than merely intended - the README is prose nobody
// executes, so nothing else would notice it going stale.
//
// It reads both files as text, which is the only option available: there is
// no JS runtime in the build (no Node anywhere, by design), so the module's
// row literals are matched structurally rather than evaluated. That is
// enough for the failure this guards - a binding added, removed or reworded
// on one side only - and the format it requires of the module (one
// {keys, where, what} object literal per row, plain double-quoted strings)
// is the format it already has.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortcutRowPattern matches one {keys, where, what} literal in
// spa/app/shortcuts.js, in the module's own field order.
var shortcutRowPattern = regexp.MustCompile(
	`(?s)\{\s*keys:\s*"((?:[^"\\]|\\.)*)",\s*where:\s*"((?:[^"\\]|\\.)*)",\s*what:\s*"((?:[^"\\]|\\.)*)",?\s*\}`)

// readmeMarkup strips the decoration the README adds and the module does
// not carry: `code` backticks and **bold**. What is left is the plain text
// both sides agree on.
var readmeMarkup = strings.NewReplacer("`", "", "**", "")

func TestSPAKeyboardShortcutsMatchTheREADME(t *testing.T) {
	module, err := os.ReadFile(filepath.Join("spa", "app", "shortcuts.js"))
	require.NoError(t, err)

	matches := shortcutRowPattern.FindAllStringSubmatch(string(module), -1)
	require.NotEmpty(t, matches,
		"spa/app/shortcuts.js must declare its rows as {keys, where, what} object literals - this test reads them as text, since the build has no JS runtime")

	var fromModule [][3]string
	for _, m := range matches {
		fromModule = append(fromModule, [3]string{m[1], m[2], m[3]})
	}

	fromREADME := readmeKeyboardTable(t)
	assert.Equal(t, fromModule, fromREADME,
		"the README's Keyboard table and spa/app/shortcuts.js must document the same bindings, in the same order - one of them was changed alone")
}

// readmeKeyboardTable returns the rows of the "### Keyboard" section's one
// markdown table, header and alignment row dropped, each cell stripped of
// markdown decoration and surrounding space.
func readmeKeyboardTable(t *testing.T) [][3]string {
	t.Helper()

	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	require.NoError(t, err)

	_, after, found := strings.Cut(string(readme), "\n### Keyboard\n")
	require.True(t, found, "README.md must carry a '### Keyboard' section")
	section, _, _ := strings.Cut(after, "\n### ")

	var rows [][3]string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 3 {
			continue
		}
		var row [3]string
		var separator bool
		for i, cell := range cells {
			cell = strings.TrimSpace(readmeMarkup.Replace(cell))
			if strings.Trim(cell, "-") == "" {
				separator = true
			}
			row[i] = cell
		}
		if separator || row == [3]string{"Key", "Where", "What it does"} {
			continue
		}
		rows = append(rows, row)
	}
	require.NotEmpty(t, rows, "the Keyboard section must carry a table of bindings")
	return rows
}

package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

// helpWrapMinimum is how short a mid-paragraph line has to be before it
// reads as an orphan rather than a wrap. The help corpus wraps at ~72
// columns, so anything under this ended a line early - a leftover from an
// edit that inserted or removed words without re-wrapping the paragraph.
const helpWrapMinimum = 40

// TestCommandHelpParagraphsAreRewrapped is P1b review F8, generalised to the
// corpus it belongs to. #396's flag rename left
//
//	metadata
//	(name, author,
//	version, summary, URL) is fetched from it automatically
//
// in `lmm mod edit --help` and, through `make man`, in lmm-mod-edit.1.
// Nothing catches that: help text is prose, and the man pages are generated
// FROM it, so a bad wrap is faithfully carried into the shipped page.
//
// A line is an orphan when it sits mid-paragraph (a non-blank line follows),
// is well short of the wrap column, is not indented (examples and lists set
// their own shape) and does not end in a colon (a heading like "Useful
// for:"). Re-wrap the paragraph and re-run `make man`.
func TestCommandHelpParagraphsAreRewrapped(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		lines := strings.Split(c.Long, "\n")
		for i, line := range lines {
			if i+1 >= len(lines) {
				break
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.TrimSpace(lines[i+1]) == "" {
				continue // end of a paragraph: a short last line is fine
			}
			if line != trimmed || strings.HasSuffix(trimmed, ":") {
				continue // indented block, or a heading
			}
			assert.GreaterOrEqual(t, len(line), helpWrapMinimum,
				"%s: %q ends a line early mid-paragraph - re-wrap it (and re-run `make man`)",
				c.CommandPath(), line)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

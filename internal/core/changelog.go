package core

import (
	"regexp"
	"strings"
)

// CleanChangelog strips HTML markup from html for readable terminal
// display: <br> and <p>/</p> become newlines, every other tag is removed
// outright, and the five common HTML entities a mod source's changelog HTML
// typically contains (&nbsp; &amp; &lt; &gt; &quot;) are decoded. The result
// is trimmed of leading/trailing whitespace but is otherwise the FULL
// cleaned text - no truncation happens here; that stays a presentation
// concern for each caller (cmd/lmm/update.go truncates to 800/500 chars for
// its own "Changelogs:"/"Changelog:" blocks).
//
// Used by cmd/lmm/update.go's doUpdate/applySingleUpdate (moved here
// verbatim, Phase 6b Task 7, from that package's former unexported
// stripHTMLForTerminal) to strip a source's raw HTML changelog for terminal
// display.
//
// Despite the name (it predates any other caller), this is a general
// HTML-to-terminal cleaner, not changelog-specific: #86 also routes mod
// descriptions through it, so `lmm mod show` renders a source's markup
// through the same cleaner. Left named CleanChangelog to avoid churning
// three unrelated call sites for a rename.
var (
	changelogBreakRE = regexp.MustCompile(`(?i)<br\s*/?>|</p>|<p[^>]*>`)
	changelogTagRE   = regexp.MustCompile(`<[^>]*>`)
)

func CleanChangelog(html string) string {
	// Replace block/line breaks with newlines
	html = changelogBreakRE.ReplaceAllString(html, "\n")
	// Remove remaining tags
	html = changelogTagRE.ReplaceAllString(html, "")
	// Decode common entities
	html = strings.ReplaceAll(html, "&nbsp;", " ")
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")
	return strings.TrimSpace(html)
}

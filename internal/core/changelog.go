package core

import (
	"regexp"
	"strings"
)

// CleanChangelog strips HTML markup from html for readable terminal/TUI
// display: <br> and <p>/</p> become newlines, every other tag is removed
// outright, and the five common HTML entities a mod source's changelog HTML
// typically contains (&nbsp; &amp; &lt; &gt; &quot;) are decoded. The result
// is trimmed of leading/trailing whitespace but is otherwise the FULL
// cleaned text - no truncation happens here; that stays a presentation
// concern for each caller (cmd/lmm/update.go truncates to 800/500 chars for
// its own "Changelogs:"/"Changelog:" blocks, and the TUI's changelog overlay
// scrolls instead of truncating - see UpdateItem.Changelog's doc comment).
//
// Shared by cmd/lmm/update.go's doUpdate/applySingleUpdate (moved here
// verbatim, Phase 6b Task 7, from that package's former unexported
// stripHTMLForTerminal) and internal/tui's coreProvider.CheckUpdates
// (service_core.go), so the CLI and TUI strip a source's raw HTML changelog
// identically.
//
// Despite the name (it predates any other caller), this is a general
// HTML-to-terminal cleaner, not changelog-specific: #86 also routes mod
// descriptions through it, so `lmm mod show` and the TUI's details view
// render a source's markup identically. Left named CleanChangelog to avoid
// churning three unrelated call sites for a rename.
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

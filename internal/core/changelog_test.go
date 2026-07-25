package core

import "testing"

// TestCleanChangelogMatchesLegacyStrip ports cmd/lmm/update.go's former
// unexported stripHTMLForTerminal test cases onto the moved, exported
// CleanChangelog (Phase 6b Task 7): <br>/<p> become newlines, every other
// tag is removed, the five entities stripHTMLForTerminal decoded are still
// decoded, and the result is trimmed - byte-for-byte the same transform, now
// shared by the CLI (cmd/lmm/update.go) and the TUI (coreProvider.
// CheckUpdates, service_core.go).
func TestCleanChangelogMatchesLegacyStrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text passthrough", "Fixed some bugs.", "Fixed some bugs."},
		{"br becomes newline", "Line one<br>Line two", "Line one\nLine two"},
		{"self-closing br becomes newline", "Line one<br/>Line two", "Line one\nLine two"},
		{"spaced self-closing br becomes newline", "Line one<br />Line two", "Line one\nLine two"},
		{"paragraph tags become newlines", "<p>First</p><p>Second</p>", "First\n\nSecond"},
		{"paragraph tag with attributes becomes newline", `<p class="note">First</p>`, "First"},
		{"other tags are stripped without adding newlines", "Fixed <b>some</b> <i>bugs</i>.", "Fixed some bugs."},
		{"nbsp decodes to space", "a&nbsp;b", "a b"},
		{"amp decodes to ampersand", "Health &amp; Stamina", "Health & Stamina"},
		{"lt and gt decode to angle brackets", "&lt;script&gt;", "<script>"},
		{"quot decodes to double quote", "&quot;quoted&quot;", "\"quoted\""},
		{"leading and trailing whitespace is trimmed", "  <p>padded</p>  ", "padded"},
		{
			"combined html changelog",
			"<p>Fixed some <b>bugs</b>.</p><p>Added &amp; improved textures.</p>",
			"Fixed some bugs.\n\nAdded & improved textures.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanChangelog(tt.in)
			if got != tt.want {
				t.Errorf("CleanChangelog(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

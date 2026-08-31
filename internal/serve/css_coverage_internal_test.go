package serve

// css_coverage_internal_test.go ratchets the gap task-9 review's
// carry-forward found: internal/serve/static/app.css (the hand-written
// stopgap - see its own header comment) went two units without gaining the
// utility classes Tasks 4/8/9 added to the templates, so the whole browser
// UI rendered essentially unstyled with no test ever noticing. This pins
// every class name a template references against app.css's defined
// selectors, so the NEXT template addition that forgets to extend the
// stopgap fails the build instead of silently shipping unstyled.

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var classAttrPattern = regexp.MustCompile(`class="([^"]*)"`)

// templateClassNames returns every static class name referenced by a
// class="..." attribute across every embedded template. A token containing
// "{{" or "}}" is a Go template action, not a class name, and is skipped -
// none of the current templates interpolate INTO a class attribute (every
// conditional class instead picks between whole, separate class="..."
// attributes on different branches), so this is exact today, not a
// best-effort approximation.
func templateClassNames(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}

	entries, err := fs.Glob(templateFS, "templates/*.gohtml")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, name := range entries {
		data, err := templateFS.ReadFile(name)
		require.NoError(t, err)
		for _, m := range classAttrPattern.FindAllStringSubmatch(string(data), -1) {
			for _, cls := range strings.Fields(m[1]) {
				if strings.Contains(cls, "{{") || strings.Contains(cls, "}}") {
					t.Fatalf("%s: class attribute %q interpolates a Go template action - "+
						"templateClassNames's static-only assumption no longer holds, "+
						"update it to extract the real class names", name, m[1])
				}
				seen[cls] = true
			}
		}
	}

	names := make([]string, 0, len(seen))
	for cls := range seen {
		names = append(names, cls)
	}
	sort.Strings(names)
	return names
}

// cssSelectorFor is the escaped selector Tailwind (and app.css, by hand)
// emits for a utility class name: every character a class name can carry
// that a bare CSS selector cannot (`:`, `[`, `]`, `.`) is backslash-escaped.
func cssSelectorFor(class string) string {
	replacer := strings.NewReplacer(
		":", `\:`,
		"[", `\[`,
		"]", `\]`,
		".", `\.`,
	)
	return "." + replacer.Replace(class)
}

// TestAppCSS_CoversEveryTemplateClass is the ratchet: every class name a
// template references must have a defined selector in the committed
// app.css (whether that file is the hand-written stopgap or a real
// Tailwind build - the check is the same either way).
func TestAppCSS_CoversEveryTemplateClass(t *testing.T) {
	css, err := staticFS.ReadFile("static/app.css")
	require.NoError(t, err)

	var missing []string
	for _, class := range templateClassNames(t) {
		if !strings.Contains(string(css), cssSelectorFor(class)) {
			missing = append(missing, class)
		}
	}

	assert.Empty(t, missing, "these template classes have no definition in internal/serve/static/app.css - "+
		"the browser UI renders unstyled for them until it's extended (or `make css` re-run)")
}

package serve_test

// The color-token ratchet (docs/plans/2026-08-31-webui-impl.md Unit 2:
// "Both theme token sets exercised (no hardcoded colors - tokens only; a
// grep-style test)"). Every visual color in this application has to come
// from a CSS custom property so both Launcher token sets - dark and light -
// actually apply everywhere; a component style that reaches for a literal
// hex or rgb() instead paints identically in both themes, which is a
// passing build and a broken screen the moment someone switches.
//
// A grep ratchet is the right shape here for the same reason
// no_unsafe_dom_test.go's is: the failure it prevents is someone reaching
// for a literal color under deadline, and it costs nothing to make that
// fail the build instead.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// colorLiteral matches a CSS hex color or an rgb()/rgba() function call -
// the two shapes a literal (non-token) color takes in this codebase.
var colorLiteral = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|rgba?\(`)

// tokenBlock matches one of the three blocks app.css defines Launcher's
// tokens in - the ONLY place a literal color may appear. Order matters: the
// dark override block's selector text ("dark") is a substring-free match
// of the plain :root block's, so removing the nested @media block FIRST
// (it is the only one that itself contains a closing brace before its own)
// leaves the other two as flat, single-level matches.
var tokenBlocks = []*regexp.Regexp{
	regexp.MustCompile(`(?s)@media \(prefers-color-scheme: dark\)\s*\{\s*:root:not\(\[data-theme="light"\]\)\s*\{[^}]*\}\s*\}`),
	regexp.MustCompile(`(?s):root\s*\{[^}]*\}`),
	regexp.MustCompile(`(?s):root\[data-theme="dark"\]\s*\{[^}]*\}`),
}

// TestNoHardcodedColors walks internal/serve/spa for stylesheets and fails
// on any hex/rgb() literal found OUTSIDE the three token-definition blocks.
// Component styles must reach for var(--token) instead - the whole point of
// defining both Launcher sets as custom properties in the first place.
func TestNoHardcodedColors(t *testing.T) {
	err := filepath.Walk(filepath.Join(".", "spa"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".css" {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		rest := string(data)
		for _, block := range tokenBlocks {
			rest = block.ReplaceAllString(rest, "")
		}

		if loc := colorLiteral.FindStringIndex(rest); loc != nil {
			t.Errorf("%s: hardcoded color literal outside the token-definition blocks: %q - use var(--token) instead",
				path, rest[loc[0]:min(loc[1]+12, len(rest))])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking spa: %v", err)
	}
}

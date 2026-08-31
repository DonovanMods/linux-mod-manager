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
//
// Minor 8 (unit 2 gate review): the original walk covered only *.css and
// only #hex/rgb()/rgba(). Widened here to *.js and *.html (an inline style
// or a literal in a template string is just as much a hardcoded color) and
// to the hsl()/hsla()/oklch()/color() function forms - all four are
// function CALLS, so they carry no false-positive risk the way a bare
// color NAME would. Named colors ("red", "white", ...) are deliberately
// NOT added: Go's regexp is RE2, which has no lookahead/lookbehind, and a
// naive \bwhite\b matches "white-space" (a real declaration in this file)
// with no way to exclude it without one - a real defect, not a hypothetical
// one, caught while writing this widening. Catching named colors needs a
// CSS-value-position-aware matcher, not a grep ratchet; left as a residual
// gap rather than shipped with a false positive baked in.
import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// colorLiteral matches a CSS hex color or an rgb()/rgba()/hsl()/hsla()/
// oklch()/color() function call - the shapes a literal (non-token) color
// takes in this codebase. See the package doc comment above for why named
// colors are deliberately not included.
var colorLiteral = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(|oklch\(|color\(`)

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

// htmlEntity matches a numeric character reference ("&#8230;", the SPA's
// own ellipsis/em-dash). Several .js/.html files spell one this way, and
// its digits alone (e.g. "8230") satisfy colorLiteral's hex pattern just as
// well as a real color would - stripped before matching for the same
// reason tokenBlocks is, in .css: it is the one legitimate shape that would
// otherwise false-positive once the walk covers .js/.html too.
var htmlEntity = regexp.MustCompile(`&#\d+;?`)

// hardcodedColorExtensions are the file types walked for a literal color -
// CSS values, but also any JS/HTML that could carry an inline style or a
// literal in a template string.
var hardcodedColorExtensions = map[string]bool{".css": true, ".js": true, ".html": true}

// TestNoHardcodedColors walks internal/serve/spa and fails on any color
// literal found outside app.css's three token-definition blocks (CSS files
// only - .js/.html carry no such blocks to begin with). Component styles
// must reach for var(--token) instead - the whole point of defining both
// Launcher sets as custom properties in the first place.
func TestNoHardcodedColors(t *testing.T) {
	err := filepath.Walk(filepath.Join(".", "spa"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !hardcodedColorExtensions[filepath.Ext(path)] {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		rest := htmlEntity.ReplaceAllString(string(data), "")
		if filepath.Ext(path) == ".css" {
			for _, block := range tokenBlocks {
				rest = block.ReplaceAllString(rest, "")
			}
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

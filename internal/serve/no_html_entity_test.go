package serve_test

// The literal-entity ratchet (unit7-review.md Important 2): `htm` does not
// HTML-decode static text chunks, so a numeric character reference like
// "&#8230;" reaches the screen verbatim instead of rendering as "…" - the
// first thing a user saw on every page load before this was fixed. A named
// reference ("&hellip;", "&amp;", ...) would fail the same way.
//
// A grep ratchet is the right shape here for the same reason
// no_hardcoded_color_test.go's is: the failure it prevents is someone
// reaching for the HTML-source spelling of a special character under
// deadline, in a file with no HTML parser to decode it, and it costs
// nothing to make that fail the build instead.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// htmlEntityLiteral matches a numeric ("&#8230;") or named ("&amp;")
// character reference. Both spellings are only ever a mistake in a .js
// file under spa/app: there is no HTML parser there to decode either one,
// so the literal source text reaches the browser unchanged.
var htmlEntityLiteral = regexp.MustCompile(`&#\d+;|&[a-zA-Z]+;`)

// TestNoLiteralHTMLEntities walks internal/serve/spa/app for any .js file
// spelling a special character as an HTML entity rather than the literal
// character itself.
func TestNoLiteralHTMLEntities(t *testing.T) {
	err := filepath.Walk(filepath.Join(".", "spa", "app"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".js" {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if loc := htmlEntityLiteral.FindIndex(data); loc != nil {
			t.Errorf("%s: literal HTML entity %q - use the actual character instead (htm does not decode static text)",
				path, data[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking spa/app: %v", err)
	}
}

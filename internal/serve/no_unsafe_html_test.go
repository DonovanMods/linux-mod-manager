package serve_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// unsafeTemplateCast matches a call to the two html/template conversions
// that opt a value out of auto-escaping (the type named HTML, and the type
// named JS, from the standard library's template package, each followed by
// an open paren). docs/plans/2026-08-30-serve-design.md §Rendering model
// and §Security require every page to stay auto-escaped; WEBUI.md forbids
// raw HTML injection outside that boundary. This is the ratchet
// (docs/plans/2026-08-30-serve-impl.md §Global Constraints): any use of
// either cast anywhere under internal/serve must be a deliberate, reviewed
// exception, and until one is needed the grep must come back empty.
var unsafeTemplateCast = regexp.MustCompile(`\btemplate\.(HTML|JS)\(`)

// TestNoUnsafeTemplateCasts walks every .go file in this package (tests
// included - a helper that builds unsafe HTML for a test fixture is just as
// forbidden as one in production code) and fails if any file calls either
// forbidden html/template conversion (see unsafeTemplateCast).
func TestNoUnsafeTemplateCasts(t *testing.T) {
	var offenders []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if unsafeTemplateCast.Match(src) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/serve: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("template.HTML/template.JS casts are forbidden in internal/serve, found in: %s", strings.Join(offenders, ", "))
	}
}

// Package testutil holds test-only helpers shared across more than one
// package's own test files. It is never imported by production code, so it
// never reaches the shipped lmm binary - only test binaries pull it in.
package testutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// AssertJSONContractCoverage asserts that every exported struct type
// declared directly under dir and carrying at least one `json:` struct tag
// has a recorded golden at dir/testdata/json/<snake-case-name>.golden,
// except a name listed in exclusions (the caller records the reason at its
// own call site) - so a type that gains json tags without gaining a golden
// fails CI instead of passing in silence (originally core's final review,
// Important #3 / #282; lifted into a shared helper - review Minor #1 / #301
// - so app's single wire type gets the same enforcement without a second
// ~40-line copy of the scanner).
func AssertJSONContractCoverage(t *testing.T, dir string, exclusions map[string]bool) {
	t.Helper()

	tagged, err := JSONTaggedExportedStructs(dir)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	assertGoldensExist(t, dir, tagged, exclusions)
}

// AssertJSONWireCoverage is AssertJSONContractCoverage for a package whose
// wire types are package-INTERNAL - internal/serve, whose job status
// document, plan response and error envelope are all unexported because
// nothing outside the package constructs them. They are still wire surface
// a client parses, so they get the same "json tag implies golden" ratchet;
// the only difference is that the scan does not skip unexported names.
func AssertJSONWireCoverage(t *testing.T, dir string, exclusions map[string]bool) {
	t.Helper()

	tagged, err := JSONTaggedStructs(dir)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	assertGoldensExist(t, dir, tagged, exclusions)
}

// assertGoldensExist is the shared half of the two coverage assertions:
// every scanned name needs dir/testdata/json/<snake>.golden unless it is
// excluded.
func assertGoldensExist(t *testing.T, dir string, tagged, exclusions map[string]bool) {
	t.Helper()

	for name := range tagged {
		if exclusions[name] {
			continue
		}
		golden := filepath.Join(dir, "testdata", "json", PascalToSnake(name)+".golden")
		if _, err := os.Stat(golden); err != nil {
			t.Errorf("%s carries a json tag but has no golden at %s (add a TestJSONGoldens row, or add it to the exclusions with a reason)", name, golden)
		}
	}
}

// JSONTaggedExportedStructs parses every non-test .go file directly under
// dir and returns the set of exported struct type names with at least one
// field carrying a `json:` struct tag. Walks the directory itself (rather
// than go/parser.ParseDir, deprecated since Go 1.25) so it stays plain
// stdlib with no new dependency on golang.org/x/tools/go/packages.
func JSONTaggedExportedStructs(dir string) (map[string]bool, error) {
	return jsonTaggedStructs(dir, true)
}

// JSONTaggedStructs is JSONTaggedExportedStructs including UNEXPORTED
// struct types - the scan a package whose wire types are package-internal
// needs (see AssertJSONWireCoverage).
func JSONTaggedStructs(dir string) (map[string]bool, error) {
	return jsonTaggedStructs(dir, false)
}

// jsonTaggedStructs is the shared scanner; exportedOnly skips unexported
// type names.
func jsonTaggedStructs(dir string, exportedOnly bool) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	tagged := make(map[string]bool)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}

		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || (exportedOnly && !ts.Name.IsExported()) {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				if structHasJSONTag(st) {
					tagged[ts.Name.Name] = true
				}
			}
		}
	}
	return tagged, nil
}

func structHasJSONTag(st *ast.StructType) bool {
	if st.Fields == nil {
		return false
	}
	for _, field := range st.Fields.List {
		if field.Tag != nil && strings.Contains(field.Tag.Value, "json:") {
			return true
		}
	}
	return false
}

// PascalToSnake converts a PascalCase Go identifier (e.g. "SourceWarning")
// to the snake_case golden basename convention TestJSONGoldens' table uses
// (e.g. "source_warning"). Every type name on the contract today is plain
// PascalCase with no acronym runs, so this simple per-letter rule matches
// every recorded golden filename exactly.
func PascalToSnake(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

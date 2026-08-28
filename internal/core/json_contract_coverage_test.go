package core_test

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

// contractCoverageExclusions lists exported internal/core struct types that
// carry at least one `json:` tag but are intentionally NOT required to have
// their own testdata/json/<name>.golden, because they're pinned elsewhere or
// are deliberately outside the wire contract (final review, Important #3 /
// #282).
var contractCoverageExclusions = map[string]bool{
	// Scope is embedded (flattened) into every event type below, and all
	// nine are pinned by testdata/events/*.golden (events_golden_test.go),
	// not testdata/json.
	"Scope":            true,
	"DownloadEvent":    true,
	"HookEvent":        true,
	"MergeEvent":       true,
	"ModEvent":         true,
	"StepEvent":        true,
	"UpdateCheckEvent": true,
	"VerifyEvent":      true,
	"WarningEvent":     true,
	// MergedFingerprintEntry keeps its own on-disk merge-fingerprint format
	// (PascalCase-by-default, #197/#256) and is deliberately outside the
	// JSON wire contract.
	"MergedFingerprintEntry": true,
}

// TestJSONContractCoverage asserts that every exported internal/core struct
// type carrying a `json:` tag has a recorded golden, so a Phase 2 type that
// gains tags without gaining a golden fails CI instead of passing in
// silence (final review, Important #3 / #282).
func TestJSONContractCoverage(t *testing.T) {
	tagged, err := jsonTaggedExportedStructs(".")
	if err != nil {
		t.Fatalf("parsing internal/core: %v", err)
	}

	for name := range tagged {
		if contractCoverageExclusions[name] {
			continue
		}
		golden := filepath.Join("testdata", "json", pascalToSnake(name)+".golden")
		if _, err := os.Stat(golden); err != nil {
			t.Errorf("%s carries a json tag but has no golden at %s (add a TestJSONGoldens row, or add it to contractCoverageExclusions with a reason)", name, golden)
		}
	}
}

// jsonTaggedExportedStructs parses every non-test .go file directly under
// dir and returns the set of exported struct type names with at least one
// field carrying a `json:` struct tag. Walks the directory itself (rather
// than go/parser.ParseDir, deprecated since Go 1.25) so it stays plain
// stdlib with no new dependency on golang.org/x/tools/go/packages.
func jsonTaggedExportedStructs(dir string) (map[string]bool, error) {
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
				if !ok || !ts.Name.IsExported() {
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

// pascalToSnake converts a PascalCase Go identifier (e.g. "SourceWarning")
// to the snake_case golden basename convention TestJSONGoldens' table uses
// (e.g. "source_warning"). Every type name on the contract today is plain
// PascalCase with no acronym runs, so this simple per-letter rule matches
// every recorded golden filename exactly.
func pascalToSnake(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

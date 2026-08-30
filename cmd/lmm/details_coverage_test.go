package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// detailsCoverage names, for every type in internal/core or cmd/lmm that
// implements the unnamed `Details() any` extension point (jsonout.go's
// errorDetails switches on it; it is the --json error envelope's "details"
// field, Ruling 3), the test function that pins its exact wire shape.
// Unit P's M7 review finding: a Details() type with no golden/test is
// undetectable drift in the --json contract - a future change to what it
// returns, or to its json tags, would pass silently. This is the ratchet:
// TestDetailsTypesAreCovered walks both packages' go/ast for the method,
// and a type with no entry here (or an entry naming a test function that no
// longer exists) fails the build.
var detailsCoverage = map[string]string{
	"core.ConflictError":     "TestReportError_JSON_ConflictError",
	"gameDetectPartialError": "TestDoGameDetect_JSON_PartialApplyFailure_EnvelopeNamesPersistedGames",
	"profileWarningsError":   "TestDoProfileSwitch_JSON_FatalAfterWarning_EnvelopeCarriesWarnings",
	"sourceValidationError":  "TestReportError_JSON_SourceValidationError",
}

// TestDetailsTypesAreCovered enforces detailsCoverage: every type found to
// implement Details() any must have a map entry, that entry's test
// function must actually exist, and every map entry must correspond to a
// type that was actually found (no stale entries once a type is deleted).
func TestDetailsTypesAreCovered(t *testing.T) {
	coreTypes, err := detailsTypes("../../internal/core", "core")
	if err != nil {
		t.Fatalf("parsing internal/core: %v", err)
	}
	cliTypes, err := detailsTypes(".", "")
	if err != nil {
		t.Fatalf("parsing cmd/lmm: %v", err)
	}
	found := map[string]bool{}
	for _, name := range append(coreTypes, cliTypes...) {
		found[name] = true
	}

	testFuncs, err := testFunctionNames(".")
	if err != nil {
		t.Fatalf("parsing cmd/lmm test files: %v", err)
	}

	for name := range found {
		fn, ok := detailsCoverage[name]
		if !ok {
			t.Errorf("%s implements Details() any but has no entry in detailsCoverage (add a named test pinning its wire shape)", name)
			continue
		}
		if !testFuncs[fn] {
			t.Errorf("%s's covering test %s no longer exists in cmd/lmm's test files", name, fn)
		}
	}
	for name := range detailsCoverage {
		if !found[name] {
			t.Errorf("detailsCoverage has a stale entry %q: no type implementing Details() any was found for it - delete the entry", name)
		}
	}
}

// detailsTypes parses every non-test .go file directly under dir and
// returns the receiver type name (prefixed with "pkg." when prefix is
// non-empty) of every method named Details with no parameters and a single
// `any` result.
func detailsTypes(dir, prefix string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	var found []string
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
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Details" || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			if fn.Type.Params != nil && len(fn.Type.Params.List) != 0 {
				continue
			}
			if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			resultIdent, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
			if !ok || resultIdent.Name != "any" {
				continue
			}

			recvType := fn.Recv.List[0].Type
			if star, ok := recvType.(*ast.StarExpr); ok {
				recvType = star.X
			}
			ident, ok := recvType.(*ast.Ident)
			if !ok {
				continue
			}
			if prefix != "" {
				found = append(found, prefix+"."+ident.Name)
			} else {
				found = append(found, ident.Name)
			}
		}
	}
	return found, nil
}

// testFunctionNames parses every _test.go file directly under dir and
// returns the set of top-level (non-method) func names declared there.
func testFunctionNames(dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	names := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			names[fn.Name.Name] = true
		}
	}
	return names, nil
}

package domain_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// allowedDomainImports is the one module package internal/domain may
// import besides the standard library: internal/fomod, a stdlib-only leaf
// whose choice types ARE the profile's recorded FOMOD choices (#614). The
// domain stays the vocabulary every layer imports, so it must never reach
// anything that could import it back, nor a third-party module.
var allowedDomainImports = map[string]bool{
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod": true,
}

// TestDomainImportsOnlyTheStandardLibraryAndFomod is the ratchet: every
// non-test file in this package imports the standard library (a path
// whose first element has no dot) or an allow-listed module package.
func TestDomainImportsOnlyTheStandardLibraryAndFomod(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(path, "/")
			if !strings.Contains(first, ".") || allowedDomainImports[path] {
				continue
			}
			t.Errorf("%s imports %s: internal/domain may import only the standard library and internal/fomod", name, path)
		}
	}
}

package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestExportedIdentifiersHaveDocComments is internal/core's doc-comment
// ratchet (v2 Phase 3 Task 20, #305) extended to internal/app: every
// exported type, func, method (on an exported receiver type), const, and
// var declared in this package's non-test files must carry a leading doc
// comment. A comment directly above a `const (`/`var (` block's opening
// paren covers every name in that block that has no comment of its own
// (accepted Go style for a run of related one-liners) — but each FuncDecl
// is its own declaration, so a comment above the first of a run of
// one-liner methods does NOT cover the rest of the run; each needs its own
// line.
func TestExportedIdentifiersHaveDocComments(t *testing.T) {
	offenders, err := undocumentedExports(".")
	if err != nil {
		t.Fatalf("parsing internal/app: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("%d exported identifier(s) missing a doc comment:\n%s", len(offenders), strings.Join(offenders, "\n"))
	}
}

// undocumentedExports parses every non-test .go file directly under dir and
// returns "file:line: Name (kind)" for each exported type, func-or-method,
// const, or var with no leading doc comment, sorted for a stable diff.
// Walks the directory itself (rather than parser.ParseDir, deprecated since
// Go 1.25) so it stays plain stdlib with no new dependency on
// golang.org/x/tools/go/packages.
func undocumentedExports(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	var offenders []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}

		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv != nil && !recvExported(d.Recv) {
					continue
				}
				if d.Doc == nil {
					offenders = append(offenders, formatOffender(fset, d.Pos(), d.Name.Name, "func"))
				}
			case *ast.GenDecl:
				if d.Tok != token.CONST && d.Tok != token.VAR && d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						if s.Doc == nil && d.Doc == nil {
							offenders = append(offenders, formatOffender(fset, s.Pos(), s.Name.Name, "type"))
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if !n.IsExported() {
								continue
							}
							if s.Doc == nil && d.Doc == nil {
								offenders = append(offenders, formatOffender(fset, n.Pos(), n.Name, valueKind(d.Tok)))
							}
						}
					}
				}
			}
		}
	}
	sort.Strings(offenders)
	return offenders, nil
}

// recvExported reports whether recv's type (stripping a leading pointer
// star) is an exported identifier.
func recvExported(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	t := recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	ident, ok := t.(*ast.Ident)
	return ok && ident.IsExported()
}

func valueKind(tok token.Token) string {
	if tok == token.CONST {
		return "const"
	}
	return "var"
}

func formatOffender(fset *token.FileSet, pos token.Pos, name, kind string) string {
	p := fset.Position(pos)
	return fmt.Sprintf("%s:%d: %s (%s)", filepath.Base(p.Filename), p.Line, name, kind)
}

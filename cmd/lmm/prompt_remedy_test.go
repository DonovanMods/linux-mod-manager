package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// remedyFuncs are the two ways a prompt names the flag that would have
// answered it without a human: the --json envelope's own wording, and (since
// #385) the EOF rendering that must say the same thing.
var remedyFuncs = map[string]int{
	"confirmationRequiredVia": 0, // remedy is the only argument
	"promptReadError":         1, // ...the second, after the error
}

// TestPromptRemediesComeFromNamedConstants is P1b review F9. #385's whole
// claim is that a prompt's plain-mode remedy and its --json remedy "cannot
// drift" - but they were duplicated string literals at three pairs of call
// sites (helpers.go, install.go, auth.go), matching only because someone
// typed them the same way twice. Nothing stopped an edit to one.
//
// The rule is structural rather than a string comparison: a literal at
// either call site is refused, so the two halves of a pair can only be the
// same named constant, and changing the remedy changes both.
func TestPromptRemediesComeFromNamedConstants(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, parseErr, "parsing %s", name)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			argIdx, watched := remedyFuncs[fn.Name]
			if !watched || len(call.Args) <= argIdx {
				return true
			}
			seen++
			_, isLiteral := call.Args[argIdx].(*ast.BasicLit)
			assert.False(t, isLiteral,
				"%s: %s's remedy is a string literal - use a named constant so the plain-mode and --json renderings cannot drift (#385)",
				fset.Position(call.Pos()), fn.Name)
			return true
		})
	}
	assert.GreaterOrEqual(t, seen, 6, "expected to find the remedy call sites at all")
}

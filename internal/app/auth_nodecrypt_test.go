package app

// The status path's #79 invariant, guarded over the SOURCE rather than
// over behaviour. Track D1 removed the per-source svc.GetSourceToken call
// from AuthStatus - that call decrypts a credential, and a report is not
// allowed to hold one - and replaced it with a single ListSourceTokens
// listing whose db.TokenInfo has no field a key could travel in. #356's
// precedence fix is exactly the kind of change that puts it back: naming a
// shadowed stored key is one careless line away from "just read the token
// and mask it", which is what the first cut of #356 did.
//
// A counting fake cannot express this - core.Service is concrete, so a
// test cannot intercept the call - and no assertion over the RESULT can
// either: a decrypt that is performed and then discarded looks identical
// on the wire. The guard is therefore syntactic, in the same spirit as the
// package's other go/ast ratchets.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthStatusNeverReadsTheDecryptingTokenAPI(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "auth_status.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	calls := map[string]int{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			calls[sel.Sel.Name]++
		}
		return true
	})

	assert.Zero(t, calls["GetSourceToken"],
		"auth_status.go must not call the decrypting per-source read (#79): a status surface "+
			"describes a stored credential from ListSourceTokens' listing, it never holds one")
	assert.Equal(t, 1, calls["ListSourceTokens"],
		"one non-decrypting listing answers the whole report - a per-row read would be both a "+
			"decrypt per source and N queries")
}

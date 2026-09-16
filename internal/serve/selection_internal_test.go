package serve

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveSelection_BuildsNoGameRows (#413 re-review L5): a game row
// resolves its adapter, which for a game with no adapter key can stat the
// install directory - so building every game's row on every scoped request
// made each request touch every game's disk, and one game on a hung network
// share hung them all. The rows are only the 404's list of valid choices,
// so they are built there and nowhere else.
func TestResolveSelection_BuildsNoGameRows(t *testing.T) {
	s, game := newLiveFixtureServer(t)
	var built atomic.Int32
	s.gameRows = func(ctx context.Context) ([]core.GameListEntry, error) {
		built.Add(1)
		return s.svc.ListGameEntries(ctx)
	}

	get := func(query string) int {
		return doAPI(s, http.MethodGet, "/api/v1/mods"+query, "").Code
	}

	require.Equal(t, http.StatusOK, get("?game="+game.ID))
	assert.Zero(t, built.Load(), "a resolved selection needs no game rows")

	require.Equal(t, http.StatusNotFound, get("?game=nope"))
	assert.Equal(t, int32(1), built.Load(), "the 404 lists the valid games")
}

// rowBuilders are the calls that build game rows: the Service method, and
// the Server seam production wires to it.
var rowBuilders = map[string]bool{"ListGameEntries": true, "gameRows": true}

// TestResolveSelection_NeverReachesARowBuilder is L5's ratchet (#413 final
// review F7). The seam test above counts builds made THROUGH s.gameRows, so
// restoring the literal pre-fix line - s.svc.ListGameEntries(ctx), straight
// from resolveSelection - passed it. This reads the code instead:
// resolveSelection, and every function or method of this package it calls,
// directly or through another, may not call a row builder.
//
// Functions are keyed by name, receiver ignored, so a name shared by two
// types pulls both bodies in - the ratchet errs strict, never lenient.
func TestResolveSelection_NeverReachesARowBuilder(t *testing.T) {
	bodies := packageFuncBodies(t)
	require.Contains(t, bodies, "resolveSelection")

	var offenders []string
	visited := map[string]bool{}
	queue := []string{"resolveSelection"}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if visited[name] {
			continue
		}
		visited[name] = true
		for _, body := range bodies[name] {
			ast.Inspect(body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var callee string
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					callee = fn.Name
				case *ast.SelectorExpr:
					callee = fn.Sel.Name
				default:
					return true
				}
				if rowBuilders[callee] {
					offenders = append(offenders, name+" calls "+callee)
				}
				if _, local := bodies[callee]; local {
					queue = append(queue, callee)
				}
				return true
			})
		}
	}
	sort.Strings(offenders)
	assert.Empty(t, offenders, "resolveSelection runs on every scoped request and must build no game rows")
}

// packageFuncBodies parses this package's non-test files and returns every
// function and method body, keyed by name.
func packageFuncBodies(t *testing.T) map[string][]*ast.BlockStmt {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	bodies := map[string][]*ast.BlockStmt{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		require.NoError(t, err)
		file, err := parser.ParseFile(fset, name, src, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				bodies[fn.Name.Name] = append(bodies[fn.Name.Name], fn.Body)
			}
		}
	}
	return bodies
}

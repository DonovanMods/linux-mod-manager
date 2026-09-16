package core_test

// The structural half of I4 (#411; #413 fix round 4, F8).
//
// TestEveryPlanChecksTheAdapterPrecondition proves each Plan it LISTS
// refuses, which catches an exemption only in a Plan someone already put
// in its table. Two holes were left: a new Plan built on the ungated
// removal helpers, and an Apply switched to checkRemovalPlanFresh - a
// one-word change that the whole core suite let through. The two
// ratchets here close them by reading the package's source.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ungatedSnapshotHelpers build or check a freshness snapshot WITHOUT the
// adapter's say.
var ungatedSnapshotHelpers = map[string]bool{
	"removalSnapshotOf":     true,
	"checkRemovalPlanFresh": true,
}

// ungatedSnapshotCallers is the complete set of functions allowed to
// reference them: the two removal flows' Plan and Apply, and the two
// helpers' own gated wrapper and shared body.
var ungatedSnapshotCallers = map[string]bool{
	"PlanPurge":             true,
	"ApplyPurge":            true,
	"PlanUninstall":         true,
	"ApplyUninstall":        true,
	"snapshotOf":            true,
	"checkRemovalPlanFresh": true,
}

// parseCorePackage parses internal/core's non-test files.
func parseCorePackage(t *testing.T) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, err)
		files = append(files, f)
	}
	require.NotEmpty(t, files)
	return files
}

// ungatedHelperReferences maps each function in files that references an
// ungated snapshot helper - called, or taken as a value - to the helpers it
// names. A reference outside any function is keyed "<package scope>".
func ungatedHelperReferences(files []*ast.File) map[string][]string {
	refs := map[string][]string{}
	record := func(owner string, n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			var name string
			switch x := n.(type) {
			case *ast.Ident:
				name = x.Name
			case *ast.SelectorExpr:
				name = x.Sel.Name
			default:
				return true
			}
			if ungatedSnapshotHelpers[name] {
				refs[owner] = append(refs[owner], name)
			}
			return true
		})
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				record("<package scope>", decl)
				continue
			}
			if fn.Body != nil {
				record(fn.Name.Name, fn.Body)
			}
		}
	}
	return refs
}

// TestOnlyTheRemovalFlowsSkipTheAdapterPrecondition: the ungated snapshot
// helpers are referenced from the removal flows and nowhere else.
func TestOnlyTheRemovalFlowsSkipTheAdapterPrecondition(t *testing.T) {
	refs := ungatedHelperReferences(parseCorePackage(t))
	require.NotEmpty(t, refs, "the walk found the helpers at all")
	var offenders []string
	for owner, names := range refs {
		if !ungatedSnapshotCallers[owner] {
			offenders = append(offenders, owner+" -> "+strings.Join(names, ", "))
		}
	}
	sort.Strings(offenders)
	assert.Empty(t, offenders,
		"only purge and uninstall may skip the adapter's precondition (removalSnapshotOf's doc comment says why); "+
			"a deploying Plan or Apply builds and checks its snapshot with snapshotOf/checkPlanFresh")
}

// servicePlanMethods lists every exported Plan* method on *Service.
func servicePlanMethods(files []*ast.File) []string {
	var names []string
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || !fn.Name.IsExported() || !strings.HasPrefix(fn.Name.Name, "Plan") {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); ok && id.Name == "Service" {
				names = append(names, fn.Name.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// TestEveryServicePlanIsGatedOrExempt: every Plan* method is either in the
// gated table - so TestEveryPlanChecksTheAdapterPrecondition proves it
// refuses - or a named exemption, and neither list names a Plan that no
// longer exists.
func TestEveryServicePlanIsGatedOrExempt(t *testing.T) {
	methods := servicePlanMethods(parseCorePackage(t))
	require.NotEmpty(t, methods)

	listed := map[string]bool{}
	for _, p := range gatedPlans {
		assert.False(t, listed[p.name], "%s is listed twice", p.name)
		listed[p.name] = true
	}
	for name := range adapterExemptPlans {
		assert.False(t, listed[name], "%s is both gated and exempt", name)
		listed[name] = true
	}

	exists := map[string]bool{}
	for _, m := range methods {
		exists[m] = true
		assert.True(t, listed[m], "Service.%s is in neither gatedPlans nor adapterExemptPlans: add it to gatedPlans, or justify an exemption", m)
	}
	for name := range listed {
		assert.True(t, exists[name], "%s is listed but Service has no such method", name)
	}
}

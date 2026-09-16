package core_test

// The structural half of I4 (#411; #413 fix round 4, F8).
//
// TestEveryPlanChecksTheAdapterPrecondition proves each Plan it LISTS
// refuses, which catches an exemption only in a Plan someone already put
// in its table. Two holes were left: a new Plan built on the ungated
// removal helpers, and an Apply switched to checkRemovalPlanFresh - a
// one-word change that the whole core suite let through. The ratchets here
// close them by reading the package's source - and, since #413's follow-up,
// cover the single-step deploys (`lmm mod enable`, verify --fix's repairs,
// the merged-artifact resync) that have no Plan to carry the check at all.

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

// The single-step half of the ratchet (#413). A Plan/Apply pair carries the
// adapter's say in its snapshot; `lmm mod enable`, verify --fix's repairs and
// the merged-artifact resync every mutation ends with have no snapshot, so
// each asks the adapter itself before it deploys. What the build enforces:
// no exported Service method reaches a deploy without a gate on the way.

// installerDeployMethods are the Installer methods that write a mod's files
// into the game directory.
var installerDeployMethods = map[string]bool{
	"Install":             true,
	"Replace":             true,
	"ReplaceForUpdate":    true,
	"ReplaceWithCaches":   true,
	"ReplaceWithOldCache": true,
}

// nonInstallerReceivers are receivers known to own a method sharing one of
// those names - source.Registry.Replace - so a call on them is not a deploy.
// Any other receiver the walk cannot place fails the build.
var nonInstallerReceivers = map[string]bool{
	"registry": true,
}

// deployGates are the calls that give the adapter its say: the precondition
// check itself, the Plan/Apply snapshot helpers that run it, and the
// single-step gates (deployRefusal, and verify's refuseDeploy).
var deployGates = map[string]bool{
	"checkAdapterPreconditions": true,
	"snapshotOf":                true,
	"currentInstalledSnapshot":  true,
	"checkPlanFresh":            true,
	"deployRefusal":             true,
	"refuseDeploy":              true,
}

// ungatedDeployEntries are exported Service methods allowed to reach a
// deploy with no gate on the way, each with why. None is, today.
var ungatedDeployEntries = map[string]string{}

// coreCall is one call expression inside a package core function.
type coreCall struct {
	name    string // the called function or method name
	recv    string // the receiver's last identifier, for a method call
	method  bool
	pos     token.Pos
	blocks  []ast.Node // the enclosing blocks, outermost first
	isGate  bool
	guarded bool // a gate call precedes it in a block that encloses it
}

// coreFunc is one function declared in package core.
type coreFunc struct {
	key   string // Recv.Name, or .Name for a plain function
	recv  string
	calls []*coreCall
}

// enclosingBlocks lists the block-like nodes in stack, outermost first.
func enclosingBlocks(stack []ast.Node) []ast.Node {
	var out []ast.Node
	for _, n := range stack {
		switch n.(type) {
		case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause:
			out = append(out, n)
		}
	}
	return out
}

// receiverName is the last identifier of a method call's receiver.
func receiverName(x ast.Expr) string {
	switch e := x.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.CallExpr:
		return receiverName(e.Fun)
	}
	return ""
}

// coreCallGraph indexes every function in files by key, and by name for
// resolving a call - by name only, so a call reaches every function of
// that name (an over-approximation a ratchet can afford).
func coreCallGraph(files []*ast.File) (map[string]*coreFunc, map[string][]string) {
	funcs := map[string]*coreFunc{}
	byName := map[string][]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if !ok || d.Body == nil {
				continue
			}
			recv := ""
			if d.Recv != nil && len(d.Recv.List) == 1 {
				r := d.Recv.List[0].Type
				if s, ok := r.(*ast.StarExpr); ok {
					r = s.X
				}
				if id, ok := r.(*ast.Ident); ok {
					recv = id.Name
				}
			}
			fn := &coreFunc{key: recv + "." + d.Name.Name, recv: recv}
			var stack []ast.Node
			ast.Inspect(d.Body, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				c := &coreCall{pos: call.Pos(), blocks: enclosingBlocks(stack)}
				switch fe := call.Fun.(type) {
				case *ast.Ident:
					c.name = fe.Name
				case *ast.SelectorExpr:
					c.name, c.recv, c.method = fe.Sel.Name, receiverName(fe.X), true
				default:
					return true
				}
				c.isGate = deployGates[c.name]
				fn.calls = append(fn.calls, c)
				return true
			})
			markGuarded(fn)
			funcs[fn.key] = fn
			byName[d.Name.Name] = append(byName[d.Name.Name], fn.key)
		}
	}
	return funcs, byName
}

// markGuarded sets guarded on every call a gate call precedes from a block
// that encloses it - structured code's "the check ran on the way here".
func markGuarded(fn *coreFunc) {
	for _, c := range fn.calls {
		for _, g := range fn.calls {
			if !g.isGate || g.pos >= c.pos || len(g.blocks) == 0 {
				continue
			}
			inner := g.blocks[len(g.blocks)-1]
			for _, b := range c.blocks {
				if b == inner {
					c.guarded = true
				}
			}
		}
	}
}

// deployKind reports whether c writes into the game directory, and whether
// the walk could tell.
func deployKind(fn *coreFunc, c *coreCall) (deploy, unclear bool) {
	if fn.recv == "Installer" {
		return false, false // the primitives themselves
	}
	switch {
	case !c.method && c.name == "applyProfileOverrides":
		return true, false
	case c.method && c.name == "Deploy":
		return true, false // a linker, bypassing the Installer
	case c.method && installerDeployMethods[c.name]:
		if strings.Contains(strings.ToLower(c.recv), "installer") {
			return true, false
		}
		return false, !nonInstallerReceivers[c.recv]
	}
	return false, false
}

// ungatedDeployPaths walks every call path from root that no gate guards,
// returning one line per deploy it reaches.
func ungatedDeployPaths(root string, funcs map[string]*coreFunc, byName map[string][]string) []string {
	var found []string
	seen := map[string]bool{}
	var walk func(key string, path []string)
	walk = func(key string, path []string) {
		fn := funcs[key]
		if fn == nil || seen[key] || fn.recv == "Installer" {
			return
		}
		seen[key] = true
		path = append(path, key)
		for _, c := range fn.calls {
			if c.guarded || c.isGate {
				continue
			}
			if deploy, _ := deployKind(fn, c); deploy {
				found = append(found, strings.Join(path, " -> ")+" -> "+c.recv+"."+c.name)
				continue
			}
			for _, callee := range byName[c.name] {
				walk(callee, path)
			}
		}
	}
	walk(root, nil)
	return found
}

// TestEverySingleStepDeployIsGated: from every exported Service method, no
// call path reaches a deploy - an Installer's Install or Replace, a
// linker's Deploy, the profile's config overrides - without a gate call
// before it on the way. A new single-step flow that deploys, or a gate
// removed from an existing one, fails here.
func TestEverySingleStepDeployIsGated(t *testing.T) {
	funcs, byName := coreCallGraph(parseCorePackage(t))

	var unclear []string
	sites := map[string]bool{}
	for _, fn := range funcs {
		for _, c := range fn.calls {
			deploy, u := deployKind(fn, c)
			if u {
				unclear = append(unclear, fmt.Sprintf("%s calls %s.%s", fn.key, c.recv, c.name))
			}
			if deploy {
				sites[fn.key] = true
			}
		}
	}
	// The walk must see the deploys it exists to police, or it proves
	// nothing by staying quiet.
	for _, known := range []string{"Service.enableMod", "verifyRun.relinkDeployedRow", "verifyRun.repairUnlinkedLoaderFiles",
		"verifyRun.repairMisplacedLoaderDeploy", "Service.syncMergedPak", "Service.reconcilePakManifests", "Service.deployProfile"} {
		assert.True(t, sites[known], "the walk no longer sees %s's deploy", known)
	}
	sort.Strings(unclear)
	assert.Empty(t, unclear, "the walk cannot tell whether these are an Installer's deploys: name the receiver installer, or add it to nonInstallerReceivers")

	used := map[string]bool{}
	var offenders []string
	for key, fn := range funcs {
		name := strings.TrimPrefix(key, "Service.")
		if fn.recv != "Service" || !ast.IsExported(name) {
			continue
		}
		paths := ungatedDeployPaths(key, funcs, byName)
		if len(paths) == 0 {
			continue
		}
		if _, ok := ungatedDeployEntries[name]; ok {
			used[name] = true
			continue
		}
		offenders = append(offenders, paths...)
	}
	sort.Strings(offenders)
	assert.Empty(t, offenders,
		"a deploy reached with no adapter check on the way: call deployRefusal (or refuseDeploy, in verify) before it, "+
			"or list the entry point in ungatedDeployEntries with why")
	for name := range ungatedDeployEntries {
		assert.True(t, used[name], "ungatedDeployEntries names %s, which reaches no ungated deploy", name)
	}
}

// TestEveryDeployGateAsksTheAdapter: a name in deployGates is a gate only if
// it reaches the precondition check - so a gate that stops asking cannot
// keep the ratchet above quiet.
func TestEveryDeployGateAsksTheAdapter(t *testing.T) {
	funcs, byName := coreCallGraph(parseCorePackage(t))
	var reaches func(key string, seen map[string]bool) bool
	reaches = func(key string, seen map[string]bool) bool {
		fn := funcs[key]
		if fn == nil || seen[key] {
			return false
		}
		seen[key] = true
		for _, c := range fn.calls {
			if c.name == "checkAdapterPreconditions" {
				return true
			}
			for _, callee := range byName[c.name] {
				if reaches(callee, seen) {
					return true
				}
			}
		}
		return false
	}
	for gate := range deployGates {
		if gate == "checkAdapterPreconditions" {
			require.NotEmpty(t, byName[gate])
			continue
		}
		keys := byName[gate]
		require.NotEmpty(t, keys, "deployGates names %s, which package core does not declare", gate)
		for _, key := range keys {
			assert.True(t, reaches(key, map[string]bool{}), "%s is listed as a gate but never reaches checkAdapterPreconditions", key)
		}
	}
}

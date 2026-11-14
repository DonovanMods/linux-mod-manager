package core

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The plugin sort (#616, design docs/plans/2026-11-10-collections-design.md
// §5 "The sort"): a deterministic topological sort of the plugins a game
// will load. It is pure - every fact it needs is handed in - so it is
// table-tested without a disk, and the deploy-side code that gathers those
// facts (plugins_sync.go) stays separate from the ordering rules.
//
// Hard edges: a plugin loads after each of its masters, and masters load
// before non-masters (except a non-master some master itself depends on,
// libloadorder's documented exception). The game's implicit plugins always
// load first and are never placed. Soft edges, from the profile's rules:
// a plugin's `after` and `req` lists, and group order (a group's plugins
// after every plugin of each group it loads after), a group edge that
// would close a cycle being skipped, as LOOT skips one.
//
// Ties go, in order, to: the profile's order (a plugin it lists before
// one it does not list yet - a newly installed plugin loads last, after
// its own masters, as in LOOT, MO2 and Vortex: owner ruling, #616), then
// the order of the mods that ship the plugins, then the case-insensitive
// name. A plugin the user placed therefore stays where they put it unless
// an edge forbids it.

// pluginNode is one plugin the sort places.
type pluginNode struct {
	// name is the plugin's file name, as it is spelled on disk.
	name string
	// class is its header's answer (ClassifyPlugin).
	class PluginClass
	// masters are the masters its header names.
	masters []string
	// managed reports that lmm deployed it; false is an external plugin
	// the current list file names.
	managed bool
	// modIndex is the index in the profile's mods of the mod that ships
	// it, for the tiebreak; -1 when no mod does.
	modIndex int
	// active is whether the written list activates it.
	active bool
}

// pluginKey is the case-insensitive identity of a plugin name: the game
// matches plugin names the way Windows matches file names.
func pluginKey(name string) string { return strings.ToLower(name) }

// PluginEdgeMaster, PluginEdgeMastersFirst, PluginEdgeAfter,
// PluginEdgeReq and PluginEdgeGroup are PluginOrderEdge.Reason's values:
// why From must load before To.
const (
	// PluginEdgeMaster: From is one of To's masters.
	PluginEdgeMaster = "master"
	// PluginEdgeMastersFirst: From is a master and To is not.
	PluginEdgeMastersFirst = "masters_first"
	// PluginEdgeAfter: a profile rule says To loads after From.
	PluginEdgeAfter = "rule_after"
	// PluginEdgeReq: a profile rule says To requires From.
	PluginEdgeReq = "rule_req"
	// PluginEdgeGroup: To's rule group loads after From's.
	PluginEdgeGroup = "group"
)

// PluginOrderEdge is one ordering constraint: From loads before To, for
// Reason.
type PluginOrderEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
	// Group names the groups of a PluginEdgeGroup edge ("A > B": To's
	// group loads after From's).
	Group string `json:"group,omitempty"`
}

// describe is the edge in words.
func (e PluginOrderEdge) describe() string {
	switch e.Reason {
	case PluginEdgeMaster:
		return fmt.Sprintf("%s is a master of %s", e.From, e.To)
	case PluginEdgeMastersFirst:
		return fmt.Sprintf("%s is a master, so it loads before %s", e.From, e.To)
	case PluginEdgeAfter:
		return fmt.Sprintf("a rule loads %s after %s", e.To, e.From)
	case PluginEdgeReq:
		return fmt.Sprintf("a rule says %s requires %s", e.To, e.From)
	case PluginEdgeGroup:
		return fmt.Sprintf("%s's group loads after %s's (%s)", e.To, e.From, e.Group)
	default:
		return fmt.Sprintf("%s loads before %s", e.From, e.To)
	}
}

// PluginOrderCycleError refuses a plugin order whose constraints form a
// cycle: no order satisfies them all. Edges is the cycle, each edge's To
// the next one's From, the last edge's To the first one's From. Nothing is
// written: the game keeps the list it had.
type PluginOrderCycleError struct {
	Edges []PluginOrderEdge `json:"cycle"`
}

// Error names the cycle, edge by edge.
func (e *PluginOrderCycleError) Error() string {
	parts := make([]string, len(e.Edges))
	for i, edge := range e.Edges {
		parts[i] = edge.describe()
	}
	return "the plugin order has a cycle, so no order satisfies it: " + strings.Join(parts, "; ") +
		" - change a rule in the profile's plugins: so one of these can give way"
}

// Details is the cycle, for the --json error envelope.
func (e *PluginOrderCycleError) Details() any {
	return struct {
		Cycle []PluginOrderEdge `json:"cycle"`
	}{e.Edges}
}

// sortRequest is everything the sort reads.
type sortRequest struct {
	// nodes are the plugins to place: the managed ones and, in the final
	// pass, the external ones.
	nodes []pluginNode
	// loadedFirst are the implicit plugins present, by key: they load
	// before everything placed, so an edge from one is always satisfied.
	loadedFirst map[string]bool
	// order is the profile's plugin order.
	order []domain.PluginEntry
	// rules are the profile's plugin rules.
	rules domain.PluginRules
	// preferred, when set, replaces the order/mod/name tiebreak with a
	// rank per node key: the final pass's merged order.
	preferred map[string]int
}

// sortPlugins returns req's nodes in load order, or a
// *PluginOrderCycleError.
func sortPlugins(req sortRequest) ([]pluginNode, error) {
	n := len(req.nodes)
	index := make(map[string]int, n)
	for i, node := range req.nodes {
		index[pluginKey(node.name)] = i
	}
	g := newPluginGraph(n)

	// Hard edges: each plugin after its masters.
	for i, node := range req.nodes {
		for _, m := range node.masters {
			if j, ok := index[pluginKey(m)]; ok && j != i {
				g.add(j, i, PluginOrderEdge{From: req.nodes[j].name, To: node.name, Reason: PluginEdgeMaster})
			}
		}
	}
	// Masters first: a non-master waits for every master, unless a master
	// depends on it through its masters (then it is needed first, and goes
	// where that master needs it). Only header masters count: a rule can
	// never put a non-master before a master, since the game loads masters
	// first whatever the list says, so such a rule is a cycle to report.
	neededByMaster := make([]bool, n)
	for i, node := range req.nodes {
		if node.class.Master {
			g.markMasterAncestors(i, neededByMaster)
		}
	}

	// The profile's per-plugin rules.
	for _, rule := range req.rules.Plugins {
		i, ok := index[pluginKey(rule.Name)]
		if !ok {
			continue
		}
		for _, a := range rule.After {
			if j, ok := index[pluginKey(a)]; ok && j != i {
				g.add(j, i, PluginOrderEdge{From: req.nodes[j].name, To: req.nodes[i].name, Reason: PluginEdgeAfter})
			}
		}
		for _, a := range rule.Req {
			if j, ok := index[pluginKey(a)]; ok && j != i {
				g.add(j, i, PluginOrderEdge{From: req.nodes[j].name, To: req.nodes[i].name, Reason: PluginEdgeReq})
			}
		}
	}
	// Group edges, each skipped when it would close a cycle.
	addGroupEdges(g, req, neededByMaster)

	rank := tiebreak(req)
	placed := make([]bool, n)
	indeg := slices.Clone(g.indeg)
	mastersLeft := 0
	for _, node := range req.nodes {
		if node.class.Master {
			mastersLeft++
		}
	}
	available := func(i int) bool {
		if placed[i] || indeg[i] > 0 {
			return false
		}
		node := req.nodes[i]
		return node.class.Master || mastersLeft == 0 || neededByMaster[i]
	}
	out := make([]pluginNode, 0, n)
	for len(out) < n {
		best := -1
		for i := range req.nodes {
			if available(i) && (best < 0 || rank(i, best) < 0) {
				best = i
			}
		}
		if best < 0 {
			return nil, &PluginOrderCycleError{Edges: g.cycle(req.nodes, placed, mastersLeft > 0, neededByMaster)}
		}
		placed[best] = true
		if req.nodes[best].class.Master {
			mastersLeft--
		}
		for _, j := range g.succ[best] {
			indeg[j]--
		}
		out = append(out, req.nodes[best])
	}
	return out, nil
}

// tiebreak is the comparison that picks among plugins whose constraints
// all allow them next: negative when i goes before j.
func tiebreak(req sortRequest) func(i, j int) int {
	if req.preferred != nil {
		return func(i, j int) int {
			a, b := req.nodes[i], req.nodes[j]
			return cmp.Or(
				cmp.Compare(req.preferred[pluginKey(a.name)], req.preferred[pluginKey(b.name)]),
				strings.Compare(a.name, b.name),
			)
		}
	}
	pos := make(map[string]int, len(req.order))
	for i, e := range req.order {
		if _, seen := pos[pluginKey(e.Name)]; !seen {
			pos[pluginKey(e.Name)] = i
		}
	}
	return func(i, j int) int {
		a, b := req.nodes[i], req.nodes[j]
		pa, ina := pos[pluginKey(a.name)]
		pb, inb := pos[pluginKey(b.name)]
		// A plugin the order does not list yet goes after every listed
		// plugin its edges allow it to follow - it loads last, as a newly
		// added plugin does in LOOT, MO2 and Vortex, so it wins conflicts
		// (owner ruling, #616). Its masters still load before it.
		if ina != inb {
			if ina {
				return -1
			}
			return 1
		}
		if ina {
			if c := cmp.Compare(pa, pb); c != 0 {
				return c
			}
		}
		return cmp.Or(
			cmp.Compare(modRank(a), modRank(b)),
			strings.Compare(pluginKey(a.name), pluginKey(b.name)),
			strings.Compare(a.name, b.name),
		)
	}
}

// modRank orders plugins by their mod's position, a plugin no mod ships
// last.
func modRank(n pluginNode) int {
	if n.modIndex < 0 {
		return int(^uint(0) >> 1)
	}
	return n.modIndex
}

// addGroupEdges adds req's group edges to g: every plugin of a group loads
// after every plugin of each group that group loads after, directly or
// through other groups. An edge that would close a cycle with the edges
// already there is skipped (LOOT's rule), so a rule group can never make
// an order impossible on its own.
func addGroupEdges(g *pluginGraph, req sortRequest, neededByMaster []bool) {
	if len(req.rules.Groups) == 0 {
		return
	}
	groupOf := make(map[string]string, len(req.rules.Plugins))
	for _, rule := range req.rules.Plugins {
		if rule.Group != "" {
			groupOf[pluginKey(rule.Name)] = rule.Group
		}
	}
	after := make(map[string][]string, len(req.rules.Groups))
	var names []string
	for _, grp := range req.rules.Groups {
		if _, seen := after[grp.Name]; !seen {
			names = append(names, grp.Name)
		}
		after[grp.Name] = append(after[grp.Name], grp.After...)
	}
	members := make(map[string][]int)
	for i, node := range req.nodes {
		grp := groupOf[pluginKey(node.name)]
		if grp == "" {
			grp = domain.PluginGroupDefault
		}
		members[grp] = append(members[grp], i)
	}
	for _, later := range names {
		for _, earlier := range groupsBefore(later, after) {
			for _, j := range members[earlier] {
				for _, i := range members[later] {
					if i == j || g.reaches(req.nodes, neededByMaster, i, j) {
						continue
					}
					g.add(j, i, PluginOrderEdge{From: req.nodes[j].name, To: req.nodes[i].name, Reason: PluginEdgeGroup, Group: later + " > " + earlier})
				}
			}
		}
	}
}

// groupsBefore is every group `later` loads after, directly or through
// other groups, nearest first; a cycle among groups is followed once.
func groupsBefore(later string, after map[string][]string) []string {
	var out []string
	seen := map[string]bool{later: true}
	queue := slices.Clone(after[later])
	for len(queue) > 0 {
		grp := queue[0]
		queue = queue[1:]
		if seen[grp] {
			continue
		}
		seen[grp] = true
		out = append(out, grp)
		queue = append(queue, after[grp]...)
	}
	return out
}

// pluginGraph is the sort's constraint graph over node indexes.
type pluginGraph struct {
	succ  [][]int
	pred  [][]int
	indeg []int
	why   map[[2]int]PluginOrderEdge
}

func newPluginGraph(n int) *pluginGraph {
	return &pluginGraph{succ: make([][]int, n), pred: make([][]int, n), indeg: make([]int, n), why: map[[2]int]PluginOrderEdge{}}
}

// add records that from loads before to; a repeated edge keeps its first
// reason.
func (g *pluginGraph) add(from, to int, why PluginOrderEdge) {
	if _, ok := g.why[[2]int{from, to}]; ok {
		return
	}
	g.why[[2]int{from, to}] = why
	g.succ[from] = append(g.succ[from], to)
	g.pred[to] = append(g.pred[to], from)
	g.indeg[to]++
}

// reaches reports whether a path of constraints leads from `from` to `to`,
// counting masters-first as an edge from every master to every non-master
// no master needs.
func (g *pluginGraph) reaches(nodes []pluginNode, neededByMaster []bool, from, to int) bool {
	seen := make([]bool, len(g.succ))
	stack := []int{from}
	gatedPushed := false
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if i == to {
			return true
		}
		if seen[i] {
			continue
		}
		seen[i] = true
		stack = append(stack, g.succ[i]...)
		if nodes[i].class.Master && !gatedPushed {
			gatedPushed = true
			for j, other := range nodes {
				if !other.class.Master && !neededByMaster[j] {
					stack = append(stack, j)
				}
			}
		}
	}
	return false
}

// markMasterAncestors marks every node with a path of master edges to i:
// a master of i, a master of that master, and so on.
func (g *pluginGraph) markMasterAncestors(i int, marks []bool) {
	stack := []int{i}
	for len(stack) > 0 {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, j := range g.pred[k] {
			if marks[j] || g.why[[2]int{j, k}].Reason != PluginEdgeMaster {
				continue
			}
			marks[j] = true
			stack = append(stack, j)
		}
	}
}

// cycle finds a cycle among the nodes not placed, counting masters-first
// as an edge from each unplaced master to each gated non-master. The sort
// only stops early when one exists.
func (g *pluginGraph) cycle(nodes []pluginNode, placed []bool, mastersLeft bool, neededByMaster []bool) []PluginOrderEdge {
	next := func(i int) []PluginOrderEdge {
		var out []PluginOrderEdge
		for _, j := range g.succ[i] {
			if !placed[j] {
				out = append(out, g.why[[2]int{i, j}])
			}
		}
		if nodes[i].class.Master && mastersLeft {
			for j, other := range nodes {
				if !placed[j] && !other.class.Master && !neededByMaster[j] {
					out = append(out, PluginOrderEdge{From: nodes[i].name, To: other.name, Reason: PluginEdgeMastersFirst})
				}
			}
		}
		return out
	}
	idx := make(map[string]int, len(nodes))
	for i, node := range nodes {
		idx[node.name] = i
	}
	// Search for a cycle with a depth-first walk over unplaced nodes.
	const (
		white = iota
		grey
		black
	)
	color := make([]int, len(nodes))
	var stack []PluginOrderEdge
	var found []PluginOrderEdge
	var visit func(i int) bool
	visit = func(i int) bool {
		color[i] = grey
		for _, e := range next(i) {
			j := idx[e.To]
			switch color[j] {
			case grey:
				// The cycle is the stack from the edge leaving j, plus e.
				k := len(stack)
				for k > 0 && stack[k-1].From != e.To {
					k--
				}
				if k > 0 {
					found = append(slices.Clone(stack[k-1:]), e)
				} else {
					found = []PluginOrderEdge{e}
				}
				return true
			case white:
				stack = append(stack, e)
				if visit(j) {
					return true
				}
				stack = stack[:len(stack)-1]
			}
		}
		color[i] = black
		return false
	}
	for i := range nodes {
		if !placed[i] && color[i] == white && visit(i) {
			return found
		}
	}
	return nil
}

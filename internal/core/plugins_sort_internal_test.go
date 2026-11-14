package core

import (
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func esp(name string, mod int, masters ...string) pluginNode {
	return pluginNode{name: name, managed: true, modIndex: mod, masters: masters, active: true}
}

func esm(name string, mod int, masters ...string) pluginNode {
	n := esp(name, mod, masters...)
	n.class.Master = true
	return n
}

func names(nodes []pluginNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.name
	}
	return out
}

func entries(list ...string) []domain.PluginEntry {
	out := make([]domain.PluginEntry, len(list))
	for i, n := range list {
		out[i] = domain.PluginEntry{Name: n, Enabled: true}
	}
	return out
}

func TestSortPlugins_NoOrderFollowsModOrderThenName(t *testing.T) {
	got, err := sortPlugins(sortRequest{nodes: []pluginNode{
		esp("b.esp", 1), esp("A.esp", 1), esp("z.esp", 0),
	}})
	require.NoError(t, err)
	assert.Equal(t, []string{"z.esp", "A.esp", "b.esp"}, names(got), "mod order first, then the case-insensitive name")
}

func TestSortPlugins_MastersFirstAndAfterTheirMasters(t *testing.T) {
	got, err := sortPlugins(sortRequest{nodes: []pluginNode{
		esp("Patch.esp", 0, "Mod.esp", "Base.esm"),
		esp("Mod.esp", 1),
		esm("Base.esm", 2, "Skyrim.esm"),
	}, loadedFirst: map[string]bool{"skyrim.esm": true}})
	require.NoError(t, err)
	assert.Equal(t, []string{"Base.esm", "Mod.esp", "Patch.esp"}, names(got))
}

func TestSortPlugins_ProfileOrderWinsUnlessAnEdgeForbids(t *testing.T) {
	got, err := sortPlugins(sortRequest{
		nodes: []pluginNode{esp("a.esp", 0), esp("b.esp", 1), esp("c.esp", 2, "b.esp")},
		order: entries("c.esp", "b.esp", "a.esp"),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"b.esp", "c.esp", "a.esp"}, names(got),
		"c needs its master b first; a keeps its place after them")
}

// A plugin the profile's order does not list yet loads last (owner ruling,
// #616: as in LOOT, MO2 and Vortex), so a freshly added mod wins its
// conflicts; new ones among themselves go by mod order.
func TestSortPlugins_NewPluginLoadsLast(t *testing.T) {
	got, err := sortPlugins(sortRequest{
		nodes: []pluginNode{esp("a.esp", 0), esp("b.esp", 1), esp("new.esp", 2, "a.esp"), esp("fresh.esp", 3)},
		order: entries("a.esp", "b.esp"),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.esp", "b.esp", "new.esp", "fresh.esp"}, names(got))
}

// Its edges still hold: a new master loads with the masters, and a new
// plugin a listed one needs as a master loads before it.
func TestSortPlugins_NewPluginStillObeysItsEdges(t *testing.T) {
	got, err := sortPlugins(sortRequest{
		nodes: []pluginNode{esp("a.esp", 0), esp("b.esp", 1, "needed.esp"), esp("needed.esp", 2), esm("new.esm", 3)},
		order: entries("a.esp", "b.esp"),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"new.esm", "a.esp", "needed.esp", "b.esp"}, names(got))
}

func TestSortPlugins_OrderMatchIsCaseInsensitive(t *testing.T) {
	got, err := sortPlugins(sortRequest{
		nodes: []pluginNode{esp("A.esp", 0), esp("B.esp", 1)},
		order: entries("b.ESP", "a.esp"),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"B.esp", "A.esp"}, names(got))
}

func TestSortPlugins_RulesAfterAndReq(t *testing.T) {
	got, err := sortPlugins(sortRequest{
		nodes: []pluginNode{esp("a.esp", 0), esp("b.esp", 1), esp("c.esp", 2)},
		order: entries("a.esp", "b.esp", "c.esp"),
		rules: domain.PluginRules{Plugins: []domain.PluginRule{
			{Name: "a.esp", After: []string{"c.esp"}},
			{Name: "b.esp", Req: []string{"a.esp"}, After: []string{"absent.esp"}},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"c.esp", "a.esp", "b.esp"}, names(got))
}

func TestSortPlugins_GroupsLoadAfterTheirGroups(t *testing.T) {
	got, err := sortPlugins(sortRequest{
		nodes: []pluginNode{esp("late.esp", 0), esp("x.esp", 1), esp("y.esp", 2)},
		rules: domain.PluginRules{
			Groups:  []domain.PluginGroup{{Name: "Late Loaders", After: []string{"default"}}},
			Plugins: []domain.PluginRule{{Name: "late.esp", Group: "Late Loaders"}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"x.esp", "y.esp", "late.esp"}, names(got))
}

func TestSortPlugins_GroupEdgeThatWouldCycleIsSkipped(t *testing.T) {
	// late.esp is a master of y.esp, so the group edge y.esp -> late.esp
	// would close a cycle and is skipped.
	got, err := sortPlugins(sortRequest{
		nodes: []pluginNode{esp("late.esp", 0), esp("y.esp", 1, "late.esp")},
		rules: domain.PluginRules{
			Groups:  []domain.PluginGroup{{Name: "late", After: []string{"default"}}},
			Plugins: []domain.PluginRule{{Name: "late.esp", Group: "late"}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"late.esp", "y.esp"}, names(got))
}

func TestSortPlugins_MasterDependingOnANonMasterGetsItFirst(t *testing.T) {
	got, err := sortPlugins(sortRequest{nodes: []pluginNode{
		esp("other.esp", 0), esm("odd.esm", 1, "needed.esp"), esp("needed.esp", 2),
	}})
	require.NoError(t, err)
	assert.Equal(t, []string{"needed.esp", "odd.esm", "other.esp"}, names(got))
}

func TestSortPlugins_CycleIsAPluginOrderCycleError(t *testing.T) {
	_, err := sortPlugins(sortRequest{
		nodes: []pluginNode{esp("a.esp", 0, "b.esp"), esp("b.esp", 1), esp("c.esp", 2)},
		rules: domain.PluginRules{Plugins: []domain.PluginRule{{Name: "b.esp", After: []string{"a.esp"}}}},
	})
	var cycle *PluginOrderCycleError
	require.True(t, errors.As(err, &cycle), "got %v", err)
	assert.ElementsMatch(t, []PluginOrderEdge{
		{From: "b.esp", To: "a.esp", Reason: PluginEdgeMaster},
		{From: "a.esp", To: "b.esp", Reason: PluginEdgeAfter},
	}, cycle.Edges)
	assert.Contains(t, err.Error(), "b.esp is a master of a.esp")
	assert.Contains(t, err.Error(), "a rule loads b.esp after a.esp")
}

func TestSortPlugins_RuleAgainstMastersFirstIsACycle(t *testing.T) {
	_, err := sortPlugins(sortRequest{
		nodes: []pluginNode{esm("m.esm", 0), esp("p.esp", 1)},
		rules: domain.PluginRules{Plugins: []domain.PluginRule{{Name: "m.esm", After: []string{"p.esp"}}}},
	})
	var cycle *PluginOrderCycleError
	require.True(t, errors.As(err, &cycle), "got %v", err)
	assert.ElementsMatch(t, []PluginOrderEdge{
		{From: "p.esp", To: "m.esm", Reason: PluginEdgeAfter},
		{From: "m.esm", To: "p.esp", Reason: PluginEdgeMastersFirst},
	}, cycle.Edges)
}

func TestSortPlugins_PreferredRankReplacesTheTiebreak(t *testing.T) {
	got, err := sortPlugins(sortRequest{
		nodes:     []pluginNode{esp("a.esp", 0), esp("b.esp", 1), esp("c.esp", 2, "a.esp")},
		preferred: map[string]int{"c.esp": 0, "b.esp": 1, "a.esp": 2},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"b.esp", "a.esp", "c.esp"}, names(got))
}

func TestSortPlugins_IsDeterministic(t *testing.T) {
	nodes := []pluginNode{esp("d.esp", 3), esm("c.esm", 2), esp("b.esp", 1, "c.esm"), esp("a.esp", 0)}
	first, err := sortPlugins(sortRequest{nodes: nodes})
	require.NoError(t, err)
	for range 20 {
		shuffled := []pluginNode{nodes[2], nodes[0], nodes[3], nodes[1]}
		again, err := sortPlugins(sortRequest{nodes: shuffled})
		require.NoError(t, err)
		require.Equal(t, names(first), names(again))
	}
}

func TestPluginOrderCycleError_Details(t *testing.T) {
	err := &PluginOrderCycleError{Edges: []PluginOrderEdge{{From: "a", To: "b", Reason: PluginEdgeAfter}}}
	assert.Equal(t, struct {
		Cycle []PluginOrderEdge `json:"cycle"`
	}{err.Edges}, err.Details())
}

func TestSortPlugins_GroupEdgeAgainstMastersFirstIsSkipped(t *testing.T) {
	got, err := sortPlugins(sortRequest{
		nodes: []pluginNode{esm("late.esm", 0), esp("x.esp", 1)},
		rules: domain.PluginRules{
			Groups:  []domain.PluginGroup{{Name: "late", After: []string{"default"}}},
			Plugins: []domain.PluginRule{{Name: "late.esm", Group: "late"}},
		},
	})
	require.NoError(t, err, "a group cannot move a master after a non-master; its edge gives way")
	assert.Equal(t, []string{"late.esm", "x.esp"}, names(got))
}

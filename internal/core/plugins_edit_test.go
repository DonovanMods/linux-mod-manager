package core_test

// plugins_edit_test.go drives #617's plugin report and edits - PluginOrder,
// PlanPluginEdit/ApplyPluginEdit, ProfilePluginRules - through the real
// bethesda adapter: the effective order with each plugin's flags, needed
// masters and what placed it, and every edit landing in the profile's
// plugins: block and the game's Plugins.txt.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fourPlugins is a fixture deploying Base.esm (a master), Patch.esp
// (Base.esm's patch), A.esp and B.esp, in that mod order.
func fourPlugins(t *testing.T) *pluginFixture {
	t.Helper()
	f := newPluginFixture(t)
	f.mod(t, "base", map[string][]byte{"Base.esm": pluginBytes(1, "Skyrim.esm")})
	f.mod(t, "patch", map[string][]byte{"Patch.esp": pluginBytes(0, "Skyrim.esm", "Base.esm")})
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.mod(t, "b", map[string][]byte{"B.esp": pluginBytes(0)})
	return f
}

func (f *pluginFixture) profilePlugins(t *testing.T) domain.ProfilePlugins {
	t.Helper()
	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	return profile.Plugins
}

func (f *pluginFixture) edit(t *testing.T, edit core.PluginEdit) (*core.PluginEditPlan, *core.PluginEditResult) {
	t.Helper()
	plan, err := f.svc.PlanPluginEdit(context.Background(), f.game, "default", edit)
	require.NoError(t, err)
	res, err := f.svc.ApplyPluginEdit(context.Background(), f.game, plan)
	require.NoError(t, err)
	return plan, res
}

func names(entries []core.PluginOrderEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}

func entryNamed(t *testing.T, report *core.PluginOrderReport, name string) core.PluginOrderEntry {
	t.Helper()
	for _, e := range report.Plugins {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("%s is not in the report: %v", name, names(report.Plugins))
	return core.PluginOrderEntry{}
}

func TestPluginOrder_ReportsTheEffectiveOrder(t *testing.T) {
	f := fourPlugins(t)
	f.setPlugins(t, domain.ProfilePlugins{
		Order: []domain.PluginEntry{{Name: "B.esp", Enabled: true}, {Name: "A.esp", Enabled: true}, {Name: "Patch.esp", Enabled: true}, {Name: "Gone.esp", Enabled: false}},
		Rules: domain.PluginRules{Plugins: []domain.PluginRule{{Name: "A.esp", After: []string{"Patch.esp"}}}},
	})
	require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, "Ext.esp"), pluginBytes(0), 0o644))
	require.NoError(t, os.MkdirAll(f.listDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte("*Ext.esp\n"), 0o644))
	f.deploy(t)

	report, err := f.svc.PluginOrder(context.Background(), f.game, "default")
	require.NoError(t, err)
	assert.True(t, report.Active)
	assert.True(t, report.Sorted)
	assert.Equal(t, filepath.Join(f.listDir, "Plugins.txt"), report.ListFile)
	assert.Equal(t, []string{"Skyrim.esm", "Base.esm", "Ext.esp", "B.esp", "Patch.esp", "A.esp"}, names(report.Plugins),
		"masters load first; the external plugin keeps its place among the rest")

	skyrim := entryNamed(t, report, "Skyrim.esm")
	assert.Equal(t, core.PluginPlacementImplicit, skyrim.Placement)
	assert.True(t, skyrim.Master)
	assert.Equal(t, 1, skyrim.Position)

	ext := entryNamed(t, report, "Ext.esp")
	assert.Equal(t, core.PluginPlacementExternal, ext.Placement)
	assert.False(t, ext.Managed)
	assert.True(t, ext.Active)

	base := entryNamed(t, report, "Base.esm")
	assert.True(t, base.Master)
	assert.True(t, base.Managed)
	assert.Equal(t, "src", base.SourceID)
	assert.Equal(t, "base", base.ModID)
	assert.Equal(t, "base", base.ModName)
	assert.Equal(t, core.PluginPlacementNew, base.Placement, "the order does not list it")

	patch := entryNamed(t, report, "Patch.esp")
	assert.Equal(t, []string{"Skyrim.esm", "Base.esm"}, patch.Masters)
	assert.Empty(t, patch.MissingMasters)
	assert.Equal(t, core.PluginPlacementMaster, patch.Placement, "its master holds it after Base.esm, which the order does not list")

	a := entryNamed(t, report, "A.esp")
	assert.Equal(t, core.PluginPlacementRule, a.Placement)
	assert.Equal(t, []core.PluginOrderEdge{{From: "Patch.esp", To: "A.esp", Reason: core.PluginEdgeAfter}}, a.Constraints)

	assert.Equal(t, core.PluginPlacementOrder, entryNamed(t, report, "B.esp").Placement)
	assert.Equal(t, []domain.PluginEntry{{Name: "Gone.esp", Enabled: false}}, report.NotDeployed)
	assert.Equal(t, "A.esp", report.Rules.Plugins[0].Name)
}

func TestPluginOrder_MissingMastersAndCycle(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0, "Absent.esm")})
	f.mod(t, "b", map[string][]byte{"B.esp": pluginBytes(0)})
	f.deploy(t)

	report, err := f.svc.PluginOrder(context.Background(), f.game, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"Absent.esm"}, entryNamed(t, report, "A.esp").MissingMasters)
	require.NotEmpty(t, report.Findings)
	assert.Equal(t, core.PluginFindingMissingMaster, report.Findings[0].Code)

	f.setPlugins(t, domain.ProfilePlugins{Rules: domain.PluginRules{Plugins: []domain.PluginRule{
		{Name: "A.esp", After: []string{"B.esp"}}, {Name: "B.esp", After: []string{"A.esp"}},
	}}})
	report, err = f.svc.PluginOrder(context.Background(), f.game, "default")
	require.NoError(t, err)
	assert.False(t, report.Sorted)
	assert.Len(t, report.Plugins, 3, "a cycle still lists the plugins, in the profile's order")
	assert.True(t, hasFinding(report.Findings, core.PluginFindingCycle))
}

func hasFinding(findings []core.PluginFinding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

func TestPluginOrder_RefusesAGameWithNoPluginList(t *testing.T) {
	f := newPluginFixture(t)
	f.game.Adapter = ""
	_, err := f.svc.PluginOrder(context.Background(), f.game, "default")
	require.ErrorIs(t, err, core.ErrNoPluginList)
	_, err = f.svc.PlanPluginEdit(context.Background(), f.game, "default", core.PluginEdit{Op: core.PluginOpSort})
	require.ErrorIs(t, err, core.ErrNoPluginList)
}

func TestPluginEdit_DisableWritesTheProfileAndTheList(t *testing.T) {
	f := fourPlugins(t)
	f.deploy(t)
	require.Equal(t, "*Base.esm\n*Patch.esp\n*A.esp\n*B.esp\n", f.list(t))

	plan, res := f.edit(t, core.PluginEdit{Op: core.PluginOpDisable, Plugins: []string{"a.ESP"}})
	assert.Equal(t, []string{"A.esp: disabled"}, plan.Changes)
	assert.True(t, plan.Active)
	assert.False(t, plan.NoChanges)
	assert.False(t, entryNamed(t, plan.Order, "A.esp").Active)
	assert.True(t, res.Changed)
	require.NotNil(t, res.List)
	assert.Equal(t, core.PluginListWritten, res.List.Status)

	// Every unlisted plugin is recorded with it, in the effective order, so
	// nothing moves.
	assert.Equal(t, []domain.PluginEntry{
		{Name: "Base.esm", Enabled: true}, {Name: "Patch.esp", Enabled: true},
		{Name: "A.esp", Enabled: false}, {Name: "B.esp", Enabled: true},
	}, f.profilePlugins(t).Order)
	assert.Equal(t, "*Base.esm\n*Patch.esp\nA.esp\n*B.esp\n", f.list(t))

	plan, res = f.edit(t, core.PluginEdit{Op: core.PluginOpEnable, Plugins: []string{"A.esp"}})
	assert.Equal(t, []string{"A.esp: enabled"}, plan.Changes)
	assert.True(t, res.Changed)
	assert.Equal(t, "*Base.esm\n*Patch.esp\n*A.esp\n*B.esp\n", f.list(t))

	plan, res = f.edit(t, core.PluginEdit{Op: core.PluginOpEnable, Plugins: []string{"A.esp"}})
	assert.True(t, plan.NoChanges)
	assert.Equal(t, []string{"A.esp is already enabled"}, plan.Notes)
	assert.False(t, res.Changed)
}

func TestPluginEdit_RefusesPluginsThatAreNotLmms(t *testing.T) {
	f := fourPlugins(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, "Ext.esp"), pluginBytes(0), 0o644))
	require.NoError(t, os.MkdirAll(f.listDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte("*Ext.esp\n"), 0o644))
	f.deploy(t)
	ctx := context.Background()

	for _, tc := range []struct {
		edit core.PluginEdit
		want error
	}{
		{core.PluginEdit{Op: core.PluginOpDisable, Plugins: []string{"Ext.esp"}}, core.ErrPluginNotOwned},
		{core.PluginEdit{Op: core.PluginOpDisable, Plugins: []string{"Skyrim.esm"}}, core.ErrPluginNotOwned},
		{core.PluginEdit{Op: core.PluginOpEnable, Plugins: []string{"Nope.esp"}}, core.ErrPluginNotFound},
		{core.PluginEdit{Op: core.PluginOpMove, Plugin: "A.esp", Before: "Ext.esp"}, core.ErrPluginNotOwned},
		{core.PluginEdit{Op: core.PluginOpMove, Plugin: "A.esp"}, core.ErrInvalidPluginEdit},
		{core.PluginEdit{Op: core.PluginOpMove, Plugin: "A.esp", Before: "B.esp", To: "last"}, core.ErrInvalidPluginEdit},
		{core.PluginEdit{Op: core.PluginOpRuleAdd, Rule: domain.PluginRule{Name: "A.esp"}}, core.ErrInvalidPluginEdit},
		{core.PluginEdit{Op: core.PluginOpRuleAdd, Rule: domain.PluginRule{Name: "A.esp", Group: "Late"}}, core.ErrPluginRuleNotFound},
		{core.PluginEdit{Op: core.PluginOpRuleRemove, Rule: domain.PluginRule{Name: "A.esp"}}, core.ErrPluginRuleNotFound},
		{core.PluginEdit{Op: core.PluginOpGroupRemove, Group: domain.PluginGroup{Name: "Late"}}, core.ErrPluginRuleNotFound},
		{core.PluginEdit{Op: "shuffle"}, core.ErrInvalidPluginEdit},
	} {
		_, err := f.svc.PlanPluginEdit(ctx, f.game, "default", tc.edit)
		assert.ErrorIs(t, err, tc.want, "%+v", tc.edit)
	}
}

func TestPluginEdit_MoveRecordsTheEffectiveOrder(t *testing.T) {
	f := fourPlugins(t)
	f.deploy(t)

	plan, _ := f.edit(t, core.PluginEdit{Op: core.PluginOpMove, Plugin: "B.esp", Before: "A.esp"})
	assert.Equal(t, []string{"B.esp: moved before A.esp"}, plan.Changes)
	assert.Empty(t, plan.Notes)
	assert.Equal(t, []string{"Skyrim.esm", "Base.esm", "Patch.esp", "B.esp", "A.esp"}, names(plan.Order.Plugins))
	assert.Equal(t, "*Base.esm\n*Patch.esp\n*B.esp\n*A.esp\n", f.list(t))
	assert.Len(t, f.profilePlugins(t).Order, 4)

	f.edit(t, core.PluginEdit{Op: core.PluginOpMove, Plugin: "B.esp", To: core.PluginMoveLast})
	assert.Equal(t, "*Base.esm\n*Patch.esp\n*A.esp\n*B.esp\n", f.list(t))

	// A master holds Patch.esp after Base.esm: the move is recorded, and the
	// plan says why it does not take effect.
	plan, _ = f.edit(t, core.PluginEdit{Op: core.PluginOpMove, Plugin: "Patch.esp", To: core.PluginMoveFirst})
	assert.Equal(t, "Patch.esp", f.profilePlugins(t).Order[0].Name)
	require.Len(t, plan.Notes, 1, "its master holds it after Base.esm")
	assert.Contains(t, plan.Notes[0], "Base.esm is a master of Patch.esp")
	plan, err := f.svc.PlanPluginEdit(context.Background(), f.game, "default", core.PluginEdit{Op: core.PluginOpMove, Plugin: "Patch.esp", Before: "Base.esm"})
	require.NoError(t, err)
	require.Len(t, plan.Notes, 1)
	assert.Contains(t, plan.Notes[0], "Patch.esp cannot load there")
	assert.Contains(t, plan.Notes[0], "Base.esm is a master of Patch.esp")
	assert.Equal(t, "*Base.esm\n*Patch.esp\n*A.esp\n*B.esp\n", f.list(t))
}

func TestPluginEdit_SortRecordsNewPluginsAndKeepsUndeployedEntries(t *testing.T) {
	f := fourPlugins(t)
	f.setPlugins(t, domain.ProfilePlugins{Order: []domain.PluginEntry{
		{Name: "B.esp", Enabled: true}, {Name: "Gone.esp", Enabled: false}, {Name: "A.esp", Enabled: true},
	}})
	f.deploy(t)

	plan, res := f.edit(t, core.PluginEdit{Op: core.PluginOpSort})
	assert.Contains(t, plan.Changes, "Base.esm: added to the profile's order where it loads")
	assert.Contains(t, plan.Changes, "Patch.esp: added to the profile's order where it loads")
	assert.True(t, res.Changed)
	assert.Equal(t, []domain.PluginEntry{
		{Name: "Base.esm", Enabled: true}, {Name: "B.esp", Enabled: true}, {Name: "Gone.esp", Enabled: false},
		{Name: "A.esp", Enabled: true}, {Name: "Patch.esp", Enabled: true},
	}, f.profilePlugins(t).Order, "the undeployed entry stays after the plugin it followed")
	assert.Equal(t, "*Base.esm\n*B.esp\n*A.esp\n*Patch.esp\n", f.list(t))

	plan, res = f.edit(t, core.PluginEdit{Op: core.PluginOpSort})
	assert.True(t, plan.NoChanges)
	assert.False(t, res.Changed)
	require.NotNil(t, res.List)
	assert.Equal(t, core.PluginListUnchanged, res.List.Status)
}

func TestPluginEdit_RulesAndGroups(t *testing.T) {
	f := fourPlugins(t)
	f.deploy(t)
	ctx := context.Background()

	plan, _ := f.edit(t, core.PluginEdit{Op: core.PluginOpRuleAdd, Rule: domain.PluginRule{Name: "A.esp", After: []string{"B.esp"}, Req: []string{"Later.esp"}}})
	assert.Equal(t, []string{"A.esp: loads after B.esp", "A.esp: requires Later.esp"}, plan.Changes)
	assert.Equal(t, []string{"Later.esp is not deployed: the rule applies once it is"}, plan.Notes)
	assert.Equal(t, "*Base.esm\n*Patch.esp\n*B.esp\n*A.esp\n", f.list(t))
	assert.Equal(t, core.PluginPlacementRule, entryNamed(t, plan.Order, "A.esp").Placement)

	// A rule closing a cycle with a master edge is refused before anything
	// is written.
	_, err := f.svc.PlanPluginEdit(ctx, f.game, "default", core.PluginEdit{Op: core.PluginOpRuleAdd, Rule: domain.PluginRule{Name: "Base.esm", After: []string{"Patch.esp"}}})
	var cycle *core.PluginOrderCycleError
	require.ErrorAs(t, err, &cycle)

	f.edit(t, core.PluginEdit{Op: core.PluginOpGroupAdd, Group: domain.PluginGroup{Name: "Early"}})
	f.edit(t, core.PluginEdit{Op: core.PluginOpGroupAdd, Group: domain.PluginGroup{Name: "Late", After: []string{"default"}}})
	plan, _ = f.edit(t, core.PluginEdit{Op: core.PluginOpRuleAdd, Rule: domain.PluginRule{Name: "Patch.esp", Group: "Late"}})
	assert.Equal(t, []string{"Patch.esp: in group Late"}, plan.Changes)
	assert.Equal(t, "*Base.esm\n*B.esp\n*A.esp\n*Patch.esp\n", f.list(t))

	_, err = f.svc.PlanPluginEdit(ctx, f.game, "default", core.PluginEdit{Op: core.PluginOpGroupRemove, Group: domain.PluginGroup{Name: "Late"}})
	require.ErrorIs(t, err, core.ErrInvalidPluginEdit, "a group a rule uses is not removed")

	rules, err := f.svc.ProfilePluginRules(ctx, f.game, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.PluginRules{
		Groups:  []domain.PluginGroup{{Name: "Early"}, {Name: "Late", After: []string{"default"}}},
		Plugins: []domain.PluginRule{{Name: "A.esp", After: []string{"B.esp"}, Req: []string{"Later.esp"}}, {Name: "Patch.esp", Group: "Late"}},
	}, rules.Rules)

	f.edit(t, core.PluginEdit{Op: core.PluginOpRuleRemove, Rule: domain.PluginRule{Name: "Patch.esp"}, ClearGroup: true})
	f.edit(t, core.PluginEdit{Op: core.PluginOpGroupRemove, Group: domain.PluginGroup{Name: "Late"}})
	plan, _ = f.edit(t, core.PluginEdit{Op: core.PluginOpRuleRemove, Rule: domain.PluginRule{Name: "A.esp", Req: []string{"later.esp"}}})
	assert.Equal(t, []string{"A.esp: no longer requires Later.esp"}, plan.Changes)
	f.edit(t, core.PluginEdit{Op: core.PluginOpRuleRemove, Rule: domain.PluginRule{Name: "A.esp"}})
	assert.Equal(t, domain.PluginRules{Groups: []domain.PluginGroup{{Name: "Early"}}}, f.profilePlugins(t).Rules)
}

func TestPluginEdit_StalePlanIsRefused(t *testing.T) {
	f := fourPlugins(t)
	f.deploy(t)
	ctx := context.Background()
	plan, err := f.svc.PlanPluginEdit(ctx, f.game, "default", core.PluginEdit{Op: core.PluginOpDisable, Plugins: []string{"A.esp"}})
	require.NoError(t, err)
	f.setPlugins(t, domain.ProfilePlugins{Order: []domain.PluginEntry{{Name: "B.esp", Enabled: false}}})
	_, err = f.svc.ApplyPluginEdit(ctx, f.game, plan)
	require.ErrorIs(t, err, core.ErrStalePlan)
}

func TestPluginEdit_InactiveProfileEditsTheProfileOnly(t *testing.T) {
	f := fourPlugins(t)
	f.deploy(t)
	ctx := context.Background()
	pm := f.svc.NewProfileManager()
	_, err := pm.Create(ctx, f.game.ID, "other")
	require.NoError(t, err)
	other, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "other")
	require.NoError(t, err)
	other.Plugins = domain.ProfilePlugins{Order: []domain.PluginEntry{{Name: "A.esp", Enabled: true}, {Name: "B.esp", Enabled: true}}}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), other))
	before := f.list(t)

	report, err := f.svc.PluginOrder(ctx, f.game, "other")
	require.NoError(t, err)
	assert.False(t, report.Active)
	assert.Empty(t, report.Plugins)
	assert.Equal(t, other.Plugins.Order, report.NotDeployed)

	_, err = f.svc.PlanPluginEdit(ctx, f.game, "other", core.PluginEdit{Op: core.PluginOpSort})
	require.ErrorIs(t, err, core.ErrInvalidPluginEdit)
	_, err = f.svc.PlanPluginEdit(ctx, f.game, "other", core.PluginEdit{Op: core.PluginOpDisable, Plugins: []string{"Base.esm"}})
	require.ErrorIs(t, err, core.ErrPluginNotFound, "only the active profile's deployed plugins are known")

	plan, err := f.svc.PlanPluginEdit(ctx, f.game, "other", core.PluginEdit{Op: core.PluginOpMove, Plugin: "B.esp", Before: "A.esp"})
	require.NoError(t, err)
	assert.False(t, plan.Active)
	res, err := f.svc.ApplyPluginEdit(ctx, f.game, plan)
	require.NoError(t, err)
	assert.Nil(t, res.List, "an inactive profile's list is not the game's")
	got, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "other")
	require.NoError(t, err)
	assert.Equal(t, []domain.PluginEntry{{Name: "B.esp", Enabled: true}, {Name: "A.esp", Enabled: true}}, got.Plugins.Order)
	assert.Equal(t, before, f.list(t))
}

// A rule about an external plugin (an in-game Creation, a hand install)
// would move it, and lmm leaves an external plugin's place to the game: such
// a rule is refused, as a move is. An external plugin may still be another
// rule's anchor.
func TestPluginEdit_RuleSubjectMustBeLmms(t *testing.T) {
	f := fourPlugins(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, "Ext.esp"), pluginBytes(0), 0o644))
	require.NoError(t, os.MkdirAll(f.listDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte("*Ext.esp\n"), 0o644))
	f.deploy(t)
	ctx := context.Background()

	for _, rule := range []domain.PluginRule{
		{Name: "Ext.esp", After: []string{"B.esp"}},
		{Name: "ext.ESP", Req: []string{"A.esp"}},
		{Name: "Skyrim.esm", After: []string{"A.esp"}},
	} {
		_, err := f.svc.PlanPluginEdit(ctx, f.game, "default", core.PluginEdit{Op: core.PluginOpRuleAdd, Rule: rule})
		assert.ErrorIs(t, err, core.ErrPluginNotOwned, "%+v", rule)
	}

	plan, _ := f.edit(t, core.PluginEdit{Op: core.PluginOpRuleAdd, Rule: domain.PluginRule{Name: "A.esp", After: []string{"Ext.esp"}}})
	assert.Equal(t, []string{"A.esp: loads after Ext.esp"}, plan.Changes, "an external plugin can be a rule's anchor")
}

// Switching a plugin the order lists but no mod deploys now records the
// state, and says when it takes effect.
func TestPluginEdit_SwitchingAnUndeployedPluginSaysWhenItTakesEffect(t *testing.T) {
	f := fourPlugins(t)
	f.setPlugins(t, domain.ProfilePlugins{Order: []domain.PluginEntry{{Name: "Gone.esp", Enabled: false}}})
	f.deploy(t)

	plan, _ := f.edit(t, core.PluginEdit{Op: core.PluginOpEnable, Plugins: []string{"Gone.esp"}})
	assert.Equal(t, []string{"Gone.esp: enabled"}, plan.Changes)
	assert.Equal(t, []string{"Gone.esp is not deployed now; it takes effect when it is"}, plan.Notes)
}

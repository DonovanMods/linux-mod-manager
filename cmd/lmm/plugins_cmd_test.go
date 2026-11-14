package main

// #617 at the CLI: `lmm plugins list|enable|disable|move|sort` and `lmm
// plugins rules ...` over core's report and its plugin-edit Plan/Apply
// pair, with --dry-run writing nothing and every --json document pinned.

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

// deployedPluginsCLI is setupPluginsCLI with A.esp (needing B.esm) and
// B.esm, deployed.
func deployedPluginsCLI(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	svc, game, listDir := setupPluginsCLI(t, map[string]map[string]string{
		"a": {"A.esp": cliPlugin(0, "B.esm")},
		"b": {"B.esm": cliPlugin(1)},
	})
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "Skyrim.esm"), []byte(cliPlugin(1)), 0o644))
	_ = captureStdout(t, func() error { return doDeploy(context.Background(), svc, game, nil) })
	resetPluginFlags(t)
	return svc, game, listDir
}

// resetPluginFlags puts every `lmm plugins` flag back to its default for
// the test, and after it.
func resetPluginFlags(t *testing.T) {
	t.Helper()
	setFlag(t, &pluginsProfile, "")
	setFlag(t, &pluginsDryRun, false)
	setFlag(t, &pluginsBefore, "")
	setFlag(t, &pluginsAfter, "")
	setFlag(t, &pluginsFirst, false)
	setFlag(t, &pluginsLast, false)
	setFlag(t, &pluginsRuleAfter, nil)
	setFlag(t, &pluginsRuleReq, nil)
	setFlag(t, &pluginsRuleGroup, "")
	setFlag(t, &pluginsClearGroup, false)
	setFlag(t, &pluginsGroupAfter, nil)
}

func readList(t *testing.T, listDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(listDir, "Plugins.txt"))
	require.NoError(t, err)
	return string(data)
}

func TestCLI_PluginsListPrintsTheEffectiveOrder(t *testing.T) {
	ctx := context.Background()
	svc, game, listDir := deployedPluginsCLI(t)

	stdout := captureStdout(t, func() error { return doPluginsList(ctx, svc, game) })
	assert.Contains(t, stdout, "Plugin order for "+game.ID+" (profile default)\n")
	assert.Contains(t, stdout, "Plugins.txt: "+filepath.Join(listDir, "Plugins.txt")+"\n")
	assert.Regexp(t, `1\s+\*\s+Skyrim\.esm\s+M\s+game`, stdout)
	assert.Regexp(t, `2\s+\*\s+B\.esm\s+M\s+new\s+b`, stdout)
	assert.Regexp(t, `3\s+\*\s+A\.esp\s+master: B\.esm\s+B\.esm\s+a`, stdout)
}

func TestCLI_PluginsDisableDryRunThenApply(t *testing.T) {
	ctx := context.Background()
	svc, game, listDir := deployedPluginsCLI(t)
	before := readList(t, listDir)

	setFlag(t, &pluginsDryRun, true)
	stdout := captureStdout(t, func() error {
		return doPluginEdit(ctx, svc, game, core.PluginEdit{Op: core.PluginOpDisable, Plugins: []string{"B.esm"}})
	})
	assert.Contains(t, stdout, "Plugin edit for profile default (dry run)\n")
	assert.Contains(t, stdout, "  ~ B.esm: disabled\n")
	assert.Contains(t, stdout, "B.esm (missing)", "the plan shows the master A.esp would lose")
	assert.Equal(t, before, readList(t, listDir), "a dry run writes nothing")

	setFlag(t, &pluginsDryRun, false)
	stdout, stderr, err := captureStdoutStderrErr(t, func() error {
		return doPluginEdit(ctx, svc, game, core.PluginEdit{Op: core.PluginOpDisable, Plugins: []string{"B.esm"}})
	})
	require.NoError(t, err)
	assert.Contains(t, stdout, "  ✓ B.esm: disabled\n")
	assert.Contains(t, stdout, "Plugins.txt written: 2 plugin(s), 1 active")
	assert.Contains(t, stderr, "Warning: plugins: A.esp needs the master(s) B.esm (installed, not active)")
	assert.Equal(t, "B.esm\n*A.esp\n", readList(t, listDir))
}

func TestCLI_PluginsMoveSortAndRules(t *testing.T) {
	ctx := context.Background()
	svc, game, listDir := setupPluginsCLI(t, map[string]map[string]string{
		"a": {"A.esp": cliPlugin(0)},
		"b": {"B.esp": cliPlugin(0)},
	})
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "Skyrim.esm"), []byte(cliPlugin(1)), 0o644))
	_ = captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	resetPluginFlags(t)
	require.Equal(t, "*A.esp\n*B.esp\n", readList(t, listDir))

	stdout := captureStdout(t, func() error {
		return doPluginEdit(ctx, svc, game, core.PluginEdit{Op: core.PluginOpMove, Plugin: "B.esp", To: core.PluginMoveFirst})
	})
	assert.Contains(t, stdout, "  ✓ B.esp: moved to the top of the order\n")
	assert.Equal(t, "*B.esp\n*A.esp\n", readList(t, listDir))

	stdout = captureStdout(t, func() error {
		return doPluginEdit(ctx, svc, game, core.PluginEdit{Op: core.PluginOpRuleAdd, Rule: domain.PluginRule{Name: "B.esp", After: []string{"A.esp"}}})
	})
	assert.Contains(t, stdout, "  ✓ B.esp: loads after A.esp\n")
	assert.Equal(t, "*A.esp\n*B.esp\n", readList(t, listDir), "the rule overrides the order")

	stdout = captureStdout(t, func() error { return doPluginsList(ctx, svc, game) })
	assert.Regexp(t, `B\.esp\s+rule: after A\.esp`, stdout)
	assert.Contains(t, stdout, "1 rule(s) and group(s): lmm plugins rules list")

	stdout = captureStdout(t, func() error { return doPluginRulesList(ctx, svc, game) })
	assert.Contains(t, stdout, "  B.esp - after A.esp\n")

	stdout = captureStdout(t, func() error { return doPluginEdit(ctx, svc, game, core.PluginEdit{Op: core.PluginOpSort}) })
	assert.Contains(t, stdout, "the profile's order now records where rules and masters place its plugins")
	profile, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []domain.PluginEntry{{Name: "A.esp", Enabled: true}, {Name: "B.esp", Enabled: true}}, profile.Plugins.Order)

	_, _, err = captureStdoutStderrErr(t, func() error {
		return doPluginEdit(ctx, svc, game, core.PluginEdit{Op: core.PluginOpRuleAdd, Rule: domain.PluginRule{Name: "A.esp", After: []string{"B.esp"}}})
	})
	var cycle *core.PluginOrderCycleError
	require.ErrorAs(t, err, &cycle)
}

func TestCLI_PluginsRefusesAGameWithoutAPluginList(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	resetPluginFlags(t)
	err := doPluginsList(ctx, svc, game)
	require.ErrorIs(t, err, core.ErrNoPluginList)
}

func TestJSONGolden_Plugins(t *testing.T) {
	ctx := context.Background()
	svc, game, listDir := deployedPluginsCLI(t)
	withJSONOutput(t)
	subs := []string{game.Prefix, "<PREFIX>"}
	_ = listDir

	out := captureStdout(t, func() error { return doPluginsList(ctx, svc, game) })
	assertJSONCLIGolden(t, "plugins_list", out, subs...)

	setFlag(t, &pluginsDryRun, true)
	out = captureStdout(t, func() error {
		return doPluginEdit(ctx, svc, game, core.PluginEdit{Op: core.PluginOpDisable, Plugins: []string{"A.esp"}})
	})
	assertJSONCLIGolden(t, "plugins_edit_dry_run", out, subs...)

	setFlag(t, &pluginsDryRun, false)
	out = captureStdout(t, func() error {
		return doPluginEdit(ctx, svc, game, core.PluginEdit{Op: core.PluginOpRuleAdd, Rule: domain.PluginRule{Name: "A.esp", Req: []string{"B.esm"}}})
	})
	assertJSONCLIGolden(t, "plugins_edit_result", out, subs...)

	out = captureStdout(t, func() error { return doPluginRulesList(ctx, svc, game) })
	assertJSONCLIGolden(t, "plugins_rules_list", out, subs...)
}

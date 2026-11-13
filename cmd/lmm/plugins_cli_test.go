package main

// #616 at the CLI: a deploy on a Bethesda game writes its Plugins.txt and
// says so; a purge puts the user's list back and says so; verify prints the
// plugin list's rows; and a plugin-order cycle has its --json envelope.

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliPlugin is a minimal TES4 plugin header naming masters.
func cliPlugin(flags uint32, masters ...string) string {
	var body []byte
	for _, m := range masters {
		data := append([]byte(m), 0)
		sub := make([]byte, 6, 6+len(data))
		copy(sub, "MAST")
		binary.LittleEndian.PutUint16(sub[4:], uint16(len(data)))
		body = append(body, append(sub, data...)...)
	}
	hdr := make([]byte, 24)
	copy(hdr, "TES4")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(len(body)))
	binary.LittleEndian.PutUint32(hdr[8:], flags)
	return string(append(hdr, body...))
}

// setupPluginsCLI is a Skyrim SE game (catalog skyrim-se, the bethesda
// adapter, an explicit prefix) with mods shipping plugins.
func setupPluginsCLI(t *testing.T, mods map[string]map[string]string) (*core.Service, *domain.Game, string) {
	t.Helper()
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	app.RegisterAdapters(svc)
	game.InstallPath = filepath.Dir(game.ModPath)
	game.ModPath = filepath.Join(game.ModPath, "Data")
	game.Adapter = "bethesda"
	game.Catalog = "skyrim-se"
	prefix := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(prefix, "drive_c", "users", "steamuser"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(prefix, "system.reg"), nil, 0o644))
	game.Prefix = prefix
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(ctx, game))
	for _, id := range []string{"a", "b"} {
		files, ok := mods[id]
		if !ok {
			continue
		}
		for rel, content := range files {
			require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: "src", ModID: id, Version: "1.0"}, rel, []byte(content)))
		}
		seedSyncInstalledMod(t, svc, game, "src", id, id, "1.0", "default", true, nil)
		require.NoError(t, getProfileManager(svc).AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}))
	}
	return svc, game, filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim Special Edition")
}

func TestCLI_DeployWritesPluginsTxtAndPurgeRestoresIt(t *testing.T) {
	ctx := context.Background()
	svc, game, listDir := setupPluginsCLI(t, map[string]map[string]string{
		"a": {"A.esp": cliPlugin(0)},
		"b": {"B.esm": cliPlugin(1)},
	})
	require.NoError(t, os.MkdirAll(listDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(listDir, "Plugins.txt"), []byte("# mine\r\n"), 0o644))

	stdout := captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	assert.Contains(t, stdout, "Plugins.txt written: 2 plugin(s), 2 active\n")
	assert.Contains(t, stdout, "  "+filepath.Join(listDir, "Plugins.txt")+"\n")
	got, err := os.ReadFile(filepath.Join(listDir, "Plugins.txt"))
	require.NoError(t, err)
	assert.Equal(t, "# mine\r\n*B.esm\r\n*A.esp\r\n", string(got))

	setFlag(t, &purgeProfile, "")
	setFlag(t, &purgeYes, true)
	stdout = captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Contains(t, stdout, "Plugins.txt restored to the list you had before lmm wrote it\n")
	got, err = os.ReadFile(filepath.Join(listDir, "Plugins.txt"))
	require.NoError(t, err)
	assert.Equal(t, "# mine\r\n", string(got))
}

func TestCLI_DeployJSONCarriesThePluginList(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := setupPluginsCLI(t, map[string]map[string]string{"a": {"A.esp": cliPlugin(0, "Absent.esm")}})
	withJSONOutput(t)
	stdout := captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	assert.Contains(t, stdout, `"plugins": {`)
	assert.Contains(t, stdout, `"status": "written"`)
	assert.Contains(t, stdout, `"code": "plugin_missing_master"`)
	assert.Contains(t, stdout, `"severity": "error"`)
}

func TestCLI_DeployWarnsWhenTheListHasAnError(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := setupPluginsCLI(t, map[string]map[string]string{"a": {"A.esp": cliPlugin(0, "Absent.esm")}})
	stdout, stderr, _ := captureStdoutStderrErr(t, func() error { return doDeploy(ctx, svc, game, nil) })
	assert.Contains(t, stdout, "Plugins.txt written: 1 plugin(s), 1 active - ")
	assert.Contains(t, stdout, "with errors, see the warnings above")
	assert.Contains(t, stderr, "Warning: plugins: A.esp needs the master(s) Absent.esm (not installed)")
}

func TestCLI_VerifyPrintsPluginDrift(t *testing.T) {
	ctx := context.Background()
	svc, game, listDir := setupPluginsCLI(t, map[string]map[string]string{"a": {"A.esp": cliPlugin(0)}})
	_ = captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	require.NoError(t, os.WriteFile(filepath.Join(listDir, "Plugins.txt"), []byte("A.esp\r\n"), 0o644))

	stdout, _ := captureStdoutErr(t, func() error { return doVerify(ctx, svc, game, nil) })
	assert.Contains(t, stdout, "plugins - A.esp is inactive in Plugins.txt; the profile activates it\n")
	assert.Contains(t, stdout, "Run 'lmm verify --fix' (or 'lmm deploy') to write the list in lmm's order.")
}

func TestReportError_JSON_PluginOrderCycleError(t *testing.T) {
	withJSONOutput(t)
	err := &core.PluginOrderCycleError{Edges: []core.PluginOrderEdge{
		{From: "B.esp", To: "A.esp", Reason: core.PluginEdgeMaster},
		{From: "A.esp", To: "B.esp", Reason: core.PluginEdgeAfter},
	}}
	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"the plugin order has a cycle, so no order satisfies it: B.esp is a master of A.esp; a rule loads B.esp after A.esp - change a rule in the profile's plugins: so one of these can give way\",\n"+
		"  \"details\": {\n"+
		"    \"cycle\": [\n"+
		"      {\n"+
		"        \"from\": \"B.esp\",\n"+
		"        \"to\": \"A.esp\",\n"+
		"        \"reason\": \"master\"\n"+
		"      },\n"+
		"      {\n"+
		"        \"from\": \"A.esp\",\n"+
		"        \"to\": \"B.esp\",\n"+
		"        \"reason\": \"rule_after\"\n"+
		"      }\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
}

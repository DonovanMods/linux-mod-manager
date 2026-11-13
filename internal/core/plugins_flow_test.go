package core_test

// plugins_flow_test.go drives #616's plugin-list sync through the real
// flows and the real bethesda adapter: a deploy writes the game's
// Plugins.txt into its prefix, keeps the plugins lmm did not deploy where
// they were, and a purge puts back the list the user had.

import (
	"context"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bethesda"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pluginBytes is a minimal TES4 plugin: a header record with flags and one
// MAST subrecord per master.
func pluginBytes(flags uint32, masters ...string) []byte {
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
	return append(hdr, body...)
}

// pluginFixture is a Skyrim SE game on the bethesda adapter with an explicit
// prefix holding one user, steamuser, and Skyrim.esm in its Data folder.
type pluginFixture struct {
	svc     *core.Service
	dataDir string
	game    *domain.Game
	prefix  string
	listDir string
}

func newPluginFixture(t *testing.T) *pluginFixture {
	t.Helper()
	svc, dataDir := newOriginalsService(t)
	svc.RegisterAdapter(bethesda.New())
	install := t.TempDir()
	prefix := prefixTree(t, t.TempDir(), "steamuser")
	game := &domain.Game{
		ID: "skyrim-se", Name: "Skyrim SE", InstallPath: install, ModPath: filepath.Join(install, "Data"),
		LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true, Adapter: bethesda.ID, Prefix: prefix,
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "Skyrim.esm"), pluginBytes(1), 0o644))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return &pluginFixture{
		svc: svc, dataDir: dataDir, game: game, prefix: prefix,
		listDir: filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim Special Edition"),
	}
}

// mod seeds an installed mod shipping files and adds it to the default
// profile, in that order.
func (f *pluginFixture) mod(t *testing.T, id string, files map[string][]byte) {
	t.Helper()
	seedNamedInstalledMod(t, f.svc, f.game, "src", id, id, "1.0", true, files)
	seedProfileWithMod(t, f.svc, f.game.ID, "default", "src", id, "1.0")
}

func (f *pluginFixture) deploy(t *testing.T) *core.DeployResult {
	t.Helper()
	res, err := f.svc.DeployProfile(context.Background(), f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return res
}

func (f *pluginFixture) list(t *testing.T) string {
	t.Helper()
	return readFileString(t, filepath.Join(f.listDir, "Plugins.txt"))
}

func (f *pluginFixture) setPlugins(t *testing.T, plugins domain.ProfilePlugins) {
	t.Helper()
	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	profile.Plugins = plugins
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), profile))
}

type prefixOriginalRow struct {
	Prefix  string `json:"prefix"`
	User    string `json:"user"`
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
}

func readPrefixOriginals(t *testing.T, dataDir, gameID string) []prefixOriginalRow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "snapshots", gameID, "_originals", "originals-prefix.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var m struct {
		Files []prefixOriginalRow `json:"files"`
	}
	require.NoError(t, json.Unmarshal(data, &m))
	return m.Files
}

func TestPlugins_DeployWritesPluginsTxt(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "patch", map[string][]byte{"Patch.esp": pluginBytes(0, "Skyrim.esm", "Base.esm")})
	f.mod(t, "base", map[string][]byte{"Base.esm": pluginBytes(1, "Skyrim.esm"), "textures/a.dds": []byte("x")})
	f.mod(t, "light", map[string][]byte{"Small.esl": pluginBytes(0)})

	res := f.deploy(t)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListWritten, res.Plugins.Status)
	assert.Empty(t, res.Plugins.Findings)
	assert.Equal(t, 3, res.Plugins.Plugins)
	assert.Equal(t, 3, res.Plugins.Active)
	assert.Equal(t, filepath.Join(f.listDir, "Plugins.txt"), res.Plugins.ListFile)

	// Masters (Base.esm, and the light master Small.esl) first in mod
	// order, then Patch.esp; Skyrim.esm is implicit and never listed. A new
	// file is CRLF.
	assert.Equal(t, "*Base.esm\r\n*Small.esl\r\n*Patch.esp\r\n", f.list(t))

	rows := readPrefixOriginals(t, f.dataDir, f.game.ID)
	require.Len(t, rows, 1)
	assert.Equal(t, "AppData/Local/Skyrim Special Edition/Plugins.txt", rows[0].Path)
	assert.False(t, rows[0].Existed, "there was no list before lmm's")

	again := f.deploy(t)
	assert.Equal(t, core.PluginListUnchanged, again.Plugins.Status)
}

func TestPlugins_ExternalPluginsKeepTheirStateAndPlace(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.mod(t, "b", map[string][]byte{"B.esp": pluginBytes(0)})
	// Hand-installed: Creation.esp (active) and Off.esp (inactive). Gone.esp
	// is listed but no longer installed. The existing file is LF, named in
	// lower case, and opens with the game's comment.
	for _, name := range []string{"Creation.esp", "Off.esp"} {
		require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, name), pluginBytes(0), 0o644))
	}
	require.NoError(t, os.MkdirAll(f.listDir, 0o755))
	lower := filepath.Join(f.listDir, "plugins.txt")
	require.NoError(t, os.WriteFile(lower, []byte("# This file is used by the game\n*B.esp\n*Creation.esp\nOff.esp\n*Gone.esp\n*A.esp\n"), 0o600))

	res := f.deploy(t)
	assert.Equal(t, core.PluginListWritten, res.Plugins.Status)
	assert.Equal(t, 2, res.Plugins.External)
	assertAbsent(t, filepath.Join(f.listDir, "Plugins.txt"), "the existing file's own spelling is written to")
	assert.Equal(t, "# This file is used by the game\n*A.esp\n*B.esp\n*Creation.esp\nOff.esp\n", readFileString(t, lower),
		"lmm's plugins in its order; the hand-installed ones follow B.esp as before, Off.esp still inactive; the uninstalled one is gone; LF kept")
	info, err := os.Stat(lower)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the file keeps its mode")

	rows := readPrefixOriginals(t, f.dataDir, f.game.ID)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].Existed)
	assert.Equal(t, "AppData/Local/Skyrim Special Edition/plugins.txt", rows[0].Path)
}

func TestPlugins_ProfileOrderAndDisabledEntries(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.mod(t, "b", map[string][]byte{"B.esp": pluginBytes(0)})
	f.mod(t, "c", map[string][]byte{"C.esp": pluginBytes(0)})
	f.setPlugins(t, domain.ProfilePlugins{Order: []domain.PluginEntry{
		{Name: "C.esp", Enabled: true}, {Name: "A.esp", Enabled: false}, {Name: "B.esp", Enabled: true}, {Name: "Absent.esp", Enabled: true},
	}})
	res := f.deploy(t)
	assert.Equal(t, "*C.esp\r\nA.esp\r\n*B.esp\r\n", f.list(t))
	assert.Equal(t, 2, res.Plugins.Active)

	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	assert.Len(t, profile.Plugins.Order, 4, "the effective order is never written back to the profile")
}

func TestPlugins_MissingMasterIsAnErrorFindingAndTheListIsStillWritten(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "patch", map[string][]byte{"Patch.esp": pluginBytes(0, "Skyrim.esm", "Absent.esm")})
	res := f.deploy(t)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListWritten, res.Plugins.Status)
	require.Len(t, res.Plugins.Findings, 1)
	got := res.Plugins.Findings[0]
	assert.Equal(t, core.PluginFindingMissingMaster, got.Code)
	assert.Equal(t, core.PluginSeverityError, got.Severity)
	assert.Equal(t, []string{"Absent.esm"}, got.Missing)
	assert.Contains(t, res.Warnings, "plugins: "+got.Message, "the finding is a warning too")
}

func TestPlugins_CycleLeavesTheListAlone(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0, "B.esp")})
	f.mod(t, "b", map[string][]byte{"B.esp": pluginBytes(0)})
	require.NoError(t, os.MkdirAll(f.listDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte("*Before.esp\r\n"), 0o644))
	f.setPlugins(t, domain.ProfilePlugins{Rules: domain.PluginRules{Plugins: []domain.PluginRule{{Name: "B.esp", After: []string{"A.esp"}}}}})

	res := f.deploy(t)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListSkipped, res.Plugins.Status)
	require.Len(t, res.Plugins.Findings, 1)
	assert.Equal(t, core.PluginFindingCycle, res.Plugins.Findings[0].Code)
	assert.Len(t, res.Plugins.Findings[0].Cycle, 2)
	assert.Equal(t, "*Before.esp\r\n", f.list(t), "nothing is written")
	assert.Empty(t, readPrefixOriginals(t, f.dataDir, f.game.ID))
}

func TestPlugins_UnresolvedPrefixIsAFindingAndTheDeploySucceeds(t *testing.T) {
	f := newPluginFixture(t)
	f.game.Prefix = filepath.Join(t.TempDir(), "absent")
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})

	res := f.deploy(t)
	assert.Equal(t, 1, res.Deployed)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListSkipped, res.Plugins.Status)
	require.Len(t, res.Plugins.Findings, 1)
	assert.Equal(t, core.FindingPrefixUnresolved, res.Plugins.Findings[0].Code)
	assert.Empty(t, res.Plugins.ListFile)
}

func TestPlugins_UninstallAndDisableResync(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.mod(t, "b", map[string][]byte{"B.esp": pluginBytes(0)})
	f.deploy(t)
	require.Equal(t, "*A.esp\r\n*B.esp\r\n", f.list(t))

	_, err := f.svc.DisableMod(context.Background(), f.game, "default", "src", "a")
	require.NoError(t, err)
	assert.Equal(t, "*B.esp\r\n", f.list(t), "a disabled mod's plugin leaves the list")

	_, err = f.svc.EnableMod(context.Background(), f.game, "default", "src", "a")
	require.NoError(t, err)
	assert.Equal(t, "*A.esp\r\n*B.esp\r\n", f.list(t))
}

func TestPlugins_PurgeRestoresTheOriginalList(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	require.NoError(t, os.MkdirAll(f.listDir, 0o755))
	original := "# game\r\n*Mine.esp\r\n"
	require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, "Mine.esp"), pluginBytes(0), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte(original), 0o644))
	f.deploy(t)
	require.Equal(t, "# game\r\n*Mine.esp\r\n*A.esp\r\n", f.list(t))

	plan, err := f.svc.PlanPurge(context.Background(), f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	res, err := f.svc.ApplyPurge(context.Background(), f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListRestored, res.Plugins.Status)
	assert.Equal(t, original, f.list(t), "the list the user had is back, byte for byte")
	assert.Empty(t, readPrefixOriginals(t, f.dataDir, f.game.ID), "the original was handed back")
}

func TestPlugins_PurgeRemovesAListLmmCreated(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.deploy(t)
	require.FileExists(t, filepath.Join(f.listDir, "Plugins.txt"))

	plan, err := f.svc.PlanPurge(context.Background(), f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	res, err := f.svc.ApplyPurge(context.Background(), f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListRemoved, res.Plugins.Status)
	assertAbsent(t, filepath.Join(f.listDir, "Plugins.txt"), "there was no list before lmm's")
}

func TestPlugins_PurgeKeepsAnExternalPluginAddedSince(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	require.NoError(t, os.MkdirAll(f.listDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte("*Mine.esp\r\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, "Mine.esp"), pluginBytes(0), 0o644))
	f.deploy(t)
	// The game's own Creations menu adds and activates a plugin.
	require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, "Bought.esp"), pluginBytes(0), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte("*Mine.esp\r\n*A.esp\r\n*Bought.esp\r\n"), 0o644))

	plan, err := f.svc.PlanPurge(context.Background(), f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	_, err = f.svc.ApplyPurge(context.Background(), f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "*Mine.esp\r\n*Bought.esp\r\n", f.list(t), "lmm never deactivates a plugin it did not deploy")
}

func TestPlugins_RefusesAListFolderThatIsALink(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(f.listDir), 0o755))
	require.NoError(t, os.Symlink(outside, f.listDir))

	res := f.deploy(t)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListSkipped, res.Plugins.Status)
	require.NotEmpty(t, res.Plugins.Findings)
	assert.Equal(t, core.PluginFindingUnwritable, res.Plugins.Findings[0].Code)
	assertAbsent(t, filepath.Join(outside, "Plugins.txt"), "nothing is written through the link")
}

func TestPlugins_RefusesAListFileThatIsALink(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	outside := filepath.Join(t.TempDir(), "target.txt")
	require.NoError(t, os.WriteFile(outside, []byte("keep\n"), 0o644))
	require.NoError(t, os.MkdirAll(f.listDir, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(f.listDir, "Plugins.txt")))

	res := f.deploy(t)
	assert.Equal(t, core.PluginListSkipped, res.Plugins.Status)
	assert.Equal(t, "keep\n", readFileString(t, outside))
}

func TestPlugins_RefusesAUserDirectorySwappedForALink(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	user := filepath.Join(f.prefix, "drive_c", "users", "steamuser")
	outside := t.TempDir()
	require.NoError(t, os.RemoveAll(user))
	require.NoError(t, os.Symlink(outside, user))

	res := f.deploy(t)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListSkipped, res.Plugins.Status)
	assertAbsent(t, filepath.Join(outside, "AppData"), "nothing is written outside the prefix")
}

func TestPlugins_SkyrimLEWritesLoadorderTxt(t *testing.T) {
	f := newPluginFixture(t)
	f.game.ID = "skyrim"
	f.listDir = filepath.Join(f.prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim")
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.mod(t, "b", map[string][]byte{"B.esp": pluginBytes(0)})
	f.setPlugins(t, domain.ProfilePlugins{Order: []domain.PluginEntry{{Name: "B.esp", Enabled: false}}})

	res := f.deploy(t)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListWritten, res.Plugins.Status)
	assert.Equal(t, filepath.Join(f.listDir, "loadorder.txt"), res.Plugins.OrderFile)
	assert.Equal(t, "A.esp\r\n", f.list(t), "Plugins.txt lists the active plugins")
	assert.Equal(t, "Skyrim.esm\r\nA.esp\r\nB.esp\r\n", readFileString(t, filepath.Join(f.listDir, "loadorder.txt")),
		"loadorder.txt lists every plugin, the implicit ones first")
	assert.Len(t, readPrefixOriginals(t, f.dataDir, f.game.ID), 2)
}

func TestPlugins_NonBethesdaGameHasNoPluginList(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	seedNamedInstalledMod(t, svc, game, "src", "a", "a", "1.0", true, map[string][]byte{"A.esp": pluginBytes(0)})
	seedProfileWithMod(t, svc, game.ID, "default", "src", "a", "1.0")
	res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Nil(t, res.Plugins)
}

func (f *pluginFixture) verify(t *testing.T, fix bool) *core.VerifyResult {
	t.Helper()
	rep, err := f.svc.VerifyReport(context.Background(), f.game, "default", core.VerifyOptions{Fix: fix, Force: true}, nil)
	require.NoError(t, err)
	return rep.Result
}

func TestPlugins_VerifyReportsDriftForLmmsOwnPluginsAndFixRewrites(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.mod(t, "b", map[string][]byte{"B.esp": pluginBytes(0)})
	require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, "Ext.esp"), pluginBytes(0), 0o644))
	f.deploy(t)
	clean := f.verify(t, false)
	assert.Nil(t, findingWithStatus(clean, core.VerifyStatusPluginDrift), "statuses: %v", findingStatuses(clean))

	// Someone deactivates A.esp, and adds an external plugin: only A.esp
	// is drift.
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte("A.esp\r\n*B.esp\r\n*Ext.esp\r\n"), 0o644))
	res := f.verify(t, false)
	row := findingWithStatus(res, core.VerifyStatusPluginDrift)
	require.NotNil(t, row, "statuses: %v", findingStatuses(res))
	assert.Equal(t, "A.esp is inactive in Plugins.txt; the profile activates it", row.Note)
	assert.True(t, row.Fixable)
	assert.Equal(t, 1, res.Warnings)

	fixed := f.verify(t, true)
	require.NotNil(t, findingWithStatus(fixed, core.VerifyStatusFixedPluginDrift), "statuses: %v", findingStatuses(fixed))
	assert.Equal(t, "*A.esp\r\n*B.esp\r\n*Ext.esp\r\n", f.list(t), "--fix rewrites lmm's plugins and keeps the external one")
	assert.Nil(t, findingWithStatus(f.verify(t, false), core.VerifyStatusPluginDrift))
}

func TestPlugins_VerifyReportsAMovedPlugin(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.mod(t, "b", map[string][]byte{"B.esp": pluginBytes(0)})
	f.deploy(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte("*B.esp\r\n*A.esp\r\n"), 0o644))
	row := findingWithStatus(f.verify(t, false), core.VerifyStatusPluginDrift)
	require.NotNil(t, row)
	assert.Equal(t, "A.esp has moved in Plugins.txt since lmm wrote it", row.Note)
}

func TestPlugins_VerifyReportsAMissingMasterAsAnIssue(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "p", map[string][]byte{"Patch.esp": pluginBytes(0, "Absent.esm")})
	f.deploy(t)
	res := f.verify(t, false)
	row := findingWithStatus(res, core.PluginFindingMissingMaster)
	require.NotNil(t, row, "statuses: %v", findingStatuses(res))
	assert.Equal(t, "Patch.esp", row.FileID)
	assert.False(t, row.Fixable)
	assert.GreaterOrEqual(t, res.Issues, 1)
}

func TestPlugins_UnresolvedPrefixWithoutPluginsSaysNothing(t *testing.T) {
	f := newPluginFixture(t)
	f.game.Prefix = ""
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))
	f.mod(t, "tex", map[string][]byte{"textures/a.dds": []byte("x")})
	res := f.deploy(t)
	assert.Nil(t, res.Plugins, "no plugin to list, so no plugin list to report on")
}

func TestPlugins_AListLmmNeverWroteIsLeftAloneWithoutLmmPlugins(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "tex", map[string][]byte{"textures/a.dds": []byte("x")})
	require.NoError(t, os.MkdirAll(f.listDir, 0o755))
	original := "*Skyrim.esm\r\n*Gone.esp\r\n"
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte(original), 0o644))
	res := f.deploy(t)
	assert.Nil(t, res.Plugins, "no plugin of lmm's, and a list lmm never wrote")
	assert.Equal(t, original, f.list(t))
}

func TestPlugins_UninstallingTheLastPluginStillUpdatesTheList(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.deploy(t)
	require.Equal(t, "*A.esp\r\n", f.list(t))
	_, err := f.svc.DisableMod(context.Background(), f.game, "default", "src", "a")
	require.NoError(t, err)
	assert.Equal(t, "", f.list(t), "lmm wrote this list, so it keeps it in step")
}

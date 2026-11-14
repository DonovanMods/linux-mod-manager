package core_test

// loadorder_flow_test.go drives #620's file load-order sync through the real
// flows and the real cyberpunk2077 adapter: a deploy writes the profile's
// archive order to archive/pc/mod/modlist.txt, keeps the archives lmm did
// not deploy where the list had them, and a purge puts back the list the
// user had.

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/cyberpunk2077"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const archiveOrder = "cyberpunk2077-archive"

// archiveFixture is a Cyberpunk 2077 game on the cyberpunk2077 adapter,
// whose mod_path is its install root, as the Steam catalog sets it.
type archiveFixture struct {
	svc     *core.Service
	dataDir string
	game    *domain.Game
	modDir  string
}

func newArchiveFixture(t *testing.T) *archiveFixture {
	t.Helper()
	svc, dataDir := newOriginalsService(t)
	svc.RegisterAdapter(cyberpunk2077.New())
	install := t.TempDir()
	game := &domain.Game{
		ID: "cyberpunk2077", Name: "Cyberpunk 2077", InstallPath: install, ModPath: install,
		LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true, Adapter: cyberpunk2077.ID,
	}
	modDir := filepath.Join(install, "archive", "pc", "mod")
	require.NoError(t, os.MkdirAll(modDir, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return &archiveFixture{svc: svc, dataDir: dataDir, game: game, modDir: modDir}
}

// mod seeds an installed mod shipping the named archives (and a script
// elsewhere) and adds it to the default profile.
func (f *archiveFixture) mod(t *testing.T, id string, archives ...string) {
	t.Helper()
	files := map[string][]byte{"r6/scripts/" + id + ".reds": []byte("x")}
	for _, a := range archives {
		files["archive/pc/mod/"+a] = []byte(a)
	}
	seedNamedInstalledMod(t, f.svc, f.game, "src", id, id, "1.0", true, files)
	seedProfileWithMod(t, f.svc, f.game.ID, "default", "src", id, "1.0")
}

func (f *archiveFixture) setOrder(t *testing.T, names ...string) {
	t.Helper()
	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	profile.LoadOrders = nil
	if len(names) > 0 {
		profile.LoadOrders = domain.FileLoadOrders{archiveOrder: names}
	}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), profile))
}

func (f *archiveFixture) deploy(t *testing.T) *core.DeployResult {
	t.Helper()
	res, err := f.svc.DeployProfile(context.Background(), f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return res
}

func (f *archiveFixture) purge(t *testing.T) *core.PurgeResult {
	t.Helper()
	plan, err := f.svc.PlanPurge(context.Background(), f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	res, err := f.svc.ApplyPurge(context.Background(), f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	return res
}

func (f *archiveFixture) modlist(t *testing.T) string {
	t.Helper()
	return readFileString(t, filepath.Join(f.modDir, "modlist.txt"))
}

func (f *archiveFixture) originals(t *testing.T) []prefixOriginalRow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dataDir, "snapshots", f.game.ID, "_originals", "originals-loadorders.json"))
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

// A profile that orders nothing leaves the game's list alone: the order is
// opt-in, so an existing Cyberpunk profile deploys exactly as before.
func TestLoadOrder_NoOrderWritesNothing(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive")
	res := f.deploy(t)
	assert.Nil(t, res.LoadOrders)
	assertAbsent(t, filepath.Join(f.modDir, "modlist.txt"), "no order, no list")
	assert.Empty(t, f.originals(t))
}

func TestLoadOrder_DeployWritesTheProfileOrder(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "basegame_a.archive")
	f.mod(t, "b", "basegame_b.archive")
	f.mod(t, "c", "! fixes.archive", "zz_late.archive")
	f.setOrder(t, "basegame_b.archive", "BASEGAME_A.archive", "not-deployed.archive", "! fixes.archive")

	res := f.deploy(t)
	require.Len(t, res.LoadOrders, 1)
	lo := res.LoadOrders[0]
	assert.Equal(t, archiveOrder, lo.ID)
	assert.Equal(t, core.PluginListWritten, lo.Status)
	assert.Equal(t, 4, lo.Files)
	assert.Empty(t, lo.Findings)
	assert.Equal(t, filepath.Join(f.modDir, "modlist.txt"), lo.ListFile)
	// The profile's order, each archive spelled as on disk; an archive it
	// does not name goes in straight after the archive it sorts next after
	// (zz_late after basegame_b, the greatest name below it), as Vortex's
	// extension places it; a new file is CRLF with no trailing line
	// ending, as Vortex writes it.
	assert.Equal(t, "basegame_b.archive\r\nzz_late.archive\r\nbasegame_a.archive\r\n! fixes.archive", f.modlist(t))
	rows := f.originals(t)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].Existed)
	assert.Equal(t, "archive/pc/mod/modlist.txt", rows[0].Path)

	again := f.deploy(t)
	require.Len(t, again.LoadOrders, 1)
	assert.Equal(t, core.PluginListUnchanged, again.LoadOrders[0].Status)
}

// An unordered archive goes in beside the archive it sorts after, and
// before every other one when it sorts first.
func TestLoadOrder_NewArchiveTakesItsAlphabeticalSlot(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "m.archive", "c.archive", "a.archive", "x.archive")
	f.setOrder(t, "x.archive", "c.archive")
	f.deploy(t)
	// a sorts before every listed one: before the first it precedes (c);
	// m follows c.
	assert.Equal(t, "x.archive\r\na.archive\r\nc.archive\r\nm.archive", f.modlist(t))
}

// An archive lmm did not deploy is never reordered (owner ruling): every
// one on disk is listed at its place in the order the game loads them in
// now - a listed one after the archive it followed, an unlisted one after
// the listed ones - and a purge takes back the ones lmm added.
func TestLoadOrder_KeepsExternalArchives(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive", "b.archive")
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "mine.archive"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "loose.archive"), []byte("x"), 0o644))
	original := "b.archive\nmine.archive\ngone.archive\n"
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "modlist.txt"), []byte(original), 0o644))
	f.setOrder(t, "a.archive", "b.archive")

	res := f.deploy(t)
	require.Len(t, res.LoadOrders, 1)
	lo := res.LoadOrders[0]
	assert.Equal(t, 2, lo.External)
	assert.Empty(t, lo.Findings)
	// The game loads b, mine, then (unlisted, alphabetically) a and loose:
	// mine stays straight after b, and loose after mine. gone.archive is
	// not on disk; an existing file keeps its LF endings and trailing
	// newline.
	assert.Equal(t, "a.archive\nb.archive\nmine.archive\nloose.archive\n", f.modlist(t))
	rows := f.originals(t)
	require.Len(t, rows, 1)

	// Purge: lmm's archives and the one it added come out, the user's own
	// list comes back.
	pr := f.purge(t)
	require.Len(t, pr.LoadOrders, 1)
	assert.Equal(t, core.PluginListRestored, pr.LoadOrders[0].Status)
	assert.Equal(t, original, f.modlist(t), "the list the user had is back, byte for byte")
	assert.Empty(t, f.originals(t))
}

// External archives keep their order among themselves, whatever the
// profile does to lmm's own, and a list lmm created goes at purge.
func TestLoadOrder_ExternalsKeepTheirOrder(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive", "z.archive")
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "m.archive"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "c.archive"), []byte("x"), 0o644))
	f.setOrder(t, "z.archive", "a.archive")

	f.deploy(t)
	// The game loaded a, c, m, z; c and m followed a, and still do, in
	// their order.
	assert.Equal(t, "z.archive\r\na.archive\r\nc.archive\r\nm.archive", f.modlist(t))

	res := f.purge(t)
	require.Len(t, res.LoadOrders, 1)
	assert.Equal(t, core.PluginListRemoved, res.LoadOrders[0].Status, "lmm added both externals, so nothing of the user's is left")
	assertAbsent(t, filepath.Join(f.modDir, "modlist.txt"), "there was no list before lmm's")
}

func TestLoadOrder_PurgeRemovesAListLmmCreated(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive")
	f.setOrder(t, "a.archive")
	f.deploy(t)
	require.FileExists(t, filepath.Join(f.modDir, "modlist.txt"))

	res := f.purge(t)
	require.Len(t, res.LoadOrders, 1)
	assert.Equal(t, core.PluginListRemoved, res.LoadOrders[0].Status)
	assertAbsent(t, filepath.Join(f.modDir, "modlist.txt"), "there was no list before lmm's")
}

// What the user added to lmm's list since keeps its place through a purge.
func TestLoadOrder_PurgeKeepsAnEntryAddedSince(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive")
	f.setOrder(t, "a.archive")
	f.deploy(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "mine.archive"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "modlist.txt"), []byte("mine.archive\r\na.archive"), 0o644))

	f.purge(t)
	assert.Equal(t, "mine.archive", f.modlist(t))
}

// Clearing the profile's order hands the list back at the next deploy: the
// game's own order applies again.
func TestLoadOrder_ResetHandsTheListBack(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive", "b.archive")
	f.setOrder(t, "b.archive", "a.archive")
	f.deploy(t)
	require.Equal(t, "b.archive\r\na.archive", f.modlist(t))

	f.setOrder(t)
	res := f.deploy(t)
	require.Len(t, res.LoadOrders, 1)
	assert.Equal(t, core.PluginListRemoved, res.LoadOrders[0].Status)
	assertAbsent(t, filepath.Join(f.modDir, "modlist.txt"), "lmm created it, so a reset removes it")
	assert.Empty(t, f.originals(t))
}

// A mod that ships its own modlist.txt is not written over.
func TestLoadOrder_ModShippingTheListIsNotOverwritten(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive", "modlist.txt")
	f.setOrder(t, "a.archive")
	res := f.deploy(t)
	require.Len(t, res.LoadOrders, 1)
	assert.Equal(t, core.PluginListSkipped, res.LoadOrders[0].Status)
	require.NotEmpty(t, res.LoadOrders[0].Findings)
	assert.Equal(t, core.LoadOrderFindingDeployed, res.LoadOrders[0].Findings[0].Code)
	assert.Equal(t, "modlist.txt", f.modlist(t), "the mod's own file is untouched")
	assert.Empty(t, f.originals(t))
}

// The game's folders are matched case-insensitively, as Wine does, and the
// list's own spelling is kept.
func TestLoadOrder_MatchesFoldersAndListCaseInsensitively(t *testing.T) {
	f := newArchiveFixture(t)
	require.NoError(t, os.RemoveAll(filepath.Join(f.game.InstallPath, "archive")))
	f.modDir = filepath.Join(f.game.InstallPath, "Archive", "PC", "Mod")
	require.NoError(t, os.MkdirAll(f.modDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "ModList.txt"), []byte("old.archive"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "old.archive"), []byte("x"), 0o644))
	seedNamedInstalledMod(t, f.svc, f.game, "src", "a", "a", "1.0", true, map[string][]byte{"Archive/PC/Mod/a.archive": []byte("a")})
	seedProfileWithMod(t, f.svc, f.game.ID, "default", "src", "a", "1.0")
	f.setOrder(t, "a.archive")

	f.deploy(t)
	// old.archive is external and followed no archive of lmm's, so it
	// stays first.
	assert.Equal(t, "old.archive\r\na.archive", readFileString(t, filepath.Join(f.modDir, "ModList.txt")))
	assertAbsent(t, filepath.Join(f.modDir, "modlist.txt"), "the existing spelling is written to")
}

// A link where the list is, is never written through.
func TestLoadOrder_RefusesAListThatIsALink(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive")
	f.setOrder(t, "a.archive")
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	require.NoError(t, os.WriteFile(outside, []byte("keep"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(f.modDir, "modlist.txt")))

	res := f.deploy(t)
	require.Len(t, res.LoadOrders, 1)
	assert.Equal(t, core.PluginListSkipped, res.LoadOrders[0].Status)
	assert.True(t, res.LoadOrders[0].HasErrors())
	assert.Equal(t, "keep", readFileString(t, outside))
}

// A profile whose archive folder is gone - a drive that is not mounted -
// writes nothing rather than drop every one of its archives.
func TestLoadOrder_SkipsAFolderWithoutTheDeployedArchives(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive")
	f.setOrder(t, "a.archive")
	f.deploy(t)
	require.NoError(t, os.RemoveAll(f.modDir))

	results := f.svc.SyncLoadOrdersForTest(context.Background(), f.game, "default")
	require.Len(t, results, 1)
	assert.Equal(t, core.PluginListSkipped, results[0].Status)
	require.NotEmpty(t, results[0].Findings)
	assert.Equal(t, core.LoadOrderFindingUnwritable, results[0].Findings[0].Code)
	assert.Contains(t, results[0].Findings[0].Message, "is its drive mounted?")
}

// #620, mirroring #616's plugin_drift: verify reports one of lmm's archives
// the list dropped or moved since lmm wrote it, and --fix rewrites the list.
// An external archive's place is the user's, never drift.
func TestVerify_LoadOrderDrift(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive", "b.archive")
	f.setOrder(t, "b.archive", "a.archive")
	f.deploy(t)
	ctx := context.Background()
	list := filepath.Join(f.modDir, "modlist.txt")

	res, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(res.Result, core.VerifyStatusLoadOrderDrift), "nothing drifted yet")

	// Someone reorders lmm's archives and adds one of their own.
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "mine.archive"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(list, []byte("mine.archive\r\na.archive\r\nb.archive"), 0o644))
	res, err = f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	d := findingWithStatus(res.Result, core.VerifyStatusLoadOrderDrift)
	require.NotNil(t, d)
	assert.True(t, d.Fixable)
	assert.Equal(t, "modlist.txt", d.FileID)
	assert.Contains(t, d.Note, "has moved in modlist.txt")

	res, err = f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true, Fix: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, findingWithStatus(res.Result, core.VerifyStatusFixedLoadOrderDrift))
	assert.Equal(t, "mine.archive\r\nb.archive\r\na.archive", f.modlist(t), "the user's archive keeps its place")

	// A list deleted altogether drifts too.
	require.NoError(t, os.Remove(list))
	res, err = f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	d = findingWithStatus(res.Result, core.VerifyStatusLoadOrderDrift)
	require.NotNil(t, d)
	assert.Contains(t, d.Note, "modlist.txt is missing")
}

// A profile that orders nothing has no list to drift.
func TestVerify_NoLoadOrderNoDrift(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive", "b.archive")
	f.deploy(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "modlist.txt"), []byte("b.archive"), 0o644))
	res, err := f.svc.VerifyReport(context.Background(), f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(res.Result, core.VerifyStatusLoadOrderDrift))
}

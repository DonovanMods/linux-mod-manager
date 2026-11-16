package core_test

// modsettings_flow_test.go drives #646 through the real flows and the real
// baldurs-gate-3 adapter: a deploy lists each .pak's module in the game's
// modsettings.lsx (keeping the game's GustavX entry and every entry lmm did
// not add), in the profile's pak order; an uninstall takes lmm's entry out;
// a purge puts back the list the user had; verify reports a list the game
// reset, and --fix lists the paks again. Every pak is synthetic
// (lariantest): no real game file is in the repository.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/modtypes"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/larian"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/larian/lariantest"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	uuidA    = "aaaaaaaa-0000-0000-0000-000000000001"
	uuidB    = "bbbbbbbb-0000-0000-0000-000000000002"
	uuidC    = "cccccccc-0000-0000-0000-000000000003"
	uuidUser = "eeeeeeee-0000-0000-0000-00000000000e"
)

var userEntry = lariantest.Entry{Folder: "UserMod", Name: "A user's mod", UUID: uuidUser, Version64: "7"}

// bg3Fixture is a Baldur's Gate 3 game on the baldurs-gate-3 adapter with
// a Proton-shaped prefix, copy-deployed unless a test says otherwise.
type bg3Fixture struct {
	svc     *core.Service
	dataDir string
	prefix  string
	game    *domain.Game
}

func newBG3Fixture(t *testing.T, method domain.LinkMethod) *bg3Fixture {
	t.Helper()
	svc, dataDir := newOriginalsService(t)
	prefix := newPrefixDir(t)
	game := newBG3Game(t, svc, method, prefix)
	return &bg3Fixture{svc: svc, dataDir: dataDir, prefix: prefix, game: game}
}

// profileDir is the game's PlayerProfiles/Public in the prefix.
func (f *bg3Fixture) profileDir() string {
	return filepath.Join(f.prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Larian Studios", "Baldur's Gate 3", "PlayerProfiles", "Public")
}

func (f *bg3Fixture) listPath() string { return filepath.Join(f.profileDir(), "modsettings.lsx") }

// writeList writes the game's modsettings.lsx, as the game writes it on
// its first run.
func (f *bg3Fixture) writeList(t *testing.T, entries ...lariantest.Entry) []byte {
	t.Helper()
	require.NoError(t, os.MkdirAll(f.profileDir(), 0o755))
	data := lariantest.ModSettings(entries...)
	require.NoError(t, os.WriteFile(f.listPath(), data, 0o644))
	return data
}

// entries are the list's entries now, by name.
func (f *bg3Fixture) entries(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.listPath())
	require.NoError(t, err)
	s, err := larian.ParseModSettings(data)
	require.NoError(t, err, "lmm's list must parse:\n%s", data)
	var out []string
	for _, e := range s.Entries {
		out = append(out, e.Name)
	}
	return out
}

// mod seeds an installed mod shipping the named paks and adds it to the
// default profile.
func (f *bg3Fixture) mod(t *testing.T, id string, paks map[string][]byte) {
	t.Helper()
	files := map[string][]byte{id + "/info.json": []byte(`{}`)}
	for name, data := range paks {
		files[id+"/"+name] = data
	}
	seedNamedInstalledMod(t, f.svc, f.game, "src", id, "Mod "+id, "1.0", true, files)
	seedProfileWithMod(t, f.svc, f.game.ID, "default", "src", id, "1.0")
}

func (f *bg3Fixture) setOrder(t *testing.T, names ...string) {
	t.Helper()
	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	profile.LoadOrders = nil
	if len(names) > 0 {
		profile.LoadOrders = domain.FileLoadOrders{modtypes.PakOrderID: names}
	}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), profile))
}

func (f *bg3Fixture) deploy(t *testing.T) *core.DeployResult {
	t.Helper()
	res, err := f.svc.DeployProfile(context.Background(), f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return res
}

func (f *bg3Fixture) purge(t *testing.T) *core.PurgeResult {
	t.Helper()
	plan, err := f.svc.PlanPurge(context.Background(), f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	res, err := f.svc.ApplyPurge(context.Background(), f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	return res
}

// The issue (#646): a deployed .pak is enabled - its module is listed in
// modsettings.lsx after the game's GustavX entry and the user's own entry,
// which lmm keeps as they were - under every link method.
func TestModSettings_DeployListsThePaksModule(t *testing.T) {
	for _, method := range rootMethods {
		t.Run(method.String(), func(t *testing.T) {
			f := newBG3Fixture(t, method)
			before := f.writeList(t, lariantest.GustavX, userEntry)
			f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})

			res := f.deploy(t)
			require.Len(t, res.LoadOrders, 1, "%+v", res.LoadOrders)
			lo := res.LoadOrders[0]
			assert.Equal(t, modtypes.PakOrderID, lo.ID)
			assert.Equal(t, core.PluginListWritten, lo.Status, "%+v", lo.Findings)
			assert.Equal(t, 3, lo.Files)
			assert.Equal(t, 2, lo.External, "GustavX and the user's entry are not lmm's")
			assert.Equal(t, []string{"GustavX", "A user's mod", "Mod A"}, f.entries(t))

			data, err := os.ReadFile(f.listPath())
			require.NoError(t, err)
			gustav := string(before[strings.Index(string(before), `                        <node id="ModuleShortDesc">`):])
			gustav = gustav[:strings.Index(gustav, "</node>")+len("</node>")]
			assert.Contains(t, string(data), gustav, "the game's entry is kept byte for byte")
			assert.Contains(t, string(data), `<attribute id="UUID" type="guid" value="`+uuidA+`"/>`)

			// A second deploy changes nothing.
			again := f.deploy(t)
			require.Len(t, again.LoadOrders, 1)
			assert.Equal(t, core.PluginListUnchanged, again.LoadOrders[0].Status)
		})
	}
}

// The profile's pak order decides lmm's entries' order; a pak it does not
// order goes at the end. Neither moves the entries lmm did not add.
func TestModSettings_FollowsTheProfilesPakOrder(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	f.writeList(t, lariantest.GustavX, userEntry)
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	f.mod(t, "b", map[string][]byte{"ModB.pak": lariantest.ModPak("ModB", "Mod B", uuidB)})
	f.mod(t, "c", map[string][]byte{"ModC.pak": lariantest.ModPak("ModC", "Mod C", uuidC)})
	f.setOrder(t, "modc.pak", "ModA.pak") // names compare case-insensitively

	f.deploy(t)
	assert.Equal(t, []string{"GustavX", "A user's mod", "Mod C", "Mod A", "Mod B"}, f.entries(t))

	f.setOrder(t, "ModB.pak", "ModC.pak", "ModA.pak")
	f.deploy(t)
	assert.Equal(t, []string{"GustavX", "A user's mod", "Mod B", "Mod C", "Mod A"}, f.entries(t))
}

// An override pak - no meta.lsx - loads without an entry and is never
// listed (Vortex's rule); a pak whose meta.lsx cannot be read is left out
// with a warning saying so.
func TestModSettings_OverrideAndUnreadablePaks(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	f.writeList(t, lariantest.GustavX)
	zstd := lariantest.Pak(18, 0, lariantest.Member{Name: "Mods/Z/meta.lsx", Data: lariantest.MetaLSX("Z", "Z", uuidC, "1"), Method: lariantest.MethodZstd})
	f.mod(t, "a", map[string][]byte{
		"ModA.pak":     lariantest.ModPak("ModA", "Mod A", uuidA),
		"Override.pak": lariantest.OverridePak(),
		"Zstd.pak":     zstd,
		"Junk.pak":     []byte("not a pak"),
	})
	res := f.deploy(t)
	assert.Equal(t, []string{"GustavX", "Mod A"}, f.entries(t))
	require.Len(t, res.LoadOrders, 1)
	var files []string
	for _, fd := range res.LoadOrders[0].Findings {
		files = append(files, fd.File)
		assert.Equal(t, core.LoadOrderFindingPakUnreadable, fd.Code)
		assert.Equal(t, core.PluginSeverityWarning, fd.Severity)
	}
	assert.ElementsMatch(t, []string{"Zstd.pak", "Junk.pak"}, files)
	joined := strings.Join(res.Warnings, "\n")
	assert.Contains(t, joined, "zstd")
	assert.NotContains(t, joined, "Override.pak", "an override pak needs no entry, so it is nothing to warn about")
}

// The game writes modsettings.lsx on its first run; lmm does not invent
// one (which Gustav entry it needs depends on the game's patch), and says
// so.
func TestModSettings_MissingListIsAWarningNotAWrite(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	res := f.deploy(t)
	require.Len(t, res.LoadOrders, 1)
	assert.Equal(t, core.PluginListSkipped, res.LoadOrders[0].Status)
	require.NotEmpty(t, res.LoadOrders[0].Findings)
	assert.Equal(t, core.LoadOrderFindingListMissing, res.LoadOrders[0].Findings[0].Code)
	assert.Contains(t, strings.Join(res.Warnings, "\n"), "run the game once")
	assertAbsent(t, f.listPath(), "lmm never creates the list")
}

// A pre-Patch 7 list (a ModOrder node) is not one lmm writes.
func TestModSettings_PrePatch7ListIsNotWritten(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	require.NoError(t, os.MkdirAll(f.profileDir(), 0o755))
	v6 := strings.Replace(string(lariantest.ModSettings(lariantest.GustavX)), `<node id="Mods">`, `<node id="ModOrder"><children/></node><node id="Mods">`, 1)
	require.NoError(t, os.WriteFile(f.listPath(), []byte(v6), 0o644))
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	res := f.deploy(t)
	require.Len(t, res.LoadOrders, 1)
	assert.Equal(t, core.PluginListSkipped, res.LoadOrders[0].Status)
	assert.Equal(t, v6, readFileString(t, f.listPath()))
}

// Uninstalling a mod takes its entry out and leaves the rest.
func TestModSettings_UninstallRemovesOnlyLmmsEntry(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	f.writeList(t, lariantest.GustavX, userEntry)
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	f.mod(t, "b", map[string][]byte{"ModB.pak": lariantest.ModPak("ModB", "Mod B", uuidB)})
	f.deploy(t)
	require.Equal(t, []string{"GustavX", "A user's mod", "Mod A", "Mod B"}, f.entries(t))

	_, err := f.svc.DisableMod(context.Background(), f.game, "default", "src", "a")
	require.NoError(t, err)
	assert.Equal(t, []string{"GustavX", "A user's mod", "Mod B"}, f.entries(t))
}

// A purge puts back the list the user had: byte for byte when nothing else
// changed it, and keeping an entry the user (or the in-game manager) added
// since.
func TestModSettings_PurgeRestoresTheList(t *testing.T) {
	t.Run("unchanged", func(t *testing.T) {
		f := newBG3Fixture(t, domain.LinkCopy)
		before := f.writeList(t, lariantest.GustavX, userEntry)
		f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
		f.deploy(t)
		require.NotEqual(t, string(before), readFileString(t, f.listPath()))

		res := f.purge(t)
		require.Len(t, res.LoadOrders, 1, "%+v", res.LoadOrders)
		assert.Equal(t, core.PluginListRestored, res.LoadOrders[0].Status)
		assert.Equal(t, string(before), readFileString(t, f.listPath()))
	})
	t.Run("an entry added since", func(t *testing.T) {
		f := newBG3Fixture(t, domain.LinkCopy)
		f.writeList(t, lariantest.GustavX)
		f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
		f.deploy(t)
		// The in-game manager adds a mod of its own after lmm's.
		data := readFileString(t, f.listPath())
		s, err := larian.ParseModSettings([]byte(data))
		require.NoError(t, err)
		added := s.Render(append(append([]larian.ModEntry(nil), s.Entries...), larian.NewModEntry(larian.Module{UUID: uuidUser, Folder: "UserMod", Name: "A user's mod", Version64: "7"})))
		require.NoError(t, os.WriteFile(f.listPath(), added, 0o644))

		f.purge(t)
		assert.Equal(t, []string{"GustavX", "A user's mod"}, f.entries(t))
	})
}

// The game resets modsettings.lsx when it finds a mod it cannot load:
// verify reports each of lmm's paks the list dropped, and --fix lists them
// again.
func TestVerify_ModSettingsDrift(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	f.writeList(t, lariantest.GustavX)
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	f.mod(t, "b", map[string][]byte{"ModB.pak": lariantest.ModPak("ModB", "Mod B", uuidB)})
	f.deploy(t)

	f.writeList(t, lariantest.GustavX) // the game's reset

	ctx := context.Background()
	res, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	var drift []core.VerifyFinding
	for _, fd := range res.Result.Findings {
		if fd.Status == core.VerifyStatusLoadOrderDrift {
			drift = append(drift, fd)
		}
	}
	require.Len(t, drift, 2, "%+v", res.Result.Findings)
	assert.Equal(t, "modsettings.lsx", drift[0].FileID)
	assert.Contains(t, drift[0].Note, "ModA.pak is not in modsettings.lsx")
	assert.True(t, drift[0].Fixable)

	fixed, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true, Fix: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, findingWithStatus(fixed.Result, core.VerifyStatusFixedLoadOrderDrift), "%+v", fixed.Result.Findings)
	assert.Equal(t, []string{"GustavX", "Mod A", "Mod B"}, f.entries(t))
}

// `lmm load-order list` for Baldur's Gate 3: the paks in their order, the
// entries lmm did not add as external, and a report that says the list
// enables.
func TestModSettings_Report(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	f.writeList(t, lariantest.GustavX, userEntry)
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	f.deploy(t)

	report, err := f.svc.FileLoadOrders(context.Background(), f.game, "default")
	require.NoError(t, err)
	require.Len(t, report.Orders, 1)
	o := report.Orders[0]
	assert.True(t, o.Enables)
	assert.True(t, o.Active)
	assert.False(t, o.Recorded)
	assert.Equal(t, f.listPath(), o.ListFile)
	var names []string
	for _, e := range o.Files {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"GustavX", "A user's mod", "ModA.pak"}, names)
	assert.Equal(t, core.LoadOrderPlacedExternal, o.Files[0].Placement)
	assert.True(t, o.Files[2].Managed)
	assert.Equal(t, "a", o.Files[2].ModID)
}

// A move records the order and rewrites modsettings.lsx at once.
func TestModSettings_MoveWritesTheList(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	f.writeList(t, lariantest.GustavX)
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	f.mod(t, "b", map[string][]byte{"ModB.pak": lariantest.ModPak("ModB", "Mod B", uuidB)})
	f.deploy(t)
	require.Equal(t, []string{"GustavX", "Mod A", "Mod B"}, f.entries(t))

	ctx := context.Background()
	plan, err := f.svc.PlanFileLoadOrderEdit(ctx, f.game, "default", core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "ModB.pak", To: core.LoadOrderMoveFirst})
	require.NoError(t, err)
	_, err = f.svc.ApplyFileLoadOrderEdit(ctx, f.game, plan)
	require.NoError(t, err)
	assert.Equal(t, []string{"GustavX", "Mod B", "Mod A"}, f.entries(t), "lmm's paks never go above the game's own entry")

	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"ModB.pak", "ModA.pak"}, profile.LoadOrders[modtypes.PakOrderID])

	// Moving an entry lmm did not add is refused.
	_, err = f.svc.PlanFileLoadOrderEdit(ctx, f.game, "default", core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "GustavX", To: core.LoadOrderMoveLast})
	require.ErrorIs(t, err, core.ErrLoadOrderFileNotOwned)

	// A reset clears the profile's order but keeps the paks enabled.
	plan, err = f.svc.PlanFileLoadOrderEdit(ctx, f.game, "default", core.FileLoadOrderEdit{Op: core.LoadOrderOpReset})
	require.NoError(t, err)
	_, err = f.svc.ApplyFileLoadOrderEdit(ctx, f.game, plan)
	require.NoError(t, err)
	assert.Equal(t, []string{"GustavX", "Mod B", "Mod A"}, f.entries(t), "the list keeps the order it has")
}

// Wine matches names case-insensitively: a lower-case playerprofiles
// folder and a MODSETTINGS.LSX are the game's.
func TestModSettings_MatchesTheListCaseInsensitively(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	dir := filepath.Join(f.prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Larian Studios", "Baldur's Gate 3", "playerprofiles", "public")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	list := filepath.Join(dir, "MODSETTINGS.LSX")
	require.NoError(t, os.WriteFile(list, lariantest.ModSettings(lariantest.GustavX), 0o644))
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	f.deploy(t)
	assert.Contains(t, readFileString(t, list), uuidA)
	assertAbsent(t, f.listPath(), "no second list beside the game's")
}

// A list that is a link is never read or written through.
func TestModSettings_RefusesAListThatIsALink(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	require.NoError(t, os.MkdirAll(f.profileDir(), 0o755))
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.lsx")
	original := lariantest.ModSettings(lariantest.GustavX)
	require.NoError(t, os.WriteFile(elsewhere, original, 0o644))
	require.NoError(t, os.Symlink(elsewhere, f.listPath()))
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	res := f.deploy(t)
	require.Len(t, res.LoadOrders, 1)
	assert.Equal(t, core.PluginListSkipped, res.LoadOrders[0].Status)
	assert.Equal(t, string(original), readFileString(t, elsewhere))
}

// Disabling every pak lmm deployed hands the list back: lmm's entries go,
// the rest stay, and lmm keeps no copy any longer.
func TestModSettings_DisablingEveryPakHandsTheListBack(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	before := f.writeList(t, lariantest.GustavX, userEntry)
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	f.deploy(t)
	_, err := f.svc.DisableMod(context.Background(), f.game, "default", "src", "a")
	require.NoError(t, err)
	assert.Equal(t, string(before), readFileString(t, f.listPath()))
	assert.Empty(t, bg3Originals(t, f), "nothing of lmm's is left to keep a copy for")
}

// A user's entry between lmm's keeps its place among them, and an entry
// for a module lmm's pak declares that the user had already is lmm's from
// then on (it moves with the profile's order).
func TestModSettings_ExternalEntriesKeepTheirPlace(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	modB := lariantest.Entry{Folder: "ModB", Name: "Mod B", UUID: uuidB, Version64: "36028797018963968"}
	f.writeList(t, lariantest.GustavX, modB, userEntry)
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	f.mod(t, "b", map[string][]byte{"ModB.pak": lariantest.ModPak("ModB", "Mod B", uuidB)})
	f.deploy(t)
	assert.Equal(t, []string{"GustavX", "Mod B", "A user's mod", "Mod A"}, f.entries(t))

	f.setOrder(t, "ModA.pak", "ModB.pak")
	f.deploy(t)
	assert.Equal(t, []string{"GustavX", "Mod A", "Mod B", "A user's mod"}, f.entries(t), "the user's entry stays after the pak it followed")
}

// An entry whose fields no longer match its pak's meta.lsx - the list
// still names an older version of the mod - is rewritten from the pak; one
// that matches is left byte for byte.
func TestModSettings_AnOutdatedEntryIsRewritten(t *testing.T) {
	f := newBG3Fixture(t, domain.LinkCopy)
	older := lariantest.Entry{Folder: "ModA", Name: "Mod A", UUID: uuidA, Version64: "1"}
	f.writeList(t, lariantest.GustavX, older)
	f.mod(t, "a", map[string][]byte{"ModA.pak": lariantest.ModPak("ModA", "Mod A", uuidA)})
	res := f.deploy(t)
	require.Len(t, res.LoadOrders, 1)
	assert.Equal(t, core.PluginListWritten, res.LoadOrders[0].Status)
	data := readFileString(t, f.listPath())
	assert.Contains(t, data, `<attribute id="Version64" type="int64" value="36028797018963968"/>`+"\n                        </node>\n                    </children>")
	assert.Equal(t, 1, strings.Count(data, uuidA), "one entry for the module")

	again := f.deploy(t)
	assert.Equal(t, core.PluginListUnchanged, again.LoadOrders[0].Status, "a matching entry is left as it is")
}

func bg3Originals(t *testing.T, f *bg3Fixture) []prefixOriginalRow {
	t.Helper()
	a := &archiveFixture{dataDir: f.dataDir, game: f.game}
	return a.originals(t)
}

package core_test

// #607 C2b review, blocker 1: a mod's stated type reaches the deploy and
// the row whichever way profile apply gets its files - downloaded, or
// already in the cache (a mod uninstalled with its cache kept, then
// re-applied) - and a snapshot restore, which converges through the same
// apply, keeps it too.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bethesda"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type typedFlowFixture struct {
	svc  *core.Service
	game *domain.Game
	src  *mockSourceWithDownloads
}

// newTypedFlowFixture is a Bethesda-shaped game whose default profile
// lists one script extender, stated as modType, that the source serves.
func newTypedFlowFixture(t *testing.T, modType string) *typedFlowFixture {
	t.Helper()
	svc, _ := newOriginalsService(t)
	svc.RegisterAdapter(bethesda.New())
	src := newMockSourceWithDownloads("src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	install := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", InstallPath: install, ModPath: filepath.Join(install, "Data"),
		LinkMethod: domain.LinkSymlink, Adapter: "bethesda", SourceIDs: map[string]string{"src": "g1"}}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	src.AddMod("g1", &domain.Mod{ID: "ext", SourceID: "src", Name: "Extender", Version: "1.0", GameID: "g1"})
	src.AddDownload("1", createTestZipBytes(t, map[string]string{
		"extender_readme.txt":       "readme",
		"Data/Scripts/extender.pex": "script",
	}))
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default",
		domain.ModReference{SourceID: "src", ModID: "ext", Version: "1.0", FileIDs: []string{"1"}, ModType: modType}))
	return &typedFlowFixture{svc: svc, game: game, src: src}
}

func (f *typedFlowFixture) apply(t *testing.T) *core.ProfileApplyResult {
	t.Helper()
	plan, err := f.svc.PlanProfileApply(context.Background(), f.game, "default")
	require.NoError(t, err)
	res, err := f.svc.ApplyProfileApply(context.Background(), f.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	return res
}

func (f *typedFlowFixture) assertAtTheRoot(t *testing.T, msg string) {
	t.Helper()
	assert.Equal(t, "readme", readFileString(t, filepath.Join(f.game.InstallPath, "extender_readme.txt")), msg)
	assertAbsent(t, filepath.Join(f.game.ModPath, "extender_readme.txt"), msg)
	row, err := f.svc.GetInstalledMod(context.Background(), "src", "ext", f.game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "dinput", row.ModType, msg+": the row keeps the stated type")
}

func (f *typedFlowFixture) uninstallKeepingTheCache(t *testing.T) {
	t.Helper()
	_, err := f.svc.UninstallMod(context.Background(), f.game, "default", "src", "ext", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	assertAbsent(t, filepath.Join(f.game.InstallPath, "extender_readme.txt"), "uninstalled")
	// The uninstall drops the ref; the profile lists it again, typed.
	require.NoError(t, f.svc.NewProfileManager().AddMod(context.Background(), f.game.ID, "default",
		domain.ModReference{SourceID: "src", ModID: "ext", Version: "1.0", FileIDs: []string{"1"}, ModType: "dinput"}))
}

func TestProfileApply_StatedTypeDownloadedOrCached(t *testing.T) {
	f := newTypedFlowFixture(t, "dinput")
	f.apply(t)
	f.assertAtTheRoot(t, "downloaded")
	downloads := f.src.DownloadCount()

	f.uninstallKeepingTheCache(t)
	f.apply(t)
	assert.Equal(t, downloads, f.src.DownloadCount(), "the re-apply deploys from the cache")
	f.assertAtTheRoot(t, "from the cache")
}

func TestProfileApply_UnknownStatedTypeIsRefusedFromTheCacheToo(t *testing.T) {
	f := newTypedFlowFixture(t, "dinput")
	f.apply(t)
	_, err := f.svc.UninstallMod(context.Background(), f.game, "default", "src", "ext", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	require.NoError(t, f.svc.NewProfileManager().AddMod(context.Background(), f.game.ID, "default",
		domain.ModReference{SourceID: "src", ModID: "ext", Version: "1.0", FileIDs: []string{"1"}, ModType: "nosuchtype"}))

	plan, err := f.svc.PlanProfileApply(context.Background(), f.game, "default")
	require.NoError(t, err)
	res, _ := f.svc.ApplyProfileApply(context.Background(), f.game, plan, core.ProfileApplyOptions{}, nil)
	require.NotNil(t, res)
	assert.NotEmpty(t, res.Failed, "a type the adapter does not know is refused, cached or not")
	assertAbsent(t, filepath.Join(f.game.ModPath, "extender_readme.txt"), "never deployed under Data")
}

func TestSnapshotRestore_KeepsTheStatedType(t *testing.T) {
	f := newTypedFlowFixture(t, "dinput")
	f.apply(t)
	_, err := f.svc.CreateSnapshot(context.Background(), f.game, "default", "snap1")
	require.NoError(t, err)
	_, err = f.svc.UninstallMod(context.Background(), f.game, "default", "src", "ext", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)

	plan, err := f.svc.PlanSnapshotRestore(context.Background(), f.game, "snap1")
	require.NoError(t, err)
	_, err = f.svc.ApplySnapshotRestore(context.Background(), f.game, plan, core.SnapshotRestoreOptions{}, nil)
	require.NoError(t, err)
	f.assertAtTheRoot(t, "restored from the snapshot, from the cache")
}

// #607 C2b review, should-fix 2: a mod another profile already has is
// copied into the importing (or switched-to) profile with THAT profile's
// reference's stated type - the other row's own type, typed or not, never
// decides where the mod deploys.
func TestImportAndSwitch_CopiedRowTakesTheRefsType(t *testing.T) {
	for _, tc := range []struct{ name, have, want string }{
		{"an untyped row, a typed ref", "", "dinput"},
		{"a typed row, an untyped ref", "dinput", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTypedFlowFixture(t, tc.have)
			f.apply(t)

			doc := "name: other\ngame_id: g1\nmods:\n  - {source_id: src, mod_id: ext, version: \"1.0\", file_ids: [\"1\"]" +
				map[bool]string{true: ", mod_type: " + tc.want, false: ""}[tc.want != ""] + "}\n"
			plan, err := f.svc.PlanImport(context.Background(), f.game, []byte(doc))
			require.NoError(t, err)
			require.Len(t, plan.AlreadyCached, 1, "the row is borrowed from the other profile")
			_, err = f.svc.ApplyImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
			require.NoError(t, err)
			row, err := f.svc.GetInstalledMod(context.Background(), "src", "ext", f.game.ID, "other")
			require.NoError(t, err)
			assert.Equal(t, tc.want, row.ModType, "the import's own reference decides")

			// Off in the first profile, so the switch has it to deploy:
			// it deploys by the imported row's type.
			_, err = f.svc.DisableMod(context.Background(), f.game, "default", "src", "ext")
			require.NoError(t, err)
			sp, err := f.svc.PlanProfileSwitch(context.Background(), f.game, "other")
			require.NoError(t, err)
			_, err = f.svc.ApplyProfileSwitch(context.Background(), f.game, sp, nil)
			require.NoError(t, err)
			atRoot := filepath.Join(f.game.InstallPath, "extender_readme.txt")
			inData := filepath.Join(f.game.ModPath, "extender_readme.txt")
			if tc.want == "dinput" {
				assert.FileExists(t, atRoot)
				assertAbsent(t, inData, "typed: the game root")
			} else {
				assert.FileExists(t, inData)
				assertAbsent(t, atRoot, "untyped: per-file routing puts it under Data")
			}
		})
	}
}

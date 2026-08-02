package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// TestEnabledExmodzSources_OrderMatchesProfileLoadOrderAndSkipsDisabled
// proves enabledExmodzSources returns retained exmodz files in PROFILE
// LOAD ORDER (merge-application order), skips disabled mods entirely, and
// skips a mod's fileIDs that have no retained source (a plain .pak).
func TestEnabledExmodzSources_OrderMatchesProfileLoadOrderAndSkipsDisabled(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "icarus", ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	gameCache := svc.GetGameCache(game)

	seedMod := func(sourceID, modID, version string, fileIDs []string, enabled bool) {
		for _, fileID := range fileIDs {
			// Only an "exmodz"-named fileID gets a retained source, mirroring
			// the real ingest shape (Task 2/3): a plain .pak fileID is never
			// retained, so enabledExmodzSources must skip it via the
			// os.Stat check, not just by naming convention.
			if !strings.Contains(fileID, "exmodz") {
				continue
			}
			require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(fileID), []byte("exmodz-"+modID+"-"+fileID)))
		}
		require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
			Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: modID, Version: version, GameID: game.ID},
			ProfileName:  "default",
			Enabled:      enabled,
			FileIDs:      fileIDs,
			UpdatePolicy: domain.UpdateNotify,
		}))
	}

	// mixedMod has one exmodz fileID and one plain-pak fileID (no retained
	// source for the latter) - only the exmodz one should be included.
	seedMod("icarus", "second-mod", "1.0", []string{"exmodz-file", "pak-file"}, true)
	seedMod("icarus", "first-mod", "1.0", []string{"exmodz-file"}, true)
	seedMod("icarus", "disabled-mod", "1.0", []string{"exmodz-file"}, false)

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	// Profile load order: first-mod, then second-mod (disabled-mod
	// intentionally omitted - membership in Profile.Mods, not just an
	// Enabled DB row, is what GetInstalledModsInProfileOrder requires).
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "icarus", ModID: "first-mod", Version: "1.0", FileIDs: []string{"exmodz-file"}}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "icarus", ModID: "second-mod", Version: "1.0", FileIDs: []string{"exmodz-file", "pak-file"}}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "icarus", ModID: "disabled-mod", Version: "1.0", FileIDs: []string{"exmodz-file"}}))

	sources, err := svc.EnabledExmodzSourcesForTest(game, "default")
	require.NoError(t, err)
	require.Len(t, sources, 2, "disabled-mod excluded; second-mod's plain-pak fileID excluded")
	require.Equal(t, "icarus:first-mod", sources[0].ModRef)
	require.Equal(t, "icarus:second-mod", sources[1].ModRef)

	data, err := os.ReadFile(sources[0].ExmodzPath)
	require.NoError(t, err)
	require.Equal(t, "exmodz-first-mod-exmodz-file", string(data))
}

// newMergedPakTestGame builds a DeployCompile game with a registered merge
// compiler and an installed base pak - shared setup for syncMergedPak
// tests. Returns the service, game, and the base pak's own path (so a test
// can rewrite it to simulate a base-pak refresh).
func newMergedPakTestGame(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc := newFlowsTestService(t)
	src := &fakeCompilerSource{}
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	return svc, game, basePak
}

// seedEnabledExmodzMod installs an ENABLED mod with a retained exmodz file,
// via svc.SaveInstalledMod + profile UpsertMod (matching the real ingest
// shape Task 2/3 produce - a cache entry with a retained source and no
// deployment members).
func seedEnabledExmodzMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version, fileID string, exmodzContent []byte) {
	t.Helper()
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(fileID), exmodzContent))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: modID, Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: version, FileIDs: []string{fileID}}))
}

// TestSyncMergedPak_GeneratesAndDeploys is the happy path: one enabled
// exmodz mod, no merged pak yet - syncMergedPak must generate one and
// deploy it into the game directory.
func TestSyncMergedPak_GeneratesAndDeploys(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-exmodz-bytes"))

	warnings, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.Empty(t, warnings)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-exmodz-bytes", string(data), "fakeCompilerSource's MergeCompile concatenates source bytes - see its own definition")
}

// TestSyncMergedPak_NoOpWhenUnchanged proves the fingerprint gate actually
// gates: calling syncMergedPak twice with nothing changed must not
// recompile (fakeCompilerSource.compileCalls stays at 1).
func TestSyncMergedPak_NoOpWhenUnchanged(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-exmodz-bytes"))

	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	srcRaw, err := svc.GetSource("fake-compiler")
	require.NoError(t, err)
	src, ok := srcRaw.(*fakeCompilerSource)
	require.True(t, ok)
	require.Equal(t, 1, src.compileCalls)

	_, err = svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.Equal(t, 1, src.compileCalls, "an unchanged fingerprint must not trigger a second merge")
}

// TestSyncMergedPak_RegeneratesOnModEnable proves enabling a SECOND mod
// (the mod-set changing) triggers regeneration.
func TestSyncMergedPak_RegeneratesOnModEnable(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	warnings, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.Empty(t, warnings)

	srcRaw, err := svc.GetSource("fake-compiler")
	require.NoError(t, err)
	src, ok := srcRaw.(*fakeCompilerSource)
	require.True(t, ok)
	require.Equal(t, 2, src.compileCalls, "a mod-set change must trigger a second merge")

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-byteswolf-bytes", string(data), "the merged pak must now reflect BOTH mods")
}

// TestSyncMergedPak_ZeroEnabledMods_UninstallsExistingPak proves the
// uninstall-to-zero case: disabling the LAST enabled exmodz mod must
// remove any previously-deployed merged pak from the game directory.
func TestSyncMergedPak_ZeroEnabledMods_UninstallsExistingPak(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "precondition: the merged pak must exist before disabling")

	require.NoError(t, svc.SetModEnabled("fake-compiler", "bear-mount", game.ID, "default", false))

	_, err = svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	_, err = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err), "disabling the last exmodz mod must remove the deployed merged pak")
}

// TestSyncMergedPak_RegeneratesOnBaseHashChange proves a base-pak refresh
// (the "Friday problem", generalized from #196 to the merged model) still
// triggers regeneration.
func TestSyncMergedPak_RegeneratesOnBaseHashChange(t *testing.T) {
	svc, game, basePak := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	// Rewrite the base pak with different content - a new IndexHash.
	writeFakeBasePakWithTable(t, basePak, map[string][]byte{"AI/D_Other.json": []byte(`{"Rows":[{"Name":"x","V":1}]}`)})

	_, err = svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	srcRaw, err := svc.GetSource("fake-compiler")
	require.NoError(t, err)
	src, ok := srcRaw.(*fakeCompilerSource)
	require.True(t, ok)
	require.Equal(t, 2, src.compileCalls, "a base pak change must trigger a second merge")
}

// TestSyncMergedPak_NonCompileGame_NoOp: a DeployExtract/DeployCopy game has
// no merged-pak concept at all - syncMergedPak must no-op unconditionally
// (cheap enough to call from every mutation flow regardless of game type).
func TestSyncMergedPak_NonCompileGame_NoOp(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "skyrim-se", ModPath: t.TempDir(), DeployMode: domain.DeployExtract}
	require.NoError(t, svc.AddGame(game))

	warnings, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.Empty(t, warnings)
}

// TestSyncMergedPak_AssetCollisionWarningSurfaces proves MergeCompile's own
// warnings (Task 1) propagate all the way out of syncMergedPak.
func TestSyncMergedPak_AssetCollisionWarningSurfaces(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	srcRaw, err := svc.GetSource("fake-compiler")
	require.NoError(t, err)
	src, ok := srcRaw.(*fakeCompilerSource)
	require.True(t, ok)
	src.mergeWarnings = []string{"asset collision: fixture warning"}
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	warnings, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.Equal(t, []string{"asset collision: fixture warning"}, warnings)
}

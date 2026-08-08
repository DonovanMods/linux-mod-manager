package core_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// TestEnabledMergeSources_OrderMatchesProfileLoadOrderAndSkipsDisabled
// proves enabledMergeSources returns retained merge-source files in PROFILE
// LOAD ORDER (merge-application order), skips disabled mods entirely, and
// skips a mod's fileIDs that have no retained source (a plain .pak).
func TestEnabledMergeSources_OrderMatchesProfileLoadOrderAndSkipsDisabled(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "icarus", ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	gameCache := svc.GetGameCache(game)

	seedMod := func(sourceID, modID, version string, fileIDs []string, enabled bool) {
		for _, fileID := range fileIDs {
			// Only an "exmodz"-named fileID gets a retained source, mirroring
			// the real ingest shape (Task 2/3): a plain .pak fileID is never
			// retained, so enabledMergeSources must skip it via the
			// os.Stat check, not just by naming convention.
			if !strings.Contains(fileID, "exmodz") {
				continue
			}
			require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(fileID), []byte("exmodz-"+modID+"-"+fileID)))
		}
		require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
			Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: modID + " (display)", Version: version, GameID: game.ID},
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

	sources, err := svc.EnabledMergeSourcesForTest(game, "default")
	require.NoError(t, err)
	require.Len(t, sources, 2, "disabled-mod excluded; second-mod's plain-pak fileID excluded")
	require.Equal(t, "icarus:first-mod", sources[0].ModRef)
	require.Equal(t, "icarus:second-mod", sources[1].ModRef)
	require.Equal(t, "first-mod (display)", sources[0].ModName, "display name must flow into MergeSource for user-facing warnings")
	require.Equal(t, "second-mod (display)", sources[1].ModName)

	data, err := os.ReadFile(sources[0].SourcePath)
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

	warnings, err := svc.SyncMergedPak(context.Background(), game, "default")
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

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	srcRaw, err := svc.GetSource("fake-compiler")
	require.NoError(t, err)
	src, ok := srcRaw.(*fakeCompilerSource)
	require.True(t, ok)
	require.Equal(t, 1, src.compileCalls)

	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	require.Equal(t, 1, src.compileCalls, "an unchanged fingerprint must not trigger a second merge")
}

// TestSyncMergedPak_RegeneratesOnModEnable proves enabling a SECOND mod
// (the mod-set changing) triggers regeneration.
func TestSyncMergedPak_RegeneratesOnModEnable(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	warnings, err := svc.SyncMergedPak(context.Background(), game, "default")
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

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "precondition: the merged pak must exist before disabling")

	require.NoError(t, svc.SetModEnabled("fake-compiler", "bear-mount", game.ID, "default", false))

	_, err = svc.SyncMergedPak(context.Background(), game, "default")
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

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	// Rewrite the base pak with different content - a new IndexHash.
	writeFakeBasePakWithTable(t, basePak, map[string][]byte{"AI/D_Other.json": []byte(`{"Rows":[{"Name":"x","V":1}]}`)})

	_, err = svc.SyncMergedPak(context.Background(), game, "default")
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

	warnings, err := svc.SyncMergedPak(context.Background(), game, "default")
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

	warnings, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	require.Equal(t, []string{"asset collision: fixture warning"}, warnings)
}

// failOnceLinker fails a single Deploy or Undeploy call the FIRST time it's
// invoked, then delegates to the real linker for every call after - a
// fault-injection double for the #221 partial-reconcile convergence
// guarantee tests below, proving a transient installer failure gets
// retried (and converges) on the next reconcile pass rather than being
// permanently masked. Mirrors installer_test.go's failingLinker/
// conditionalFailingLinker convention (both cover only Deploy, for
// rollback tests wanting PERMANENT failure past a threshold); this one
// covers both Deploy and Undeploy and fails exactly once.
type failOnceLinker struct {
	linker.Linker
	failDeployOnce   bool
	failUndeployOnce bool
	deployCalls      int
	undeployCalls    int
}

func (f *failOnceLinker) Deploy(src, dst string) error {
	f.deployCalls++
	if f.failDeployOnce {
		f.failDeployOnce = false
		return fmt.Errorf("simulated deploy failure")
	}
	return f.Linker.Deploy(src, dst)
}

func (f *failOnceLinker) Undeploy(dst string) error {
	f.undeployCalls++
	if f.failUndeployOnce {
		f.failUndeployOnce = false
		return fmt.Errorf("simulated undeploy failure")
	}
	return f.Linker.Undeploy(dst)
}

// TestReconcilePakManifests_ConvertedPath_UninstallFailureRetries proves
// the #221 fix for the "converted" branch: when Uninstall fails, the
// manifest must NOT have already flipped to merged-claimed (members=nil) -
// that would be a double-apply (raw pak still on disk/undetermined AND
// claimed as merged-owned). The next reconcile pass must retry Uninstall
// and only then converge.
func TestReconcilePakManifests_ConvertedPath_UninstallFailureRetries(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	game.ConvertPaks = true
	seedEnabledPakMod(t, svc, game, "fake-compiler", "goodmod", "1.0", "pak", []byte("good-bytes"))

	gameCache := svc.GetGameCache(game)
	faulty := &failOnceLinker{Linker: linker.New(game.LinkMethod), failUndeployOnce: true}
	inst := core.NewInstaller(gameCache, faulty, nil)

	// First reconcile: goodmod participates (converted) - Uninstall fails.
	_, err := svc.ReconcilePakManifestsForTest(context.Background(), game, "default", inst, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "undeploying raw pak")

	manifests, err := gameCache.FileManifests(game.ID, "fake-compiler", "goodmod", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"goodmod.pak"}, manifests["pak"].Members,
		"a failed Uninstall must NOT flip the manifest to merged-claimed (would be a double-apply)")
	require.Equal(t, 1, faulty.undeployCalls)

	// Second reconcile (the "next sync"): must retry and converge.
	_, err = svc.ReconcilePakManifestsForTest(context.Background(), game, "default", inst, nil)
	require.NoError(t, err)
	require.Equal(t, 2, faulty.undeployCalls, "the retry must call Undeploy again")

	manifests, err = gameCache.FileManifests(game.ID, "fake-compiler", "goodmod", "1.0")
	require.NoError(t, err)
	require.Empty(t, manifests["pak"].Members, "the retry must converge to merged-claimed")
}

// TestReconcilePakManifests_TwoPakFileIDs_EachClaimsOwnMember proves the
// #241 fix: the raw-fallback branch must claim ONLY the member(s) belonging
// to the fileID being marked, never the entry-wide cache union. No real mod
// carries two pak-kind fileIDs today, so this constructs the synthetic shape
// the assumption breaks under: one mod, two pak fileIDs, each with its own
// retained source and deployable copy, both manifests in the post-convert
// state (members=nil - the state a flip BACK to raw starts from, where the
// ingest-time attribution has already been erased). Pre-#241, reconcile
// marked each fileID with gameCache.ListFiles' full union, double-claiming
// the sibling's member.
//
// Member names are deliberately unrelated to the fileIDs (the download path
// names members after DownloadableFile.FileName, which bears no relation to
// the fileID), and both paks are the SAME size with different content, so
// only content identity with the fileID's own retained source can attribute
// them - not name matching, not size matching.
func TestReconcilePakManifests_TwoPakFileIDs_EachClaimsOwnMember(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	// game.ConvertPaks stays false: every pak fileID takes the raw-fallback
	// branch (opted out at game level), no failedByRef needed.

	const (
		sourceID   = "fake-compiler"
		modID      = "twopak"
		version    = "1.0"
		mainFileID = "pak"      // download-path shape: the literal icarus fileID
		liteFileID = "lite.pak" // a hypothetical second pak variant ("lite" build)
	)
	// Same length, different bytes - size alone cannot tell them apart.
	mainContent := []byte("main-pak-bytes")
	liteContent := []byte("lite-pak-bytes")

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(mainFileID), mainContent))
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, "MainMod.pak", mainContent))
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(liteFileID), liteContent))
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, "LiteMod.pak", liteContent))

	versionDir := gameCache.ModPath(game.ID, sourceID, modID, version)
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, mainFileID, nil))
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, liteFileID, nil))

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: modID, Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{mainFileID, liteFileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: version, FileIDs: []string{mainFileID, liteFileID}}))

	gc := svc.GetGameCache(game)
	inst := core.NewInstaller(gc, linker.New(game.LinkMethod), nil)
	_, err := svc.ReconcilePakManifestsForTest(context.Background(), game, "default", inst, nil)
	require.NoError(t, err)

	manifests, err := gameCache.FileManifests(game.ID, sourceID, modID, version)
	require.NoError(t, err)
	require.Equal(t, []string{"MainMod.pak"}, manifests[mainFileID].Members,
		"each pak fileID's manifest must claim exactly its own member, not the entry-wide union (#241)")
	require.Equal(t, []string{"LiteMod.pak"}, manifests[liteFileID].Members,
		"the second pak fileID must not double-claim the first's member (#241)")

	// Both raw paks must still actually deploy - narrowing the claim must
	// not narrow the deployed set.
	_, err = os.Stat(filepath.Join(game.ModPath, "MainMod.pak"))
	require.NoError(t, err, "the first fileID's raw pak must be deployed")
	_, err = os.Stat(filepath.Join(game.ModPath, "LiteMod.pak"))
	require.NoError(t, err, "the second fileID's raw pak must be deployed")
}

// TestReconcilePakManifests_RawFallbackPath_InstallFailureRetries proves
// the #221 fix for the raw-fallback branch: a manifest that already
// records the target ("raw") member shape is not trusted as proof of an
// actual deploy - reconcile must confirm the file is really on disk before
// treating it as converged, so a failed Install gets retried by the next
// reconcile pass instead of being silently believed done forever.
func TestReconcilePakManifests_RawFallbackPath_InstallFailureRetries(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	game.ConvertPaks = true
	seedEnabledPakMod(t, svc, game, "fake-compiler", "badmod", "1.0", "pak", []byte("bad-bytes"))

	gameCache := svc.GetGameCache(game)
	faulty := &failOnceLinker{Linker: linker.New(game.LinkMethod), failDeployOnce: true}
	inst := core.NewInstaller(gameCache, faulty, nil)
	failedByRef := map[string]string{"fake-compiler:badmod": "boom"}

	deployedPath := filepath.Join(game.ModPath, "badmod.pak")
	_, statErr := os.Stat(deployedPath)
	require.True(t, os.IsNotExist(statErr), "precondition: nothing deployed to the game dir yet")

	// First reconcile: badmod does not participate (failed) - Install fails.
	_, err := svc.ReconcilePakManifestsForTest(context.Background(), game, "default", inst, failedByRef)
	require.Error(t, err)
	require.Contains(t, err.Error(), "deploying raw pak")
	require.Equal(t, 1, faulty.deployCalls)

	_, statErr = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(statErr), "the failed Install must not have deployed anything")

	// Second reconcile (the "next sync"): must retry and actually deploy -
	// the manifest already matched the target shape from ingest, so this
	// also proves the strengthened precheck doesn't trust shape alone.
	_, err = svc.ReconcilePakManifestsForTest(context.Background(), game, "default", inst, failedByRef)
	require.NoError(t, err)
	require.Equal(t, 2, faulty.deployCalls, "the retry must call Deploy again")

	_, statErr = os.Stat(deployedPath)
	require.NoError(t, statErr, "the retry must actually deploy the raw pak")
}

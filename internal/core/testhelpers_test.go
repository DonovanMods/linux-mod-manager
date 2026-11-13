package core_test

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bepinex"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/require"
)

// versionedFileSource serves a single file under mockSource's default ID
// ("1" - the same ID both ApplyProfileSwitch and ApplyImport's install
// loops select via stored FileIDs), but with an explicit Version ("1.0")
// distinct from the mod-level Version a test passes to AddMod - mirroring
// flows_install_test.go's oldFileSource/versionOverrideFileSource fixtures
// for the #94 stamp, applied to the two remaining flows (Task A4). See
// TestService_ApplyProfileSwitch_InstallLoop_RecordsFileVersion and
// TestApplyImport_InstallLoop_RecordsFileVersion.
type versionedFileSource struct {
	*mockSourceWithDownloads
}

func (s *versionedFileSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "1", Name: "Main File", FileName: mod.ID + ".zip", Version: "1.0", IsPrimary: true},
	}, nil
}

// newBareService returns a Service over three fresh temp directories, with
// nothing seeded and no source registered, closed when the test ends.
func newBareService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// newDefaultProfileService builds a service plus a game with an empty
// "default" profile set as the game's default - the shape the profile apply
// and sync flow tests start from. The game is deliberately NOT registered
// with the service (no SaveGame): a test that needs it registered says so.
func newDefaultProfileService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	return svc, game
}

// createImportTestZip writes a zip archive of files (member name -> content)
// at path. It is core_test's one zip writer: zipOf and createTestZip only
// choose a path and delegate here.
func createImportTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, f.Close())
	}()

	w := zip.NewWriter(f)
	defer func() {
		require.NoError(t, w.Close())
	}()

	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
}

// createTestScript creates an executable script in the temp directory.
// Shared by every hook-lifecycle test across this package (flows_test.go,
// flows_install_test.go, flows_update_test.go, flows_rollback_test.go,
// flows_variant_exclusivity_test.go) - moved here when the dead batch
// installer's test file (its original home) was deleted (#284, #285).
func createTestScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	scriptPath := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(scriptPath, []byte(content), 0755))
	return scriptPath
}

// newFlowsTestService returns a *core.Service backed by fresh temp dirs
// (config/data/cache), matching the construction pattern used throughout
// service_test.go.
//
// It registers the real BepInEx adapter, which is what internal/app does
// (registerAdapters) and therefore what every flow test in this package
// needs to be testing: since U3 (#413) the archive-layout rules, the config
// routing, the loader precondition and the loader verify tier all live
// behind the seam, and a Service without the adapter registered is a build
// of lmm that does not ship. It is the ONE concrete adapter registered
// here - icarus's own tests live in its package and core's compile tests use
// purpose-built stubs, while BepInEx's flow tests are end-to-end claims
// about an import or a deploy and have nowhere else to live.
//
// Registering it changes nothing for a game that is not a BepInEx game:
// Service.AdapterName resolves the identity unless the game declares the
// loader or has BepInEx in its install directory.
func newFlowsTestService(t *testing.T) *core.Service {
	t.Helper()
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	svc.RegisterAdapter(bepinex.New())
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})
	return svc
}

// seedInstalledMod stores the given files in the game's cache (when files is
// non-nil) and saves an InstalledMod DB record for source/mod/version with
// the requested Enabled state.
func seedInstalledMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version string, enabled bool, files map[string][]byte) {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(cache.Key{GameID: game.ID, SourceID: sourceID, ModID: modID, Version: version}, path, content))
	}

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       modID,
			SourceID: sourceID,
			Name:     "Test Mod",
			Version:  version,
			GameID:   game.ID,
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

// newDeployableService returns a Service with one enabled mod, cached and
// installed into game g1's "default" profile - ready for DeployProfile to
// actually deploy something. Extracted from
// TestService_DeployProfile_ProgressCallback_IndexTotalModNameSequence
// (v2 Phase 1 Task 6), which needs the identical shape for its own 3-mod
// setup and seeds two more mods on top of this one.
func newDeployableService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Mod One", "1.0", true, map[string][]byte{"one.esp": []byte("1")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	return svc, game
}

// seedNamedInstalledMod is seedInstalledMod with a caller-supplied Name,
// needed whenever a test must tell mods apart by name (seedInstalledMod
// hardcodes "Test Mod" for every mod, which is fine for single-mod tests but
// useless for asserting deploy order or per-mod skip/progress identity).
func seedNamedInstalledMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version string, enabled bool, files map[string][]byte) {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(cache.Key{GameID: game.ID, SourceID: sourceID, ModID: modID, Version: version}, path, content))
	}

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       modID,
			SourceID: sourceID,
			Name:     name,
			Version:  version,
			GameID:   game.ID,
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

// relsOf is a deployed-file listing's paths relative to their roots, for a
// test that asserts on mod_path paths by name (#629).
func relsOf(paths []domain.DeployPath, err error) ([]string, error) {
	if paths == nil {
		return nil, err
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = p.Rel
	}
	return out, err
}

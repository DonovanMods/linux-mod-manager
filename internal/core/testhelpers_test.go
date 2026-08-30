package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

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
func newFlowsTestService(t *testing.T) *core.Service {
	t.Helper()
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
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
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, path, content))
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
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, path, content))
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

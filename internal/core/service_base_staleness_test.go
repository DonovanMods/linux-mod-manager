package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// newStalenessTestService builds a DeployCompile game with a real (fixture)
// base pak installed at installDir, and a service whose cache/config live
// under fresh temp dirs. Returns the service, the game, and the base pak's
// path so tests can rewrite it to simulate a base-pak refresh.
func newStalenessTestService(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	return svc, game, basePak
}

// seedCompiledMod stages a fake compiled entry directly through the cache
// (bypassing Compile/Importer entirely - CheckBaseStaleness only reads
// markers, so this is a faster, more direct way to set up its inputs than
// driving a full compile), recording fingerprint as the file's base-index
// hash if non-empty (empty simulates a pre-#196 entry with NO marker at
// all).
func seedCompiledMod(t *testing.T, svc *core.Service, game *domain.Game, mod domain.InstalledMod, fingerprint string) {
	t.Helper()
	gameCache := svc.GetGameCache(game)
	versionDir := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, os.MkdirAll(versionDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(versionDir, "Fake_P.pak"), []byte("compiled"), 0o644))
	if fingerprint != "" {
		require.NoError(t, cache.MarkBaseIndexHash(versionDir, "fake-file-id", fingerprint))
	}
}

func TestCheckBaseStaleness_FingerprintMatch_NotStale(t *testing.T) {
	svc, game, basePak := newStalenessTestService(t)
	liveHash := basePakIndexHash(t, basePak)

	mod := domain.InstalledMod{Mod: domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", Version: "1.0"}}
	seedCompiledMod(t, svc, game, mod, liveHash)

	stale, err := svc.CheckBaseStaleness(game, []domain.InstalledMod{mod})
	require.NoError(t, err)
	require.Empty(t, stale, "a fingerprint matching the live base pak must not be reported stale")
}

func TestCheckBaseStaleness_FingerprintMismatch_Stale(t *testing.T) {
	svc, game, _ := newStalenessTestService(t)

	mod := domain.InstalledMod{Mod: domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", Version: "1.0"}}
	seedCompiledMod(t, svc, game, mod, "0000000000000000000000000000000000dead") // deliberately wrong

	stale, err := svc.CheckBaseStaleness(game, []domain.InstalledMod{mod})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.True(t, stale[0].RecompileNeeded)
	require.Equal(t, mod.Version, stale[0].NewVersion, "NewVersion must equal the current version - the mod hasn't changed, only the base pak has")
	require.Equal(t, mod.ID, stale[0].InstalledMod.ID)
}

// TestCheckBaseStaleness_MissingFingerprint_NotStale pins the #196-review
// amendment: a compiled entry with NO base-index marker (predates #196, or
// is actually a never-compiled prebuilt .pak - the two are locally
// indistinguishable) is skipped, not flagged. Flagging it would false-
// positive forever on plain prebuilt .pak mods, which a DeployCompile
// game's catalog can also legitimately serve (isExmodzFile only routes
// .exmodz through Compile).
func TestCheckBaseStaleness_MissingFingerprint_NotStale(t *testing.T) {
	svc, game, _ := newStalenessTestService(t)

	mod := domain.InstalledMod{Mod: domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", Version: "1.0"}}
	seedCompiledMod(t, svc, game, mod, "") // no marker at all

	stale, err := svc.CheckBaseStaleness(game, []domain.InstalledMod{mod})
	require.NoError(t, err)
	require.Empty(t, stale, "a missing fingerprint must be skipped, not flagged stale")
}

// TestCheckBaseStaleness_PinnedModIncluded pins design point 3: pinning
// fixes the mod VERSION, not the base pak, so a pinned mod's staleness must
// still be reported (unlike Updater.CheckUpdates, which filters pinned mods
// out entirely via UpdateCheckable).
func TestCheckBaseStaleness_PinnedModIncluded(t *testing.T) {
	svc, game, _ := newStalenessTestService(t)

	mod := domain.InstalledMod{Mod: domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", Version: "1.0"}, UpdatePolicy: domain.UpdatePinned}
	seedCompiledMod(t, svc, game, mod, "0000000000000000000000000000000000dead")

	stale, err := svc.CheckBaseStaleness(game, []domain.InstalledMod{mod})
	require.NoError(t, err)
	require.Len(t, stale, 1, "a pinned mod must still be checked for base staleness")
}

// TestCheckBaseStaleness_LocalModIncluded: a pure local import (SourceID ==
// domain.SourceLocal) has no remote to check, but it CAN go stale against
// the base pak - this check is entirely local/offline, so it must not skip
// local mods the way Updater.CheckUpdates does.
func TestCheckBaseStaleness_LocalModIncluded(t *testing.T) {
	svc, game, _ := newStalenessTestService(t)

	mod := domain.InstalledMod{Mod: domain.Mod{ID: "bear-mount", SourceID: domain.SourceLocal, Version: "1.0"}}
	seedCompiledMod(t, svc, game, mod, "0000000000000000000000000000000000dead")

	stale, err := svc.CheckBaseStaleness(game, []domain.InstalledMod{mod})
	require.NoError(t, err)
	require.Len(t, stale, 1)
}

// TestCheckBaseStaleness_NonCompileGame_NoOp: a DeployExtract/DeployCopy
// game has no base pak concept at all - CheckBaseStaleness must be an
// unconditional no-op rather than erroring on a missing base pak path.
func TestCheckBaseStaleness_NonCompileGame_NoOp(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "skyrim-se", ModPath: t.TempDir(), DeployMode: domain.DeployExtract}
	require.NoError(t, svc.AddGame(game))

	mod := domain.InstalledMod{Mod: domain.Mod{ID: "some-mod", SourceID: "nexusmods", Version: "1.0"}}
	stale, err := svc.CheckBaseStaleness(game, []domain.InstalledMod{mod})
	require.NoError(t, err)
	require.Empty(t, stale)
}

// TestCheckGameUpdates_MergesStalenessWithoutDuplicating proves the
// combined seam CLI/TUI both use: a mod with a REAL update available is not
// separately duplicated as a staleness row even when it's also stale, and a
// mod with ONLY staleness (no real update) is still surfaced.
func TestCheckGameUpdates_MergesStalenessWithoutDuplicating(t *testing.T) {
	svc, game, _ := newStalenessTestService(t)

	src := &updateMockSource{id: "fake-compiler", currentMod: &domain.Mod{ID: "has-real-update", Version: "2.0"}}
	svc.RegisterSource(src)

	realUpdateMod := domain.InstalledMod{Mod: domain.Mod{ID: "has-real-update", SourceID: "fake-compiler", Version: "1.0"}}
	staleOnlyMod := domain.InstalledMod{Mod: domain.Mod{ID: "stale-only", SourceID: "fake-compiler", Version: "1.0"}}
	seedCompiledMod(t, svc, game, realUpdateMod, "0000000000000000000000000000000000dead")
	seedCompiledMod(t, svc, game, staleOnlyMod, "0000000000000000000000000000000000dead")

	updates, err := svc.CheckGameUpdates(context.Background(), game, []domain.InstalledMod{realUpdateMod, staleOnlyMod})
	require.NoError(t, err)
	require.Len(t, updates, 2, "one real-update row + one staleness-only row, no duplicate for the mod with both")

	byID := map[string]domain.Update{}
	for _, u := range updates {
		byID[u.InstalledMod.ID] = u
	}

	real, ok := byID["has-real-update"]
	require.True(t, ok)
	require.Equal(t, "2.0", real.NewVersion)
	require.False(t, real.RecompileNeeded, "a real version update supersedes the staleness row - recompiling happens as part of applying it")

	stale, ok := byID["stale-only"]
	require.True(t, ok)
	require.True(t, stale.RecompileNeeded)
	require.Equal(t, "1.0", stale.NewVersion)
}

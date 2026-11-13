package core_test

// #628: the acquire engine's follow-ups from #602's review.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keptArchiveForInstalledMod installs "a", loses its cache entry, and has a
// re-install of it refused (a batch with a loader mod), so "a"'s archive is
// kept for the re-run while its row still names the lost entry. The source
// declares every archive's size and sha256, which a kept archive must still
// match to be reused (#602 review S2).
func keptArchiveForInstalledMod(t *testing.T) *acquireFixture {
	t.Helper()
	fx := newAcquireFixture(t, declared(plainMod("a", "A")), declared(loaderMod("thing", "Thing")))
	ctx := context.Background()
	a := &domain.Mod{ID: "a", SourceID: "repo", Name: "A", Version: "1.0.0", GameID: "valheim"}
	plan, err := fx.svc.PlanInstallMany(ctx, fx.game, "default", []*domain.Mod{a}, false)
	require.NoError(t, err)
	result, err := fx.svc.ApplyInstall(ctx, fx.game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Installed, 1)
	require.Equal(t, int64(1), fx.fetches("a"))

	require.NoError(t, os.RemoveAll(fx.svc.GetGameCache(fx.game).ModPath(cache.Key{GameID: fx.game.ID, SourceID: "repo", ModID: "a", Version: "1.0.0"})))

	thing := &domain.Mod{ID: "thing", SourceID: "repo", Name: "Thing", Version: "1.0.0", GameID: "valheim"}
	plan, err = fx.svc.PlanInstallMany(ctx, fx.game, "default", []*domain.Mod{a, thing}, false)
	require.NoError(t, err)
	result, err = fx.svc.ApplyInstall(ctx, fx.game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	require.True(t, result.Refused)
	require.Equal(t, int64(2), fx.fetches("a"))
	require.Equal(t, 2, fx.retainedEntries(t), "a's and thing's archives are kept for the re-run")
	return fx
}

// TestDeploySelfHeal_ReusesAnArchiveARefusedInstallKept is #628 item 2:
// deploy's self-heal re-fetches a mod whose cache entry is gone, and an
// archive a refused install kept for exactly that mod and version is the
// same bytes - no second download.
func TestDeploySelfHeal_ReusesAnArchiveARefusedInstallKept(t *testing.T) {
	fx := keptArchiveForInstalledMod(t)
	_, err := fx.svc.DeployProfile(context.Background(), fx.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, fx.svc.GetGameCache(fx.game).Exists(cache.Key{GameID: fx.game.ID, SourceID: "repo", ModID: "a", Version: "1.0.0"}), "the entry is healed")
	assert.Equal(t, int64(2), fx.fetches("a"), "the kept archive is reused, not fetched again")
	assert.Equal(t, 1, fx.retainedEntries(t), "and dropped once it is in the cache")
	fx.requireNoAcquireLeftovers(t)
}

// TestVerifyFix_ReusesAnArchiveARefusedInstallKept is the same for verify
// --fix's re-download of a missing file.
func TestVerifyFix_ReusesAnArchiveARefusedInstallKept(t *testing.T) {
	fx := keptArchiveForInstalledMod(t)
	_, err := fx.svc.VerifyReport(context.Background(), fx.game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	assert.True(t, fx.svc.GetGameCache(fx.game).Exists(cache.Key{GameID: fx.game.ID, SourceID: "repo", ModID: "a", Version: "1.0.0"}), "the entry is repaired")
	assert.Equal(t, int64(2), fx.fetches("a"), "the kept archive is reused, not fetched again")
	assert.Equal(t, 1, fx.retainedEntries(t), "and dropped once it is in the cache")
	fx.requireNoAcquireLeftovers(t)
}

// TestFomodRebuild_ReusesAnArchiveARefusedInstallKept is #628 item 2 through
// #614's rebuild paths: a FOMOD mod whose variant entry is gone, and whose
// re-install was refused (held back behind another mod's question), is
// rebuilt by self-heal and by verify --fix from the archive that refusal
// kept, with the recorded choices.
func TestFomodRebuild_ReusesAnArchiveARefusedInstallKept(t *testing.T) {
	for _, flow := range []string{"self-heal", "verify --fix"} {
		t.Run(flow, func(t *testing.T) {
			f := newFomodFixture(t)
			ctx := context.Background()
			f.serve(t, "mod1", "1.0", "4K", "v1")
			f.serve(t, "mod2", "1.0", "4K", "v2")
			_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
			require.NoError(t, err)
			require.NoError(t, f.svc.NewProfileManager().SetDefault(ctx, "g1", "default"))
			row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
			require.NoError(t, err)
			c := f.svc.GetGameCache(f.game)
			require.NoError(t, c.Delete(cacheKeyOf(row)))

			// mod1 replays its recorded choices; mod2 has none, so the
			// batch is refused and mod1's archive is kept.
			mods := []*domain.Mod{
				{ID: "mod1", SourceID: "src", Name: "Mod mod1", Version: "1.0", GameID: "g1"},
				{ID: "mod2", SourceID: "src", Name: "Mod mod2", Version: "1.0", GameID: "g1"},
			}
			plan, err := f.svc.PlanInstallMany(ctx, f.game, "default", mods, false)
			require.NoError(t, err)
			refused, err := f.svc.ApplyInstall(ctx, f.game, plan, core.InstallOptions{}, nil)
			require.NoError(t, err, "a refused batch keeps the batch's document")
			require.True(t, refused.Refused)
			require.NotNil(t, refused.FomodChoicesRequired)
			require.Equal(t, 3, f.mock.DownloadCount())

			if flow == "self-heal" {
				result, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
				require.NoError(t, err)
				assert.Empty(t, result.Skipped, "%+v", result.Skipped)
			} else {
				require.NoError(t, f.svc.SaveFileChecksum(ctx, "src", "mod1", "g1", "default", "mod1", "0123456789abcdef0123456789abcdef"))
				_, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
				require.NoError(t, err)
			}
			assert.True(t, c.Exists(cacheKeyOf(row)), "the row's own variant is rebuilt")
			content, err := os.ReadFile(filepath.Join(c.ModPath(cacheKeyOf(row)), "textures", "t.dds"))
			require.NoError(t, err)
			assert.Equal(t, "4K-v1", string(content), "with the recorded choices")
			assert.Equal(t, 3, f.mock.DownloadCount(), "from the kept archive, with no second download")
			requireNoAcquireLeftovers(t, f.svc, f.game)
		})
	}
}

// requirePanics runs fn and asserts it panicked with "boom": serve recovers
// a job's panic and keeps running, so what the panic leaves on disk stays.
func requirePanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() { require.Equal(t, "boom", recover()) }()
	fn()
}

// panicOn makes svc's acquire panic just after it extracts archive, where
// an extractor or an adapter's layout would.
func panicOn(svc *core.Service, archive string) {
	svc.SetAfterExtractForTest(func(name string) {
		if name == archive {
			panic("boom")
		}
	})
}

// TestAcquire_APanicLeavesNoAcquireDirectories is #628 item 1: a panic
// inside acquire - in the extractor, or an adapter's layout - used to skip
// the release the acquire's deferred cleanup made only on an error, leaving
// that file's acquire tree and download staging, and the same mod's earlier
// files, which were not in the run's item set yet.
func TestAcquire_APanicLeavesNoAcquireDirectories(t *testing.T) {
	t.Run("a batch item's download", func(t *testing.T) {
		fx := newAcquireFixture(t, plainMod("a", "A"), plainMod("b", "B"))
		panicOn(fx.svc, "b-1.0.0.zip")
		plan, err := fx.svc.PlanInstallMany(context.Background(), fx.game, "default", []*domain.Mod{
			{ID: "a", SourceID: "repo", Name: "A", Version: "1.0.0", GameID: "valheim"},
			{ID: "b", SourceID: "repo", Name: "B", Version: "1.0.0", GameID: "valheim"},
		}, false)
		require.NoError(t, err)
		requirePanics(t, func() { _, _ = fx.svc.ApplyInstall(context.Background(), fx.game, plan, core.InstallOptions{}, nil) })
		fx.requireNoAcquireLeftovers(t)
	})

	t.Run("an imported mod's earlier file", func(t *testing.T) {
		mux := http.NewServeMux()
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		for _, name := range []string{"f1", "f2"} {
			archive := bepinexZipBytes(t, map[string]string{name + ".txt": name})
			mux.HandleFunc("/"+name+".zip", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
		}
		manifest := fmt.Sprintf(`version: 1
mods:
  - id: m
    name: M
    version: 1.0.0
    files:
      - id: f1
        filename: f1.zip
        version: 1.0.0
        url: %[1]s/f1.zip
        primary: true
      - id: f2
        filename: f2.zip
        version: 1.0.0
        url: %[1]s/f2.zip
`, srv.URL)
		mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(manifest)) })
		src, err := custom.New(custom.SourceDefinition{ID: "repo", Name: "Repo", Type: custom.TypeManifest, AllowHTTP: true,
			Manifest: &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"}})
		require.NoError(t, err)
		svc := newFlowsTestService(t)
		svc.RegisterSource(src)
		root := t.TempDir()
		game := &domain.Game{ID: "valheim", Name: "Valheim", InstallPath: root, ModPath: root, LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{"repo": "valheim"}}
		require.NoError(t, svc.SaveGame(context.Background(), game))
		data, err := config.ExportProfile(&domain.Profile{Name: "target", GameID: game.ID,
			Mods: []domain.ModReference{{SourceID: "repo", ModID: "m", Version: "1.0.0", FileIDs: []string{"f1", "f2"}}}})
		require.NoError(t, err)
		plan, err := svc.PlanImport(context.Background(), game, data)
		require.NoError(t, err)

		panicOn(svc, "f2.zip")
		requirePanics(t, func() {
			_, _ = svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{Install: true}, nil)
		})
		requireNoAcquireLeftovers(t, svc, game)
	})

	t.Run("a batch install's earlier pinned file", func(t *testing.T) {
		svc, game, _ := setupInterplayService(t, true)
		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
		require.NoError(t, err)
		require.Len(t, plan.Dependencies, 1, "root takes the BATCH path")
		panicOn(svc, "root-patch-1.0.zip")
		requirePanics(t, func() {
			_, _ = svc.ApplyInstall(context.Background(), game, plan,
				core.InstallOptions{TargetVersion: "1.0", TargetFileIDs: []string{"root-main-1", "root-opt-1"}}, nil)
		})
		requireNoAcquireLeftovers(t, svc, game)
	})

	t.Run("a single download", func(t *testing.T) {
		fx := newAcquireFixture(t, plainMod("a", "A"))
		panicOn(fx.svc, "a-1.0.0.zip")
		plan, err := fx.svc.PlanInstall(context.Background(), fx.game, "default", "repo", "a", false)
		require.NoError(t, err)
		requirePanics(t, func() { _, _ = fx.svc.ApplyInstall(context.Background(), fx.game, plan, core.InstallOptions{}, nil) })
		fx.requireNoAcquireLeftovers(t)
	})

	t.Run("a fetched file", func(t *testing.T) {
		src := newFetchTestSource("ws")
		src.fetch = func(destDir string) (string, error) {
			return createTestZip(t, destDir, map[string]string{"item.txt": "x"}), nil
		}
		svc, game := fetchTestGame(t, src, "app")
		panicOn(svc, "test.zip")
		mod := &domain.Mod{ID: "m", SourceID: "ws", Name: "M", Version: "1", GameID: "g1"}
		requirePanics(t, func() {
			_, _ = svc.DownloadModForTest(context.Background(), "ws", game, mod,
				&domain.DownloadableFile{ID: "item", FileName: "item", IsPrimary: true}, nil)
		})
		requireNoAcquireLeftovers(t, svc, game)
	})
}

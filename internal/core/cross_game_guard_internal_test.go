package core

// cross_game_guard_internal_test.go is #445 gate 2's G2-1 where it fails
// closed: an Installer that cannot read what the games overlapping its mod
// directory record removes and replaces nothing. White-box because the
// read is a Service seam wired into an Installer by an unexported field,
// which is what the product does.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

func TestInstaller_AnUnreadableOtherGameChangesNothing(t *testing.T) {
	ctx := context.Background()
	for _, lm := range []domain.LinkMethod{domain.LinkCopy, domain.LinkSymlink} {
		t.Run(lm.String(), func(t *testing.T) {
			modPath := t.TempDir()
			game := &domain.Game{ID: "sky", ModPath: modPath}
			modCache := cache.New(t.TempDir())
			for _, version := range []string{"1.0", "2.0"} {
				require.NoError(t, modCache.Store("sky", "local", "k", version, "Data/a.esp", []byte("new "+version)))
				require.NoError(t, modCache.Store("sky", "local", "k", version, "Data/only-"+version+".esp", []byte("only "+version)))
			}
			live := filepath.Join(modPath, "Data", "a.esp")
			only := filepath.Join(modPath, "Data", "only-1.0.esp")
			require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
			require.NoError(t, os.WriteFile(live, []byte("the other game's, edited"), 0o644))
			require.NoError(t, os.WriteFile(only, []byte("only 1.0"), 0o644))

			installer := NewInstaller(modCache, linker.New(lm), nil)
			unreadable := errors.New("disk I/O error")
			installer.otherGames = func(context.Context, *domain.Game) (*otherGameRecords, error) {
				return nil, unreadable
			}
			old := &domain.Mod{SourceID: "local", ID: "k", Version: "1.0"}
			newer := &domain.Mod{SourceID: "local", ID: "k", Version: "2.0"}

			for name, call := range map[string]func() error{
				"uninstall": func() error { return installer.Uninstall(ctx, game, old, "default") },
				"install":   func() error { return installer.Install(ctx, game, newer, "default") },
				"replace":   func() error { return installer.Replace(ctx, game, old, newer, "default") },
			} {
				require.ErrorIs(t, call(), unreadable, name)
				assert.Equal(t, "the other game's, edited", readFile(t, live), name)
				assert.Equal(t, "only 1.0", readFile(t, only), name)
				assert.NoFileExists(t, filepath.Join(modPath, "Data", "only-2.0.esp"), name)
			}
		})
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// TestConverge_KeepsAFileAnotherGameRecords: verify --fix's row pass
// removes a recorded file its mod no longer ships - unless another game
// records it, when the file stays, is reported, and this stale record goes.
func TestConverge_KeepsAFileAnotherGameRecords(t *testing.T) {
	ctx := context.Background()
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	shared := t.TempDir()
	sky := &domain.Game{ID: "sky", Name: "Sky", ModPath: shared, LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}
	sky2 := &domain.Game{ID: "sky2", Name: "Sky 2", ModPath: shared, LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}
	require.NoError(t, svc.SaveGame(ctx, sky))
	require.NoError(t, svc.SaveGame(ctx, sky2))
	require.NoError(t, svc.GetGameCache(sky).Store("sky", "local", "k", "1.0", "k.esp", []byte("k")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "k", SourceID: "local", Name: "K", Version: "1.0", GameID: "sky"},
		ProfileName: "default", Enabled: true, Deployed: true, LinkMethod: domain.LinkCopy,
	}))
	require.NoError(t, os.WriteFile(filepath.Join(shared, "gone.esp"), []byte("sky2's"), 0o644))
	for _, game := range []string{"sky", "sky2"} {
		require.NoError(t, svc.db.SaveDeployedFile(ctx, game, "default", "gone.esp", "local", "k"))
	}

	for _, dryRun := range []bool{true, false} {
		result, err := svc.convergeDeployedFiles(ctx, sky, "default", dryRun)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gone.esp was left in place: game sky2 records it too")
		assert.Empty(t, result.Removed)
		assert.Equal(t, "sky2's", readFile(t, filepath.Join(shared, "gone.esp")))
	}
	rows, err := svc.db.GetDeployedFilesForMod(ctx, "sky", "default", "local", "k")
	require.NoError(t, err)
	assert.Empty(t, rows, "sky's stale record goes")
	rows, err = svc.db.GetDeployedFilesForMod(ctx, "sky2", "default", "local", "k")
	require.NoError(t, err)
	assert.Equal(t, []string{"gone.esp"}, rows)
}

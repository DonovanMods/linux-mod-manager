package core

// shared_original_internal_test.go is #632's replace half: an update whose
// new version no longer ships a path another mod of the profile now holds
// leaves that mod's file, and the original it stands over stays set aside.
// The update's obsolete-file loop used to remove the other mod's file and
// hand the original back while that mod still recorded the path.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

func TestUpdate_LeavesASharedPathAnotherModHolds(t *testing.T) {
	for _, lm := range []domain.LinkMethod{domain.LinkSymlink, domain.LinkHardlink, domain.LinkCopy} {
		t.Run(lm.String(), func(t *testing.T) {
			ctx := context.Background()
			modCache := cache.New(t.TempDir())
			database, err := db.New(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })
			gameDir := t.TempDir()
			game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: lm}

			first := &domain.Mod{ID: "first", SourceID: "src", Version: "1.0", GameID: "g"}
			firstV2 := &domain.Mod{ID: "first", SourceID: "src", Version: "2.0", GameID: "g"}
			second := &domain.Mod{ID: "second", SourceID: "src", Version: "1.0", GameID: "g"}
			store := func(m *domain.Mod, member, content string) {
				require.NoError(t, modCache.Store(cache.Key{GameID: "g", SourceID: m.SourceID, ModID: m.ID, Version: m.Version}, member, []byte(content)))
			}
			store(first, "Data/shared.esp", "the first mod's file")
			store(firstV2, "Data/other.esp", "the first mod's new file")
			store(second, "Data/shared.esp", "the second mod's file")
			for _, m := range []*domain.Mod{first, second} {
				require.NoError(t, database.SaveInstalledMod(ctx, &domain.InstalledMod{
					Mod: *m, ProfileName: "default", Enabled: true, LinkMethod: lm, UpdatePolicy: domain.UpdateNotify,
				}))
			}

			shared := filepath.Join(gameDir, "Data", "shared.esp")
			require.NoError(t, os.MkdirAll(filepath.Dir(shared), 0o755))
			require.NoError(t, os.WriteFile(shared, []byte("as the game shipped"), 0o644))

			originals := (&Service{dataDir: t.TempDir()}).originalsStoreFor("g")
			inst := NewInstaller(modCache, linker.New(lm), database)
			inst.setOriginals(originals)
			require.NoError(t, inst.Install(ctx, game, first, "default"))
			require.NoError(t, inst.Install(ctx, game, second, "default"))
			rows, err := originals.list()
			require.NoError(t, err)
			require.Len(t, rows, 1, "the first deploy set the original aside")

			require.NoError(t, inst.Replace(ctx, game, first, firstV2, "default"))

			data, err := os.ReadFile(shared)
			require.NoError(t, err)
			assert.Equal(t, "the second mod's file", string(data), "the second mod's file stays")
			rows, err = originals.list()
			require.NoError(t, err)
			assert.Len(t, rows, 1, "the original stays set aside under it")
			owner, err := database.GetFileOwner(ctx, "g", "default", domain.DeployPath{Rel: "Data/shared.esp"})
			require.NoError(t, err)
			require.NotNil(t, owner)
			assert.Equal(t, "second", owner.ModID)
		})
	}
}

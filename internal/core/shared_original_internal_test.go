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

// #633 review: hand-off notes are grouped by the mods' keys, not their
// names - two mods can share a display name.
func TestHandOffNotes_GroupByKeyNotName(t *testing.T) {
	gone := handOffMod{key: "src:gone", label: "Gone"}
	notes := handOffNotes([]handedPath{
		{path: domain.DeployPath{Rel: "a.txt"}, from: gone, heir: handOffMod{key: "src:one", label: "Twin"}},
		{path: domain.DeployPath{Rel: "b.txt"}, from: gone, heir: handOffMod{key: "src:two", label: "Twin"}},
		{path: domain.DeployPath{Rel: "c.txt"}, from: gone, heir: handOffMod{key: "src:one", label: "Twin"}},
	})
	require.Len(t, notes, 2, "one note per heir key: %q", notes)
	assert.Equal(t, "Twin's files are deployed now in place of Gone's at a.txt, c.txt, as the next mod in load order that ships them", notes[0])
	assert.Equal(t, "b.txt: Twin's file is deployed there now in place of Gone's, as the next mod in load order that ships it", notes[1])
}

// #633 review: a path is reported only while its record still names the
// mod it was handed to - one an update undone after a later failure took
// back (its record names the updated mod again) is not reported.
func TestHandOffWarnings_ReportOnlyTheFinalOwner(t *testing.T) {
	ctx := context.Background()
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	game := &domain.Game{ID: "g", ModPath: t.TempDir()}
	for _, rec := range []struct{ path, mod string }{{"held.txt", "heir"}, {"undone.txt", "updated"}} {
		require.NoError(t, svc.db.RecordDeployedFile(ctx, db.DeployedFileRecord{
			GameID: game.ID, Profile: "default", Path: domain.DeployPath{Rel: rec.path},
			SourceID: "src", ModID: rec.mod, ModPath: recordRoot(game),
		}))
	}
	from := handOffMod{key: "src:updated", label: "Updated"}
	heir := handOffMod{key: "src:heir", label: "Heir"}
	notes := svc.handOffWarnings(game.ID, []handedPath{
		{path: domain.DeployPath{Rel: "held.txt"}, profile: "default", from: from, heir: heir},
		{path: domain.DeployPath{Rel: "undone.txt"}, profile: "default", from: from, heir: heir},
		{path: domain.DeployPath{Rel: "released.txt"}, profile: "default", from: from, heir: heir},
	})
	assert.Equal(t, []string{"held.txt: Heir's file is deployed there now in place of Updated's, as the next mod in load order that ships it"}, notes)
}

// #633 review: a profile document that cannot be read hands nothing on -
// load order cannot be told, so the original goes back as before - rather
// than handing the path to the wrong mod.
func TestPathHeirs_AnUnreadableProfileHandsNothingOn(t *testing.T) {
	ctx := context.Background()
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	game := &domain.Game{ID: "g", ModPath: t.TempDir()}
	require.NoError(t, svc.db.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{ID: "other", SourceID: "src", Version: "1.0", GameID: "g"}, ProfileName: "default", Enabled: true, Deployed: true,
	}))
	dir := filepath.Join(svc.configDir, "games", "g", "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "default.yaml"), []byte("mods: [unterminated\n"), 0o644))

	_, err = svc.pathHeirsFor("", nil, nil).resolve(ctx, game, "default", &domain.Mod{ID: "going", SourceID: "src"})
	require.Error(t, err)
}

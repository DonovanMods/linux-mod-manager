package core

// originals_update_internal_test.go is #350 re-review finding N1: ruling (a)
// - "when lmm removes a file it had replaced, the captured original goes
// back" - applied to the one removal path it never reached,
// replaceWithCaches' obsolete-file loop. That loop is how BOTH `lmm update`
// and `lmm update rollback` remove a file the new (or the previous) version
// no longer ships, so an update over stock content used to leave exactly the
// hole ruling (a) exists to end - on all three link methods, the default
// symlink included, because the path holds lmm's own file by then and the
// foreign-file guard does not apply.
//
// White-box: the store is resolved from a Service's data directory and wired
// into an Installer by unexported setters, which is what the product does.

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

// obsoleteLoopFixture is the shape both tests below need: a game directory
// whose own Data/stock.esp (0755) has been deployed over by a version that
// ships that file, with the original captured - one manifest row.
type obsoleteLoopFixture struct {
	inst      *Installer
	store     *originalsStore
	game      *domain.Game
	stockPath string
}

// newObsoleteLoopFixture installs shipping over the game's own
// Data/stock.esp. shipping's cache entry names Data/stock.esp; other's does
// not, so a replace in either direction routes that path through the
// obsolete-file loop.
func newObsoleteLoopFixture(t *testing.T, lm domain.LinkMethod, shipping, other *domain.Mod) obsoleteLoopFixture {
	t.Helper()

	cacheDir, gameDir, dataDir := t.TempDir(), t.TempDir(), t.TempDir()
	modCache := cache.New(cacheDir)
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: lm}

	require.NoError(t, modCache.Store("g", shipping.SourceID, shipping.ID, shipping.Version, "Data/stock.esp", []byte("the mod's version")))
	require.NoError(t, modCache.Store("g", other.SourceID, other.ID, other.Version, "Data/other.esp", []byte("the other version")))

	stock := filepath.Join(gameDir, "Data", "stock.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("as the game shipped"), 0755))

	svc := &Service{dataDir: dataDir}
	store := svc.originalsStoreFor("g")
	require.NotNil(t, store)

	inst := NewInstaller(modCache, linker.New(lm), database)
	inst.setOriginals(store)

	require.NoError(t, inst.Install(context.Background(), game, shipping, "default"))
	rows, err := store.list()
	require.NoError(t, err)
	require.Len(t, rows, 1, "the deploy captured the game's own file")

	return obsoleteLoopFixture{inst: inst, store: store, game: game, stockPath: stock}
}

// assertStockIsBack is ruling (a)'s claim: the bytes, the mode, and only
// then the row.
func (f obsoleteLoopFixture) assertStockIsBack(t *testing.T) {
	t.Helper()

	data, err := os.ReadFile(f.stockPath)
	require.NoError(t, err, "the stock file must be BACK, not merely absent")
	assert.Equal(t, "as the game shipped", string(data))

	info, err := os.Stat(f.stockPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "and with the mode it had")

	rows, err := f.store.list()
	require.NoError(t, err)
	assert.Empty(t, rows, "the row goes once the original is back in place")
}

// TestUpdate_PutsBackTheFileTheObsoleteLoopRemoves is the `lmm update`
// direction: v1 ships Data/stock.esp over the game's own copy, v2 no longer
// ships it, so the update removes lmm's file - and the original goes back.
func TestUpdate_PutsBackTheFileTheObsoleteLoopRemoves(t *testing.T) {
	for _, lm := range []domain.LinkMethod{domain.LinkCopy, domain.LinkSymlink, domain.LinkHardlink} {
		t.Run(lm.String(), func(t *testing.T) {
			oldMod := &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g"}
			newMod := &domain.Mod{ID: "1", SourceID: "src", Version: "2.0", GameID: "g"}

			f := newObsoleteLoopFixture(t, lm, oldMod, newMod)
			require.NoError(t, f.inst.Replace(context.Background(), f.game, oldMod, newMod, "default"))

			f.assertStockIsBack(t)
		})
	}
}

// TestUpdateRollback_PutsBackTheFileTheObsoleteLoopRemoves is the same hole
// through `lmm update rollback`, which reaches the same loop by the entry
// point ApplyRollback uses (ReplaceForUpdate with the transition reversed,
// current FileIDs -> PreviousFileIDs). Here the CURRENT version is the one
// that added a file over stock content and the previous one does not have
// it.
func TestUpdateRollback_PutsBackTheFileTheObsoleteLoopRemoves(t *testing.T) {
	for _, lm := range []domain.LinkMethod{domain.LinkCopy, domain.LinkSymlink, domain.LinkHardlink} {
		t.Run(lm.String(), func(t *testing.T) {
			current := &domain.Mod{ID: "1", SourceID: "src", Version: "2.0", GameID: "g"}
			previous := &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g"}

			f := newObsoleteLoopFixture(t, lm, current, previous)
			require.NoError(t, f.inst.ReplaceForUpdate(context.Background(), f.game,
				current, previous, "default", []string{"f2"}, []string{"f1"}))

			f.assertStockIsBack(t)
		})
	}
}

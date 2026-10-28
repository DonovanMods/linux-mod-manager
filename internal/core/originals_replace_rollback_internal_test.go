package core

// originals_replace_rollback_internal_test.go is #369: ruling (a) - "when
// lmm removes a file it had replaced, the captured original goes back" - in
// the FAILURE direction, on the one path nobody scoped.
//
// replaceWithCaches captures the original under every NEW-only file it
// deploys over stock content (#350). When the replace then fails part way -
// a deploy that will not link, a cancelled context, a DB write that will
// not land - restoreOldFiles undeploys each of those new-only files again,
// and until now it left the hole the originals store exists to end: lmm's
// file gone, the game's own file still in the store, and the path simply
// empty.
//
// White-box for the same reason as its sibling: the store is resolved from
// a Service's data directory and wired into an Installer by unexported
// setters, which is what the product does.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// failingDeployLinker is a real linker that refuses to deploy ONE relative
// path. It is how the test reaches a mid-Replace failure deterministically:
// the new version's files deploy in lexical order, so a refusal on the last
// of them leaves the ones before it - including the one that went over the
// game's own file - already on disk when the rollback runs.
type failingDeployLinker struct {
	linker.Linker
	refuse string
}

func (l failingDeployLinker) Deploy(src, dst string) error {
	if strings.HasSuffix(filepath.ToSlash(dst), l.refuse) {
		return errors.New("refusing to deploy, on purpose")
	}
	return l.Linker.Deploy(src, dst)
}

// TestReplaceRollback_PutsBackTheOriginalUnderANewOnlyFile is the #369
// claim. v2.0 adds Data/stock.esp - a file v1.0 never shipped - over the
// game's own copy, and then fails on the file after it. The rollback
// removes lmm's Data/stock.esp again, so the game's own file must be back
// where it was and its row must be gone from the store.
func TestReplaceRollback_PutsBackTheOriginalUnderANewOnlyFile(t *testing.T) {
	for _, lm := range []domain.LinkMethod{domain.LinkCopy, domain.LinkSymlink, domain.LinkHardlink} {
		t.Run(lm.String(), func(t *testing.T) {
			cacheDir, gameDir, dataDir := t.TempDir(), t.TempDir(), t.TempDir()
			modCache := cache.New(cacheDir)
			database, err := db.New(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })

			game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: lm}
			oldMod := &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g"}
			newMod := &domain.Mod{ID: "1", SourceID: "src", Version: "2.0", GameID: "g"}

			// v1.0 ships one file; v2.0 ships that one, the game's own
			// Data/stock.esp, and one more that sorts AFTER it.
			require.NoError(t, modCache.Store("g", "src", "1", "1.0", "Data/keep.esp", []byte("v1 keep")))
			require.NoError(t, modCache.Store("g", "src", "1", "2.0", "Data/keep.esp", []byte("v2 keep")))
			require.NoError(t, modCache.Store("g", "src", "1", "2.0", "Data/stock.esp", []byte("v2 stock")))
			require.NoError(t, modCache.Store("g", "src", "1", "2.0", "Data/zzz.esp", []byte("v2 zzz")))

			stock := filepath.Join(gameDir, "Data", "stock.esp")
			require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
			require.NoError(t, os.WriteFile(stock, []byte("as the game shipped"), 0755))

			svc := &Service{dataDir: dataDir}
			store := svc.originalsStoreFor("g")
			require.NotNil(t, store)

			inst := NewInstaller(modCache, failingDeployLinker{Linker: linker.New(lm), refuse: "Data/zzz.esp"}, database)
			inst.setOriginals(store)

			require.NoError(t, inst.Install(context.Background(), game, oldMod, "default"))
			rows, err := store.list()
			require.NoError(t, err)
			require.Empty(t, rows, "v1.0 never touched the game's own file")

			require.Error(t, inst.Replace(context.Background(), game, oldMod, newMod, "default"),
				"the replace must fail after Data/stock.esp is already deployed")

			data, err := os.ReadFile(stock)
			require.NoError(t, err, "the game's own file must be BACK, not merely absent")
			assert.Equal(t, "as the game shipped", string(data))

			info, err := os.Stat(stock)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "and with the mode it had")

			rows, err = store.list()
			require.NoError(t, err)
			assert.Empty(t, rows, "the row goes once the original is back in place")
		})
	}
}

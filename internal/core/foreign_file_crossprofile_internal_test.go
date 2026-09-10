package core

// foreign_file_crossprofile_internal_test.go is #404's F1: Installer's
// obsolete-file and uninstall loops refuse to remove a file lmm does not
// own, and "own" has to be a question about the GAME, because the deployed
// tree is one directory every profile of that game shares.
//
// The two halves are asserted separately and on all three link methods,
// because only copy and hardlink can tell them apart at all: a symlink is
// not a regular file, so Installer.foreignFile answers false for it before
// it ever asks the database, which is exactly why the profile-scoped
// question survived as long as it did.
//
//   - a file ANOTHER profile of this game deployed is lmm's own file, and a
//     cross-profile Replace must be free to remove it;
//   - a file NO profile ever deployed - the game's own content - is still
//     foreign, so it is captured and left alone, never destroyed.
//
// White-box: foreignFile is unexported, and the originals store is wired
// into an Installer by unexported setters, exactly as the product does it.

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

// allLinkMethods is the point of this file: the invariant is about the
// game-global tree, not about symlinks.
var allLinkMethods = []domain.LinkMethod{domain.LinkSymlink, domain.LinkCopy, domain.LinkHardlink}

// crossProfileFixture is a game whose modCache holds v1 (shipping
// Data/shared.esp) and v2 (shipping Data/only-new.esp), with an Installer
// wired to a real database and to the originals store the product wires.
type crossProfileFixture struct {
	inst      *Installer
	store     *originalsStore
	game      *domain.Game
	v1, v2    *domain.Mod
	sharedDst string
}

func newCrossProfileFixture(t *testing.T, lm domain.LinkMethod) crossProfileFixture {
	t.Helper()

	cacheDir, gameDir, dataDir := t.TempDir(), t.TempDir(), t.TempDir()
	modCache := cache.New(cacheDir)
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	v1 := &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.0", GameID: "g"}
	v2 := &domain.Mod{ID: "mod1", SourceID: "src", Version: "2.0", GameID: "g"}
	require.NoError(t, modCache.Store("g", v1.SourceID, v1.ID, v1.Version, "Data/shared.esp", []byte("v1 bytes")))
	require.NoError(t, modCache.Store("g", v2.SourceID, v2.ID, v2.Version, "Data/only-new.esp", []byte("v2 bytes")))

	svc := &Service{dataDir: dataDir}
	store := svc.originalsStoreFor("g")
	require.NotNil(t, store)

	inst := NewInstaller(modCache, linker.New(lm), database)
	inst.setOriginals(store)

	return crossProfileFixture{
		inst: inst, store: store,
		game:      &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: lm, LinkMethodExplicit: true},
		v1:        v1,
		v2:        v2,
		sharedDst: filepath.Join(gameDir, "Data", "shared.esp"),
	}
}

// TestForeignFile_AnotherProfilesDeploymentIsNotForeign is the ownership
// question itself, in isolation: profile "a" deploys v1, and profile "b"
// asks about the file it left. "b" has no deployed_files row of its own for
// that path - that is the whole point - so the profile-scoped lookup finds
// nothing, and only the game-wide one can answer correctly.
func TestForeignFile_AnotherProfilesDeploymentIsNotForeign(t *testing.T) {
	for _, lm := range allLinkMethods {
		t.Run(lm.String(), func(t *testing.T) {
			f := newCrossProfileFixture(t, lm)
			require.NoError(t, f.inst.Install(context.Background(), f.game, f.v1, "a"))

			assert.False(t,
				f.inst.foreignFile(context.Background(), f.game, "b", "Data/shared.esp", f.sharedDst),
				"a file another profile of this game deployed is lmm's own; the deployed tree is game-global")
			assert.False(t,
				f.inst.foreignFile(context.Background(), f.game, "a", "Data/shared.esp", f.sharedDst),
				"and the deploying profile's own answer is unchanged")
		})
	}
}

// TestForeignFile_TheGamesOwnFileStaysForeign is the other half, and the one
// the widening must not break: a regular file at a path no profile has a
// deployed_files row for is stock content, and stays foreign under every
// link method.
func TestForeignFile_TheGamesOwnFileStaysForeign(t *testing.T) {
	for _, lm := range allLinkMethods {
		t.Run(lm.String(), func(t *testing.T) {
			f := newCrossProfileFixture(t, lm)
			require.NoError(t, os.MkdirAll(filepath.Dir(f.sharedDst), 0755))
			require.NoError(t, os.WriteFile(f.sharedDst, []byte("as the game shipped"), 0755))

			assert.True(t,
				f.inst.foreignFile(context.Background(), f.game, "a", "Data/shared.esp", f.sharedDst),
				"nothing lmm deployed is here, under any profile: this is the game's own file")
		})
	}
}

// TestUninstall_LeavesTheGamesOwnFileAlone is that same judgement reached
// through the loop it protects (#350): uninstalling a mod whose cache entry
// NAMES Data/shared.esp, on a game that has its own file there and no
// deployed_files row for it, must leave the bytes exactly where they are.
//
// The row is deleted by hand rather than never written, so the file on disk
// is genuinely unattributed while the mod is still installed - the stale
// state #350's guard exists for, and the one the #404 widening could have
// swallowed if it had answered "owned" for any row of the game rather than
// for this path.
func TestUninstall_LeavesTheGamesOwnFileAlone(t *testing.T) {
	for _, lm := range allLinkMethods {
		t.Run(lm.String(), func(t *testing.T) {
			f := newCrossProfileFixture(t, lm)
			require.NoError(t, os.MkdirAll(filepath.Dir(f.sharedDst), 0755))
			require.NoError(t, os.WriteFile(f.sharedDst, []byte("as the game shipped"), 0755))

			// A row for a DIFFERENT path of the same game, so the game-wide
			// question has something to find and still has to answer "no"
			// for this one.
			require.NoError(t, f.inst.db.SaveDeployedFile(context.Background(), "g", "a", "Data/elsewhere.esp", "src", "mod1"))

			require.NoError(t, f.inst.Uninstall(context.Background(), f.game, f.v1, "a"))

			data, err := os.ReadFile(f.sharedDst)
			require.NoError(t, err, "the game's own file must still be there")
			assert.Equal(t, "as the game shipped", string(data))

			rows, err := f.store.list()
			require.NoError(t, err)
			assert.Empty(t, rows, "nothing was replaced, so nothing was captured")
		})
	}
}

// TestReplaceCrossProfile_RemovesTheObsoleteFileAndPutsTheOriginalBack is
// #404's invariant and #350's ruling (a) in one run, which is the pairing
// that matters: profile "a" deploys v1 OVER the game's own
// Data/shared.esp - captured - and then profile "b" Replaces v1 with v2,
// which does not ship that file.
//
// The obsolete-file loop must remove lmm's file even though the row that
// records it belongs to "a" (before #404 it did so only under symlink), and
// having removed it, must put the game's own bytes back rather than leaving
// a hole where stock content used to be.
func TestReplaceCrossProfile_RemovesTheObsoleteFileAndPutsTheOriginalBack(t *testing.T) {
	for _, lm := range allLinkMethods {
		t.Run(lm.String(), func(t *testing.T) {
			f := newCrossProfileFixture(t, lm)
			require.NoError(t, os.MkdirAll(filepath.Dir(f.sharedDst), 0755))
			require.NoError(t, os.WriteFile(f.sharedDst, []byte("as the game shipped"), 0755))

			require.NoError(t, f.inst.Install(context.Background(), f.game, f.v1, "a"))
			rows, err := f.store.list()
			require.NoError(t, err)
			require.Len(t, rows, 1, "precondition: the deploy captured the game's own file")

			require.NoError(t, f.inst.Replace(context.Background(), f.game, f.v1, f.v2, "b"))

			_, err = os.Lstat(filepath.Join(f.game.ModPath, "Data", "only-new.esp"))
			assert.NoError(t, err, "v2's own file must be deployed")

			data, err := os.ReadFile(f.sharedDst)
			require.NoError(t, err, "the obsolete file went, and the game's own file came back")
			assert.Equal(t, "as the game shipped", string(data),
				"lmm's v1 bytes must be gone - another profile deployed them, which makes them lmm's own to remove")

			info, err := os.Stat(f.sharedDst)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "and with the mode it had")

			rows, err = f.store.list()
			require.NoError(t, err)
			assert.Empty(t, rows, "the row goes once the original is back in place")
		})
	}
}

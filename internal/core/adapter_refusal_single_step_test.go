package core_test

// The single-step deploy paths honour an adapter refusal like every Plan
// does (#413). `lmm mod enable`, verify --fix's re-link and re-deploy
// repairs, and the merged-artifact resync every mutation ends with have no
// plan snapshot to carry the check, so each one asks the adapter itself
// before it deploys - and a refused game keeps its tree exactly as it was.
// The removals (disable, purge, uninstall, the uninstall-to-zero of a
// merged artifact) are the deliberate exception and still run.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// byteTreeSnapshot describes every entry under root - its type, a symlink's
// target and a regular file's content hash - so two snapshots are equal
// exactly when the tree is byte-identical.
func byteTreeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entry := rel + " " + info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entry += " -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			entry += " " + hex.EncodeToString(sum[:])
		}
		out = append(out, entry)
		return nil
	}))
	sort.Strings(out)
	return out
}

// refusalOf is the error every Plan gives game - the message and remedy a
// single-step flow must repeat.
func refusalOf(t *testing.T, svc *core.Service, game *domain.Game) error {
	t.Helper()
	_, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{})
	require.Error(t, err, "fixture: every Plan refuses this game")
	return err
}

// onlyMod returns the one installed mod in game's default profile.
func onlyMod(t *testing.T, svc *core.Service, game *domain.Game) domain.InstalledMod {
	t.Helper()
	mods, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	return mods[0]
}

// TestEnableMod_RefusesWhereTheAdapterIsRefused: enabling a mod deploys
// it, so on a refused game it refuses - with the very error a deploy
// gives - and touches nothing. Under the identity routing a refused game
// falls back to, the deploy would link the seeded BepInEx/config files over
// the user's own.
func TestEnableMod_RefusesWhereTheAdapterIsRefused(t *testing.T) {
	ctx := context.Background()
	for name, edit := range refusals {
		t.Run(name, func(t *testing.T) {
			svc, game, dotfile := newGameRootWithUserConfigs(t)
			mod := onlyMod(t, svc, game)
			_, err := svc.DisableMod(ctx, game, "default", mod.SourceID, mod.ID)
			require.NoError(t, err)
			game = refuse(t, svc, game, edit)
			want := refusalOf(t, svc, game)
			before := byteTreeSnapshot(t, game.InstallPath)

			_, err = svc.EnableMod(ctx, game, "default", mod.SourceID, mod.ID)
			require.Error(t, err)
			assert.Equal(t, want.Error(), err.Error(), "the refusal and remedy every other flow gives")

			assert.Equal(t, before, byteTreeSnapshot(t, game.InstallPath), "the tree is byte-identical")
			requireUserConfigsKept(t, game, dotfile)
			row, err := svc.GetInstalledMod(ctx, mod.SourceID, mod.ID, game.ID, "default")
			require.NoError(t, err)
			assert.False(t, row.Enabled, "the row stays disabled")
		})
	}

	t.Run("a precondition refusal", func(t *testing.T) {
		svc, game, _ := newPlanFixtureWithAdapter(t, nil)
		refused := switchTo(t, svc, game)
		_, err := svc.DisableMod(ctx, game, "default", "acme", "m1")
		require.NoError(t, err)
		refused.Store(true)

		_, err = svc.EnableMod(ctx, game, "default", "acme", "m1")
		require.Error(t, err)
		assert.ErrorIs(t, err, adapter.ErrPreconditionUnmet)
		assert.NoFileExists(t, filepath.Join(game.ModPath, "a.esp"))
	})
}

// switchTo registers a switchableRefuser as game's adapter, consenting
// until the returned flag is set.
func switchTo(t *testing.T, svc *core.Service, game *domain.Game) *atomic.Bool {
	t.Helper()
	refuse := &atomic.Bool{}
	svc.RegisterAdapter(switchableRefuser{refuse: refuse})
	game.Adapter = "switchable"
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return refuse
}

package core_test

// #445 review F5: #446 made SetDefault mark the target before unmarking the
// others, and a switch calls SetDefault last - after the target's files are
// deployed. A target profile file lmm could not write therefore left the
// game with the new profile's files live and the OLD profile still marked
// active, and every guard built on "which profile is active" then worked
// against the wrong one. A switch now checks, before it touches anything,
// that it can write every profile file SetDefault will.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipAsRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file anyway")
	}
}

// requireStillOnA checks the switch to b changed nothing: the tree, and a
// still the one profile marked active.
func requireStillOnA(t *testing.T, f *backfillFixture, before map[string]string) {
	t.Helper()
	ctx := context.Background()
	assert.Equal(t, before, treeOf(t, f.gameDir), "nothing was deployed or removed")
	assert.NoFileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
	pm := f.svc.NewProfileManager()
	a, err := pm.Get(ctx, f.game.ID, "a")
	require.NoError(t, err)
	assert.True(t, a.IsDefault)
	b, err := pm.Get(ctx, f.game.ID, "b")
	require.NoError(t, err)
	assert.False(t, b.IsDefault)
}

func TestSwitch_AProfileFileItCannotWriteRefusesTheSwitchFirst(t *testing.T) {
	skipAsRoot(t)
	ctx := context.Background()

	for _, profile := range []string{"b", "a"} {
		t.Run("plan, "+profile+" read-only", func(t *testing.T) {
			f := liveDirFixture(t)
			require.NoError(t, os.Chmod(f.profilePath(profile), 0o444))
			before := treeOf(t, f.gameDir)

			_, err := f.svc.PlanProfileSwitch(ctx, f.game, "b")
			require.Error(t, err)
			assert.Contains(t, err.Error(), f.profilePath(profile))
			assert.Contains(t, err.Error(), "nothing was changed")
			requireStillOnA(t, f, before)
		})
	}

	t.Run("apply, made read-only after the plan", func(t *testing.T) {
		f := liveDirFixture(t)
		plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "b")
		require.NoError(t, err)
		require.NoError(t, os.Chmod(f.profilePath("b"), 0o444))
		before := treeOf(t, f.gameDir)

		_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), f.profilePath("b"))
		requireStillOnA(t, f, before)
	})

	t.Run("a flag-only switch", func(t *testing.T) {
		f := liveDirFixture(t)
		f.setFlag(t, "a", false)
		require.NoError(t, os.Chmod(f.profilePath("b"), 0o444))
		_, err := f.svc.PlanProfileSwitch(ctx, f.game, "b")
		require.Error(t, err)
		assert.Contains(t, err.Error(), f.profilePath("b"))
	})

	t.Run("writable files switch", func(t *testing.T) {
		f := liveDirFixture(t)
		f.switchTo(t, "b")
		assert.FileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
		b, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "b")
		require.NoError(t, err)
		assert.True(t, b.IsDefault)
	})

	t.Run("an already-active target needs no write", func(t *testing.T) {
		f := liveDirFixture(t)
		require.NoError(t, os.Chmod(f.profilePath("a"), 0o444))
		plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "a")
		require.NoError(t, err)
		assert.True(t, plan.AlreadyActive)
	})
}

// TestSwitch_ProfilesInAReadOnlyDirectoryStillSwitch is F5 read with F10: a
// writable profile file in a directory lmm cannot create files in is saved
// in place, so it does not refuse a switch - but its save is not atomic,
// and the result says so.
func TestSwitch_ProfilesInAReadOnlyDirectoryStillSwitch(t *testing.T) {
	skipAsRoot(t)
	ctx := context.Background()
	f := liveDirFixture(t)
	dir := filepath.Dir(f.profilePath("a"))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "b")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(f.gameDir, "bonly.esp"))
	b, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "b")
	require.NoError(t, err)
	assert.True(t, b.IsDefault)
	a, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "a")
	require.NoError(t, err)
	assert.False(t, a.IsDefault)
	assert.Contains(t, f.warnings.String(), "not atomically", "the non-atomic save is reported")
}

// TestSwitch_AProfileNeedingABackupInAReadOnlyDirectoryIsRefused: a target
// whose layout forces a whole rewrite needs its backup written beside it,
// which a read-only directory refuses - so the switch refuses first.
func TestSwitch_AProfileNeedingABackupInAReadOnlyDirectoryIsRefused(t *testing.T) {
	skipAsRoot(t)
	ctx := context.Background()
	f := liveDirFixture(t)
	// A flow-style document: SaveProfile cannot add is_default to it in
	// place, so marking it active rewrites it whole and keeps a backup.
	path := f.profilePath("b")
	require.NoError(t, os.WriteFile(path, []byte("{name: b, game_id: g1, mods: [{source_id: src, mod_id: bonly, version: \"1.0\"}, {source_id: src, mod_id: shared, version: \"1.0\"}]}\n"), 0o644))
	dir := filepath.Dir(path)
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	before := treeOf(t, f.gameDir)

	_, err := f.svc.PlanProfileSwitch(ctx, f.game, "b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), path)
	assert.Equal(t, before, treeOf(t, f.gameDir))
	require.NoError(t, os.Chmod(dir, 0o755))
	requireStillOnA(t, f, before)
}

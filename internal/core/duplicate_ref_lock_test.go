package core_test

// #457: a profile that lists one mod twice (lmm never writes that, a hand
// edit can) is decided by the FIRST copy in every flow - the update gate
// that enforces a lock included. The surfaces that REPORT a lock read other
// copies: the deploy preview the last, `lmm list` any. So a user could be
// told a mod is locked, or not, in a way `lmm update` then contradicted.

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeDuplicatedLock lists mod m1 twice in profile a: the first copy
// locked as given at version 1.0, the second the opposite at 2.0.
func writeDuplicatedLock(t *testing.T, f *backfillFixture, firstLocked bool) {
	t.Helper()
	doc := fmt.Sprintf(`name: a
game_id: %s
is_default: true
mods:
  - source_id: src
    mod_id: m1
    version: "1.0"
    locked: %v
  - source_id: src
    mod_id: m1
    version: "2.0"
    locked: %v
`, f.game.ID, firstLocked, !firstLocked)
	require.NoError(t, os.WriteFile(f.profilePath("a"), []byte(doc), 0o644))
}

func TestLockReadouts_ADuplicatedReferenceIsReadByItsFirstCopy(t *testing.T) {
	ctx := context.Background()
	for _, firstLocked := range []bool{true, false} {
		t.Run(fmt.Sprintf("first copy locked=%v", firstLocked), func(t *testing.T) {
			f := newBackfillFixture(t)
			f.row(t, "a", "m1", true, false)
			writeDuplicatedLock(t, f, firstLocked)

			deploy, err := f.svc.PlanDeploy(ctx, f.game, "a", core.DeployOptions{})
			require.NoError(t, err)
			require.Len(t, deploy.Mods, 1)
			assert.Equal(t, firstLocked, deploy.Mods[0].Ref.Locked, "deploy preview")

			list, err := f.svc.ListMods(ctx, f.game, "a")
			require.NoError(t, err)
			require.Len(t, list.Mods, 1)
			assert.Equal(t, firstLocked, list.Mods[0].Locked, "lmm list")
			if firstLocked {
				assert.Equal(t, "1.0", list.Mods[0].LockedVersion, "lmm list's lock target")
			} else {
				assert.Empty(t, list.Mods[0].LockedVersion)
			}

			row, err := f.svc.GetInstalledMod(ctx, "src", "m1", f.game.ID, "a")
			require.NoError(t, err)
			update, err := f.svc.PlanUpdateFrom(ctx, f.game, "a", domain.Update{InstalledMod: *row, NewVersion: "3.0"})
			require.NoError(t, err)
			// `lmm mod show` and verify read the lock through the same
			// Profile.FindRef the update gate does.
			assert.Equal(t, firstLocked, update.Locked, "the update gate")
		})
	}
}

// TestReorder_ADuplicatedReferenceKeepsItsFirstCopy: a reorder rebuilt the
// list from the LAST copy of a mod listed twice and dropped the rest, so
// reordering could unlock (or lock, or switch back on) the mod every other
// flow had decided by its first copy. The first copy is what moves now, and
// the other copies are kept where they were relative to each other, after
// the reordered ones.
func TestReorder_ADuplicatedReferenceKeepsItsFirstCopy(t *testing.T) {
	ctx := context.Background()
	f := newBackfillFixture(t)
	f.row(t, "a", "m1", true, false)
	f.row(t, "a", "m0", true, false)
	writeDuplicatedLock(t, f, true)
	require.NoError(t, f.svc.NewProfileManager().AddMod(ctx, f.game.ID, "a",
		domain.ModReference{SourceID: "src", ModID: "m0", Version: "1.0"}))

	order, err := f.svc.ResolveReorder(ctx, f.game, "a", []string{"src:m0", "src:m1"})
	require.NoError(t, err)
	require.NoError(t, f.svc.ReorderProfileMods(ctx, f.game.ID, "a", order))

	profile, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "a")
	require.NoError(t, err)
	var got []string
	for _, ref := range profile.Mods {
		got = append(got, fmt.Sprintf("%s@%s locked=%v", ref.ModID, ref.Version, ref.Locked))
	}
	assert.Equal(t, []string{"m0@1.0 locked=false", "m1@1.0 locked=true", "m1@2.0 locked=false"}, got)
}

package core_test

// #613 (owner ruling): any deliberate change the user makes to a
// collection's entry makes it theirs - a manual update, a rollback, an
// update from a file and a FOMOD reconfigure clear from_collection, as
// lock, unlock and a re-link do - so a collection update lists it as a
// conflict instead of silently moving it back. An update the auto policy
// drives keeps the marker: following the newest file is what that policy
// (a collection's "latest") already asked for.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installedCollectionMod is mod1 at 1.0 with recorded choices, its entry
// marked as a collection's.
func installedCollectionMod(t *testing.T) *fomodFixture {
	t.Helper()
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	f.markFromCollection(t)
	return f
}

func (f *fomodFixture) markFromCollection(t *testing.T) {
	t.Helper()
	p, err := config.LoadProfile(f.svc.ConfigDir(), "g1", "default")
	require.NoError(t, err)
	p.FindRef("src", "mod1").FromCollection = true
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))
}

func (f *fomodFixture) updateTo(t *testing.T, version, tag string) {
	t.Helper()
	ctx := context.Background()
	f.serve(t, "mod1", version, "4K", tag)
	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	plan, err := f.svc.PlanUpdateFrom(ctx, f.game, "default", domain.Update{InstalledMod: *row, NewVersion: version})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdate(ctx, f.game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
}

func TestFromCollection_AManualUpdateMakesTheEntryTheUsers(t *testing.T) {
	f := installedCollectionMod(t)
	f.updateTo(t, "1.1", "v2")
	ref := f.ref(t, "default", "mod1")
	assert.Equal(t, "1.1", ref.Version)
	assert.False(t, ref.FromCollection)
}

func TestFromCollection_AnAutoPolicyUpdateKeepsTheMarker(t *testing.T) {
	f := installedCollectionMod(t)
	ctx := context.Background()
	_, err := f.svc.SetModUpdatePolicy(ctx, "src", "mod1", "g1", "default", domain.UpdateAuto)
	require.NoError(t, err)
	f.updateTo(t, "1.1", "v2")
	ref := f.ref(t, "default", "mod1")
	assert.Equal(t, "1.1", ref.Version)
	assert.True(t, ref.FromCollection, "the auto policy's update is what the collection asked for")
}

func TestFromCollection_ARollbackMakesTheEntryTheUsers(t *testing.T) {
	f := installedCollectionMod(t)
	ctx := context.Background()
	_, err := f.svc.SetModUpdatePolicy(ctx, "src", "mod1", "g1", "default", domain.UpdateAuto)
	require.NoError(t, err)
	f.updateTo(t, "1.1", "v2")
	require.True(t, f.ref(t, "default", "mod1").FromCollection)

	plan, err := f.svc.PlanRollback(ctx, f.game, "default", "src", "mod1")
	require.NoError(t, err)
	_, err = f.svc.ApplyRollback(ctx, f.game, plan, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	ref := f.ref(t, "default", "mod1")
	assert.Equal(t, "1.0", ref.Version)
	assert.False(t, ref.FromCollection)
}

func TestFromCollection_AnUpdateFromAFileMakesTheEntryTheUsers(t *testing.T) {
	f := installedCollectionMod(t)
	ctx := context.Background()
	// Even on the auto policy: the file is the user's pick.
	_, err := f.svc.SetModUpdatePolicy(ctx, "src", "mod1", "g1", "default", domain.UpdateAuto)
	require.NoError(t, err)
	archive := writeFomodArchive(t, "mod1-2.0.zip", "4K", "v2")
	plan, err := f.svc.PlanUpdateFromArchive(ctx, f.game, "default", "src", "mod1", archive, core.UpdateFromArchiveOptions{Version: "2.0"})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdateFromArchive(ctx, f.game, plan, core.UpdateFromArchiveOptions{Version: "2.0", AcceptMismatch: true}, nil)
	require.NoError(t, err)
	ref := f.ref(t, "default", "mod1")
	assert.Equal(t, "2.0", ref.Version)
	assert.False(t, ref.FromCollection)
}

func TestFromCollection_AFomodReconfigureMakesTheEntryTheUsers(t *testing.T) {
	f := installedCollectionMod(t)
	ctx := context.Background()
	plan, err := f.svc.PlanFomodReconfigure(ctx, f.game, "default", "src", "mod1")
	require.NoError(t, err)
	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{
		Fomod: map[string]domain.FomodChoices{"src:mod1": answer("2K")},
	}, nil)
	require.NoError(t, err)
	ref := f.ref(t, "default", "mod1")
	assert.Equal(t, "2K", ref.Fomod.Steps[0].Groups[0].Choices[0].Name)
	assert.False(t, ref.FromCollection)
}

// A reinstall of the same mod (not a reconfigure) is not an edit.
func TestFromCollection_AReinstallKeepsTheMarker(t *testing.T) {
	f := installedCollectionMod(t)
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	assert.True(t, f.ref(t, "default", "mod1").FromCollection)
}

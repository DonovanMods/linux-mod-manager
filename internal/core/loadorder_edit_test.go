package core_test

// #620: the file load-order report and its Plan/Apply edits, through the
// real cyberpunk2077 adapter.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func orderFileNames(r *core.FileLoadOrderReport) []string {
	var out []string
	for _, e := range r.Files {
		out = append(out, e.Name)
	}
	return out
}

func TestFileLoadOrders_ReportsTheEffectiveOrder(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive")
	f.mod(t, "b", "b.archive")
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "mine.archive"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "modlist.txt"), []byte("mine.archive\r\n"), 0o644))
	f.deploy(t)

	report, err := f.svc.FileLoadOrders(context.Background(), f.game, "default")
	require.NoError(t, err)
	require.Len(t, report.Orders, 1)
	o := report.Orders[0]
	assert.Equal(t, archiveOrder, o.ID)
	assert.Equal(t, "Archives", o.Label)
	assert.True(t, o.Active)
	assert.True(t, o.FirstWins)
	assert.False(t, o.Recorded, "the profile orders nothing yet")
	// The archives lmm deploys and the profile does not order take their
	// alphabetical places around the external one the list names.
	assert.Equal(t, []string{"a.archive", "b.archive", "mine.archive"}, orderFileNames(&o))
	assert.Equal(t, core.LoadOrderPlacedExternal, o.Files[2].Placement)
	assert.False(t, o.Files[2].Managed)
	assert.Equal(t, core.LoadOrderPlacedDefault, o.Files[0].Placement)
	assert.True(t, o.Files[0].Managed)
	assert.Equal(t, "a", o.Files[0].ModID)
	assert.Equal(t, "a", o.Files[0].ModName)
	assert.Equal(t, "mine.archive\r\n", f.modlist(t), "a report writes nothing")
}

func TestFileLoadOrders_RefusesAGameWithoutOne(t *testing.T) {
	f := newArchiveFixture(t)
	g := *f.game
	g.Adapter = ""
	_, err := f.svc.FileLoadOrders(context.Background(), &g, "default")
	assert.ErrorIs(t, err, core.ErrNoFileLoadOrder)
}

func TestFileLoadOrderEdit_MoveRecordsTheOrderAndWritesTheList(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive", "b.archive", "c.archive")
	f.deploy(t)
	ctx := context.Background()

	plan, err := f.svc.PlanFileLoadOrderEdit(ctx, f.game, "default", core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "c.archive", To: core.LoadOrderMoveFirst})
	require.NoError(t, err)
	assert.Equal(t, archiveOrder, plan.Edit.ID, "the game's only order is named")
	assert.True(t, plan.Active)
	assert.False(t, plan.NoChanges)
	assert.Equal(t, []string{"c.archive", "a.archive", "b.archive"}, orderFileNames(plan.Order))
	assertAbsent(t, filepath.Join(f.modDir, "modlist.txt"), "a plan writes nothing")

	res, err := f.svc.ApplyFileLoadOrderEdit(ctx, f.game, plan)
	require.NoError(t, err)
	assert.True(t, res.Changed)
	require.NotNil(t, res.List)
	assert.Equal(t, core.PluginListWritten, res.List.Status)
	assert.Equal(t, "c.archive\r\na.archive\r\nb.archive", f.modlist(t))
	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.FileLoadOrders{archiveOrder: {"c.archive", "a.archive", "b.archive"}}, profile.LoadOrders)
	assert.True(t, res.Order.Recorded)

	plan, err = f.svc.PlanFileLoadOrderEdit(ctx, f.game, "default", core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "a.archive", After: "b.archive"})
	require.NoError(t, err)
	_, err = f.svc.ApplyFileLoadOrderEdit(ctx, f.game, plan)
	require.NoError(t, err)
	assert.Equal(t, "c.archive\r\nb.archive\r\na.archive", f.modlist(t))

	// Reset hands the list back: lmm created it, so it goes.
	plan, err = f.svc.PlanFileLoadOrderEdit(ctx, f.game, "default", core.FileLoadOrderEdit{Op: core.LoadOrderOpReset})
	require.NoError(t, err)
	require.Len(t, plan.Changes, 1)
	res, err = f.svc.ApplyFileLoadOrderEdit(ctx, f.game, plan)
	require.NoError(t, err)
	require.NotNil(t, res.List)
	assert.Equal(t, core.PluginListRemoved, res.List.Status)
	assertAbsent(t, filepath.Join(f.modDir, "modlist.txt"), "reset")
	assert.False(t, res.Order.Recorded)
}

// A move keeps the places of entries whose files are not deployed.
func TestFileLoadOrderEdit_MoveKeepsUndeployedEntries(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive", "b.archive")
	f.setOrder(t, "b.archive", "gone.archive", "a.archive")
	f.deploy(t)
	ctx := context.Background()
	plan, err := f.svc.PlanFileLoadOrderEdit(ctx, f.game, "default", core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "a.archive", Before: "b.archive"})
	require.NoError(t, err)
	assert.Equal(t, []string{"gone.archive"}, plan.Order.NotDeployed)
	_, err = f.svc.ApplyFileLoadOrderEdit(ctx, f.game, plan)
	require.NoError(t, err)
	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.archive", "b.archive", "gone.archive"}, profile.LoadOrders[archiveOrder])
}

func TestFileLoadOrderEdit_Refusals(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive")
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "mine.archive"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.modDir, "modlist.txt"), []byte("mine.archive"), 0o644))
	f.deploy(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		edit core.FileLoadOrderEdit
		want error
	}{
		"unknown file":     {core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "nope.archive", To: "first"}, core.ErrLoadOrderFileNotFound},
		"external file":    {core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "mine.archive", To: "first"}, core.ErrLoadOrderFileNotOwned},
		"no destination":   {core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "a.archive"}, core.ErrInvalidLoadOrderEdit},
		"two destinations": {core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "a.archive", To: "first", Before: "mine.archive"}, core.ErrInvalidLoadOrderEdit},
		"bad to":           {core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "a.archive", To: "middle"}, core.ErrInvalidLoadOrderEdit},
		"unknown op":       {core.FileLoadOrderEdit{Op: "sort"}, core.ErrInvalidLoadOrderEdit},
		"unknown order":    {core.FileLoadOrderEdit{ID: "cyberpunk2077-redmod", Op: core.LoadOrderOpReset}, core.ErrNoFileLoadOrder},
		"anchor external":  {core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "a.archive", Before: "mine.archive"}, core.ErrLoadOrderFileNotOwned},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.PlanFileLoadOrderEdit(ctx, f.game, "default", tc.edit)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.want), "%v", err)
		})
	}
}

func TestFileLoadOrderEdit_StalePlanIsRefused(t *testing.T) {
	f := newArchiveFixture(t)
	f.mod(t, "a", "a.archive", "b.archive")
	f.deploy(t)
	ctx := context.Background()
	plan, err := f.svc.PlanFileLoadOrderEdit(ctx, f.game, "default", core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "b.archive", To: "first"})
	require.NoError(t, err)
	f.setOrder(t, "a.archive", "b.archive")
	_, err = f.svc.ApplyFileLoadOrderEdit(ctx, f.game, plan)
	assert.ErrorIs(t, err, core.ErrStalePlan)
}

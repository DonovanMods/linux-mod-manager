package core_test

// #592: the user category is profile intent, so every flow that carries the
// disabled marker from one profile to another carries it too - mod copy,
// profile duplicate, and an export's round trip - and the source category
// name rides on the copied row.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCategory_TravelsWithTheProfile(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedRow(t, "default", "a", "1.0", map[string][]byte{"a.esp": []byte("A")}, false)
	require.NoError(t, f.svc.SetModCategoryNameForTest(ctx, "src", "a", f.game.ID, "default", "Armour"))
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}, Category: "Favourites"})

	t.Run("mod copy", func(t *testing.T) {
		plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "alt", SourceID: "src", ModID: "a"})
		require.NoError(t, err)
		require.Len(t, plan.Mods, 1)
		assert.Equal(t, "Favourites", plan.Mods[0].Category)
		_, err = f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{}, nil)
		require.NoError(t, err)

		alt, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "alt")
		require.NoError(t, err)
		require.NotNil(t, alt.FindRef("src", "a"))
		assert.Equal(t, "Favourites", alt.FindRef("src", "a").Category)
		row, err := f.svc.GetInstalledMod(ctx, "src", "a", f.game.ID, "alt")
		require.NoError(t, err)
		assert.Equal(t, "Armour", row.CategoryName, "the row's source category name is copied with it")
	})

	t.Run("duplicate", func(t *testing.T) {
		_, err := f.svc.DuplicateProfile(ctx, f.game.ID, "default", "dup")
		require.NoError(t, err)
		dup, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "dup")
		require.NoError(t, err)
		assert.Equal(t, "Favourites", dup.FindRef("src", "a").Category)
	})

	t.Run("export round trip", func(t *testing.T) {
		pm := f.svc.NewProfileManager()
		data, err := pm.Export(ctx, f.game.ID, "default")
		require.NoError(t, err)
		parsed, err := pm.ParseProfile(data)
		require.NoError(t, err)
		assert.Equal(t, "Favourites", parsed.FindRef("src", "a").Category)
	})
}

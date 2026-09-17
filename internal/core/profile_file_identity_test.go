package core_test

// #441 at the flows: a profile is the file it lives in. A copy of
// default.yaml saved by hand as vanilla.yaml, `name: default` and all, used
// to be written back to default.yaml by every change to vanilla - and to
// read as "default" wherever a flow used the loaded profile's name.

import (
	"context"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAHandCopiedProfileIsItsOwnFile(t *testing.T) {
	ctx := context.Background()
	f := newBackfillFixture(t) // "a" is active
	f.row(t, "a", "m1", true, false)
	require.NoError(t, f.svc.NewProfileManager().SetModDisabled(ctx, f.game.ID, "a", "src", "m1", true))
	original := mustRead(t, f.profilePath("a"))
	require.NoError(t, os.WriteFile(f.profilePath("vanilla"), []byte(original), 0o644))

	pm := f.svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, f.game.ID, "vanilla", domain.ModReference{SourceID: "src", ModID: "m2"}))
	require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "vanilla", "src", "m1", false))

	assert.Equal(t, original, mustRead(t, f.profilePath("a")), "a's file - its marker and is_default - is untouched")
	vanilla, err := pm.Get(ctx, f.game.ID, "vanilla")
	require.NoError(t, err)
	assert.Equal(t, "vanilla", vanilla.Name)
	require.Len(t, vanilla.Mods, 2)
	assert.False(t, vanilla.Mods[0].Disabled)

	active, err := pm.GetDefault(ctx, f.game.ID)
	require.NoError(t, err)
	assert.Equal(t, "a", active.Name, "the copy carries is_default too, and the first flagged file still answers")

	listing, err := f.svc.ListProfiles(ctx, f.game.ID)
	require.NoError(t, err)
	assert.Contains(t, listing.Warnings, "profile file vanilla.yaml of g1 says `name: a` - lmm knows it by its file name, \"vanilla\"; set its name: to vanilla, or rename the file")
}

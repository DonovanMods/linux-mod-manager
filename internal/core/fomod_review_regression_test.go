package core_test

// #614 F2b review B1, the reviewer's reproductions as regressions: recorded
// FOMOD choices are cleared when the mod's tree stops being an installer's,
// and restored by a rollback, so converge flows never reinstall forever.

import (
	"context"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFomodUpdate_ToAPlainArchiveClearsTheChoicesAndConverges: three
// applies after a FOMOD mod updates to a plain archive plan nothing.
func TestFomodUpdate_ToAPlainArchiveClearsTheChoicesAndConverges(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)

	zipPath := createTestZip(t, t.TempDir(), map[string]string{"plain.esp": "PLAIN"})
	b, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	f.mock.AddDownload("mod1", b)
	f.mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod mod1", Version: "1.1", GameID: "g1"})

	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	plan, err := f.svc.PlanUpdateFrom(ctx, f.game, "default", domain.Update{InstalledMod: *row, NewVersion: "1.1"})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdate(ctx, f.game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	row, _ = f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	assert.Empty(t, row.Variant, "the plain tree")
	assert.True(t, f.ref(t, "default", "mod1").Fomod.IsZero(), "the choices are cleared")

	for i := 0; i < 2; i++ {
		ap, err := f.svc.PlanProfileApply(ctx, f.game, "default")
		require.NoError(t, err)
		assert.Empty(t, ap.ToInstall, "apply %d plans nothing", i)
		if len(ap.ToInstall) > 0 {
			_, err = f.svc.ApplyProfileApply(ctx, f.game, ap, core.ProfileApplyOptions{}, nil)
			require.NoError(t, err)
		}
	}
	ap, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	assert.Empty(t, ap.ToInstall)
}

// TestFomodRollback_RestoresThePreviousChoices: install 1.0 with 4K,
// update to 1.2 with Ultra, roll back - the profile records 1.0's choices
// and installer again, and apply plans nothing.
func TestFomodRollback_RestoresThePreviousChoices(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	f.serve(t, "mod1", "1.2", "Ultra", "v3")
	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	plan, err := f.svc.PlanUpdateFrom(ctx, f.game, "default", domain.Update{InstalledMod: *row, NewVersion: "1.2"})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdate(ctx, f.game, plan, core.UpdateOptions{Fomod: map[string]domain.FomodChoices{"src:mod1": answer("Ultra")}}, nil)
	require.NoError(t, err)

	rp, err := f.svc.PlanRollback(ctx, f.game, "default", "src", "mod1")
	require.NoError(t, err)
	_, err = f.svc.ApplyRollback(ctx, f.game, rp, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"))
	ref := f.ref(t, "default", "mod1")
	assert.Equal(t, "4K", ref.Fomod.Steps[0].Groups[0].Choices[0].Name)
	row, err = f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, core.FomodVariantForTest(ref.Fomod), row.Variant, "the restored choices name the restored tree")
	assert.Equal(t, "Ultra", row.PreviousFomod.Steps[0].Groups[0].Choices[0].Name, "and 1.2's become the previous ones")
	ap, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	assert.Empty(t, ap.ToInstall)
}

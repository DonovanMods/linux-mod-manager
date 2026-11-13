package db_test

// #614: installed_mods.variant names which tree of the version the row
// installed (FOMOD choices, later exact file lists and patches), and
// previous_variant the tree of previous_version, so a rollback restores the
// right cache entry. Neither is ever part of the version.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func variantTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	return database
}

func variantRow(variant string) *domain.InstalledMod {
	return &domain.InstalledMod{
		Mod:          domain.Mod{ID: "12345", SourceID: "nexusmods", Name: "Test Mod", Version: "1.0.0", GameID: "skyrim-se", Variant: variant},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"f1"},
	}
}

func readVariants(t *testing.T, database *db.DB) (version, variant, previousVersion, previousVariant string) {
	t.Helper()
	got, err := database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	mods, err := database.GetInstalledMods(context.Background(), "skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, got.Variant, mods[0].Variant, "the list read agrees")
	assert.Equal(t, got.PreviousVariant, mods[0].PreviousVariant, "the list read agrees")
	return got.Version, got.Variant, got.PreviousVersion, got.PreviousVariant
}

func TestSaveInstalledMod_RoundTripsVariant(t *testing.T) {
	database := variantTestDB(t)
	row := variantRow("abcdef123456")
	row.PreviousVersion, row.PreviousVariant = "0.9", "0123456789ab"
	require.NoError(t, database.SaveInstalledMod(context.Background(), row))

	version, variant, prevVersion, prevVariant := readVariants(t, database)
	assert.Equal(t, "1.0.0", version, "the variant is never part of the version")
	assert.Equal(t, "abcdef123456", variant)
	assert.Equal(t, "0.9", prevVersion)
	assert.Equal(t, "0123456789ab", prevVariant)

	// A re-save writes the row's tree as it is now: a plain reinstall
	// moves the row back to the plain entry.
	require.NoError(t, database.SaveInstalledMod(context.Background(), variantRow("")))
	_, variant, _, _ = readVariants(t, database)
	assert.Empty(t, variant)
}

func TestApplyModUpdate_ShiftsVariantWithVersion(t *testing.T) {
	database := variantTestDB(t)
	ctx := context.Background()
	require.NoError(t, database.SaveInstalledMod(ctx, variantRow("aaaaaaaaaaaa")))

	require.NoError(t, database.ApplyModUpdate(ctx, "nexusmods", "12345", "skyrim-se", "default", "2.0.0", "bbbbbbbbbbbb", domain.FomodChoices{}, []string{"f2"}))
	version, variant, prevVersion, prevVariant := readVariants(t, database)
	assert.Equal(t, []string{"2.0.0", "bbbbbbbbbbbb", "1.0.0", "aaaaaaaaaaaa"}, []string{version, variant, prevVersion, prevVariant})

	require.NoError(t, swapNoFomod(database.SwapModVersions(ctx, "nexusmods", "12345", "skyrim-se", "default", domain.FomodChoices{})))
	version, variant, prevVersion, prevVariant = readVariants(t, database)
	assert.Equal(t, []string{"1.0.0", "aaaaaaaaaaaa", "2.0.0", "bbbbbbbbbbbb"}, []string{version, variant, prevVersion, prevVariant},
		"a rollback restores the previous version's tree, not just its version")
}

func TestUpdateModVersion_ShiftsVariantWithVersion(t *testing.T) {
	database := variantTestDB(t)
	ctx := context.Background()
	require.NoError(t, database.SaveInstalledMod(ctx, variantRow("aaaaaaaaaaaa")))

	require.NoError(t, database.UpdateModVersion(ctx, "nexusmods", "12345", "skyrim-se", "default", "2.0.0", ""))
	version, variant, prevVersion, prevVariant := readVariants(t, database)
	assert.Equal(t, []string{"2.0.0", "", "1.0.0", "aaaaaaaaaaaa"}, []string{version, variant, prevVersion, prevVariant})
}

// TestSetModVersion_KeepsVariant: a version CORRECTION (verify --fix #94)
// renames the entry it already has; the tree is the same tree.
func TestSetModVersion_KeepsVariant(t *testing.T) {
	database := variantTestDB(t)
	ctx := context.Background()
	require.NoError(t, database.SaveInstalledMod(ctx, variantRow("aaaaaaaaaaaa")))
	require.NoError(t, database.SetModVersion(ctx, "nexusmods", "12345", "skyrim-se", "default", "1.0.1"))
	version, variant, _, _ := readVariants(t, database)
	assert.Equal(t, "1.0.1", version)
	assert.Equal(t, "aaaaaaaaaaaa", variant)
}

func TestRelinkInstalledMod_CarriesVariant(t *testing.T) {
	database := variantTestDB(t)
	ctx := context.Background()
	require.NoError(t, database.SaveInstalledMod(ctx, variantRow("aaaaaaaaaaaa")))
	moved := variantRow("aaaaaaaaaaaa")
	moved.ID = "999"
	moved.PreviousVersion, moved.PreviousVariant = "0.9", "cccccccccccc"
	require.NoError(t, database.RelinkInstalledMod(ctx, "nexusmods", "12345", moved))

	got, err := database.GetInstalledMod(ctx, "nexusmods", "999", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "aaaaaaaaaaaa", got.Variant)
	assert.Equal(t, "cccccccccccc", got.PreviousVariant)
}

func TestModVersionRows_CarriesVariants(t *testing.T) {
	database := variantTestDB(t)
	ctx := context.Background()
	row := variantRow("aaaaaaaaaaaa")
	row.PreviousVersion, row.PreviousVariant = "0.9", "cccccccccccc"
	require.NoError(t, database.SaveInstalledMod(ctx, row))

	rows, err := database.ModVersionRows(ctx, "nexusmods", "12345")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "aaaaaaaaaaaa", rows[0].Variant)
	assert.Equal(t, "cccccccccccc", rows[0].PreviousVariant)
}

// TestSwapModVersions_SwapsVariants: rollback swaps the variant with the
// version, both ways.
func TestSwapModVersions_SwapsVariants(t *testing.T) {
	database := variantTestDB(t)
	ctx := context.Background()
	row := variantRow("aaaaaaaaaaaa")
	row.Version, row.PreviousVersion, row.PreviousVariant = "2.0", "1.0", ""
	require.NoError(t, database.SaveInstalledMod(ctx, row))

	require.NoError(t, swapNoFomod(database.SwapModVersions(ctx, "nexusmods", "12345", "skyrim-se", "default", domain.FomodChoices{})))
	version, variant, prevVersion, prevVariant := readVariants(t, database)
	assert.Equal(t, []string{"1.0", "", "2.0", "aaaaaaaaaaaa"}, []string{version, variant, prevVersion, prevVariant},
		"a plain previous version swaps in, and the variant moves to previous")

	require.NoError(t, swapNoFomod(database.SwapModVersions(ctx, "nexusmods", "12345", "skyrim-se", "default", domain.FomodChoices{})))
	version, variant, prevVersion, prevVariant = readVariants(t, database)
	assert.Equal(t, []string{"2.0", "aaaaaaaaaaaa", "1.0", ""}, []string{version, variant, prevVersion, prevVariant})
}

// TestPreviousFomod_RoundTrips (#614): the choices the previous version
// was installed with survive a save and a read.
func TestPreviousFomod_RoundTrips(t *testing.T) {
	database := variantTestDB(t)
	ctx := context.Background()
	choices := domain.FomodChoices{ConfigSHA256: "ab", Steps: []fomod.StepChoice{{Name: "Main", Groups: []fomod.GroupChoice{{Name: "T", Choices: []fomod.OptionChoice{{Name: "4K", Idx: 1}}}}}}}
	row := variantRow("aaaaaaaaaaaa")
	row.PreviousFomod = choices
	require.NoError(t, database.SaveInstalledMod(ctx, row))
	got, err := database.GetInstalledMod(ctx, "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, choices.Equal(got.PreviousFomod))
	mods, err := database.GetInstalledMods(ctx, "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, choices.Equal(mods[0].PreviousFomod))

}

// swapNoFomod drops SwapModVersions' restored choices, for a test that
// only asks whether the swap succeeded.
func swapNoFomod(_ domain.FomodChoices, err error) error { return err }

// TestPreviousFomod_UpdateAndRollbackSwapItTransactionally (#614 re-review
// should-fixes 2 and 3): an update records the replaced choices as
// previous_fomod in its own transaction, and a rollback swaps them out (and
// the current ones in) in the swap's - so a second swap restores both.
func TestPreviousFomod_UpdateAndRollbackSwapItTransactionally(t *testing.T) {
	database := variantTestDB(t)
	ctx := context.Background()
	old := domain.FomodChoices{ConfigSHA256: "aa", Steps: []fomod.StepChoice{{Name: "Main", Groups: []fomod.GroupChoice{{Name: "T", Choices: []fomod.OptionChoice{{Name: "4K"}}}}}}}
	cur := domain.FomodChoices{ConfigSHA256: "bb", Steps: []fomod.StepChoice{{Name: "Main", Groups: []fomod.GroupChoice{{Name: "T", Choices: []fomod.OptionChoice{{Name: "Ultra"}}}}}}}
	require.NoError(t, database.SaveInstalledMod(ctx, variantRow("aaaaaaaaaaaa")))

	require.NoError(t, database.ApplyModUpdate(ctx, "nexusmods", "12345", "skyrim-se", "default", "2.0.0", "bbbbbbbbbbbb", old, []string{"f2"}))
	got, err := database.GetInstalledMod(ctx, "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, old.Equal(got.PreviousFomod), "the update recorded the replaced choices")

	restored, err := database.SwapModVersions(ctx, "nexusmods", "12345", "skyrim-se", "default", cur)
	require.NoError(t, err)
	assert.True(t, old.Equal(restored), "the rollback hands back the restored version's choices")
	got, err = database.GetInstalledMod(ctx, "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, cur.Equal(got.PreviousFomod), "and keeps the rolled-away-from ones")

	// A failed profile write swaps back with what it restored.
	back, err := database.SwapModVersions(ctx, "nexusmods", "12345", "skyrim-se", "default", restored)
	require.NoError(t, err)
	assert.True(t, cur.Equal(back))
	got, err = database.GetInstalledMod(ctx, "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", got.Version)
	assert.True(t, old.Equal(got.PreviousFomod), "every column is back as the update left it")
}

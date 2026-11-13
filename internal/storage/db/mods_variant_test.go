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

	require.NoError(t, database.ApplyModUpdate(ctx, "nexusmods", "12345", "skyrim-se", "default", "2.0.0", "bbbbbbbbbbbb", []string{"f2"}))
	version, variant, prevVersion, prevVariant := readVariants(t, database)
	assert.Equal(t, []string{"2.0.0", "bbbbbbbbbbbb", "1.0.0", "aaaaaaaaaaaa"}, []string{version, variant, prevVersion, prevVariant})

	require.NoError(t, database.SwapModVersions(ctx, "nexusmods", "12345", "skyrim-se", "default"))
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

	require.NoError(t, database.SwapModVersions(ctx, "nexusmods", "12345", "skyrim-se", "default"))
	version, variant, prevVersion, prevVariant := readVariants(t, database)
	assert.Equal(t, []string{"1.0", "", "2.0", "aaaaaaaaaaaa"}, []string{version, variant, prevVersion, prevVariant},
		"a plain previous version swaps in, and the variant moves to previous")

	require.NoError(t, database.SwapModVersions(ctx, "nexusmods", "12345", "skyrim-se", "default"))
	version, variant, prevVersion, prevVariant = readVariants(t, database)
	assert.Equal(t, []string{"2.0", "aaaaaaaaaaaa", "1.0", ""}, []string{version, variant, prevVersion, prevVariant})
}

// TestPreviousFomod_RoundTripsAndIsSetOnItsOwn (#614): the choices the
// previous version was installed with survive a save and a read, and
// SetModPreviousFomod writes that one column.
func TestPreviousFomod_RoundTripsAndIsSetOnItsOwn(t *testing.T) {
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

	require.NoError(t, database.SetModPreviousFomod(ctx, "nexusmods", "12345", "skyrim-se", "default", domain.FomodChoices{}))
	got, err = database.GetInstalledMod(ctx, "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, got.PreviousFomod.IsZero())
	assert.ErrorIs(t, database.SetModPreviousFomod(ctx, "nexusmods", "nope", "skyrim-se", "default", choices), domain.ErrModNotFound)
}

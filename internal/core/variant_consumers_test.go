package core_test

// #614: a cache entry whose tree depends on more than the source file (FOMOD
// choices) is a VARIANT of its version - a sibling directory,
// <version>@<variant> - named by installed_mods.variant. Every consumer that
// lists, deploys, verifies, converges, purges, copies or rolls back an
// installed mod's entry must read the row's variant entry and never the
// plain one beside it. Each test seeds both: the variant entry the row names,
// and a DECOY plain entry at the same version with different content, so a
// consumer that keys by the version alone deploys or keeps the wrong tree.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	variantA = "aaaaaaaaaaaa"
	variantB = "bbbbbbbbbbbb"
)

// seedVariantRow seeds profile's row for mod a at version with variant, its
// variant cache entry holding a.esp = content, and a decoy plain entry at
// the same version holding a.esp = "PLAIN" and decoy.esp.
func (f *copyFixture) seedVariantRow(t *testing.T, profile, version, variant, content string, deployed bool) *domain.InstalledMod {
	t.Helper()
	c := f.svc.GetGameCache(f.game)
	key := cache.Key{GameID: f.game.ID, SourceID: "src", ModID: "a", Version: version, Variant: variant}
	require.NoError(t, c.Store(key, "a.esp", []byte(content)))
	require.NoError(t, cache.MarkFileCompleteWithMembers(c.ModPath(key), "f-a", []string{"a.esp"}))
	plain := key
	plain.Variant = ""
	if !c.Exists(plain) {
		require.NoError(t, c.Store(plain, "a.esp", []byte("PLAIN")))
		require.NoError(t, c.Store(plain, "decoy.esp", []byte("DECOY")))
	}
	row := &domain.InstalledMod{
		Mod:         domain.Mod{ID: "a", SourceID: "src", Name: "Mod a", Version: version, GameID: f.game.ID, Variant: variant},
		ProfileName: profile, UpdatePolicy: domain.UpdatePinned, Enabled: true, Deployed: deployed,
		FileIDs: []string{"f-a"},
	}
	require.NoError(t, f.svc.SaveInstalledMod(context.Background(), row))
	return row
}

func recordedChoices() domain.FomodChoices {
	return domain.FomodChoices{ConfigSHA256: "ab12", Steps: []fomod.StepChoice{{Name: "Main",
		Groups: []fomod.GroupChoice{{Name: "Texture", Choices: []fomod.OptionChoice{{Name: "2K", Idx: 1}}}}}}}
}

func (f *copyFixture) deployedContent(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.game.ModPath, name))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(b)
}

func (f *copyFixture) variantEntry(version, variant string) cache.Key {
	return cache.Key{GameID: f.game.ID, SourceID: "src", ModID: "a", Version: version, Variant: variant}
}

func TestVariant_DeployLinksTheVariantTree(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedVariantRow(t, "default", "1.0", variantA, "VARIANT", false)
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}, Fomod: recordedChoices()})

	_, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "VARIANT", f.deployedContent(t, "a.esp"))
	assert.Empty(t, f.deployedContent(t, "decoy.esp"), "the plain entry's files are not this row's")

	files, err := f.svc.ModFiles(ctx, f.game, "default", "src", "a")
	require.NoError(t, err)
	require.Len(t, files.Files, 1)
	assert.Equal(t, "a.esp", files.Files[0].Path)
}

func TestVariant_VerifyChecksTheVariantEntry(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedVariantRow(t, "default", "1.0", variantA, "VARIANT", false)
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}})
	_, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, f.svc.SaveFileChecksum(ctx, "src", "a", f.game.ID, "default", "f-a", "0123456789abcdef0123456789abcdef"))

	report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	assert.Zero(t, report.Result.Issues, "the variant entry is the row's entry: nothing missing, no count mismatch; findings %+v", report.Result.Findings)

	// With the variant entry gone, the plain one beside it does not stand in.
	require.NoError(t, f.svc.GetGameCache(f.game).Delete(f.variantEntry("1.0", variantA)))
	report, err = f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.NotZero(t, report.Result.Issues, "a missing variant entry is missing, decoy or not")
}

func TestVariant_UninstallRemovesOnlyItsOwnEntry(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedVariantRow(t, "default", "1.0", variantA, "VARIANT", false)
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}})
	_, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	result, err := f.svc.UninstallMod(ctx, f.game, "default", "src", "a", core.UninstallOptions{})
	require.NoError(t, err)
	assert.Empty(t, result.CacheUsedBy)
	c := f.svc.GetGameCache(f.game)
	assert.False(t, c.Exists(f.variantEntry("1.0", variantA)), "the row's variant entry goes")
	assert.True(t, c.Exists(f.variantEntry("1.0", "")), "the plain entry of the same version is not its")
	assert.Empty(t, f.deployedContent(t, "a.esp"))
}

// TestVariant_CacheEntryUsersCompareTheTree: another profile's row of the
// same version holds the entry only when it has the same tree.
func TestVariant_CacheEntryUsersCompareTheTree(t *testing.T) {
	for name, tc := range map[string]struct {
		otherVariant string
		kept         bool
	}{
		"same variant":    {variantA, true},
		"another variant": {variantB, false},
		"the plain tree":  {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCopyFixture(t)
			ctx := context.Background()
			f.seedVariantRow(t, "default", "1.0", variantA, "VARIANT", false)
			if tc.otherVariant == "" {
				f.seedRow(t, "alt", "a", "1.0", nil, false)
			} else {
				f.seedVariantRow(t, "alt", "1.0", tc.otherVariant, "OTHER", false)
			}
			f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}})

			result, err := f.svc.UninstallMod(ctx, f.game, "default", "src", "a", core.UninstallOptions{})
			require.NoError(t, err)
			assert.Equal(t, tc.kept, f.svc.GetGameCache(f.game).Exists(f.variantEntry("1.0", variantA)))
			assert.Equal(t, tc.kept, len(result.CacheUsedBy) > 0, "%v", result.CacheUsedBy)
		})
	}
}

func TestVariant_RollbackRestoresThePreviousTree(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedVariantRow(t, "default", "1.0", variantA, "OLD-VARIANT", false)
	row := f.seedVariantRow(t, "default", "2.0", variantB, "NEW-VARIANT", true)
	row.PreviousVersion, row.PreviousVariant, row.PreviousFileIDs = "1.0", variantA, []string{"f-a"}
	require.NoError(t, f.svc.SaveInstalledMod(ctx, row))
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "2.0", FileIDs: []string{"f-a"}})
	_, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, "NEW-VARIANT", f.deployedContent(t, "a.esp"))

	plan, err := f.svc.PlanRollback(ctx, f.game, "default", "src", "a")
	require.NoError(t, err)
	assert.False(t, plan.CacheMissing, "the previous variant entry is cached")
	_, err = f.svc.ApplyRollback(ctx, f.game, plan, core.RollbackOptions{}, nil)
	require.NoError(t, err)

	assert.Equal(t, "OLD-VARIANT", f.deployedContent(t, "a.esp"))
	assert.Empty(t, f.deployedContent(t, "decoy.esp"))
	got, err := f.svc.GetInstalledMod(ctx, "src", "a", f.game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.0", variantA, "2.0", variantB}, []string{got.Version, got.Variant, got.PreviousVersion, got.PreviousVariant})

	// The previous variant entry gone means the rollback has nothing to
	// restore - the plain entry of 1.0 does not stand in for it.
	_, err = f.svc.ApplyRollback(ctx, f.game, mustPlanRollback(t, f), core.RollbackOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, f.svc.GetGameCache(f.game).Delete(f.variantEntry("1.0", variantA)))
	plan, err = f.svc.PlanRollback(ctx, f.game, "default", "src", "a")
	require.NoError(t, err)
	assert.True(t, plan.CacheMissing)
}

func mustPlanRollback(t *testing.T, f *copyFixture) *core.RollbackPlan {
	t.Helper()
	plan, err := f.svc.PlanRollback(context.Background(), f.game, "default", "src", "a")
	require.NoError(t, err)
	return plan
}

func TestVariant_ModCopyCarriesTheTreeAndTheChoices(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	// The variant the recorded choices name (F2b, #614): a switch to the
	// copy's profile converges to exactly that tree.
	variantA := core.FomodVariantForTest(recordedChoices())
	f.seedVariantRow(t, "default", "1.0", variantA, "VARIANT", false)
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}, Fomod: recordedChoices()})

	plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "alt", SourceID: "src", ModID: "a"})
	require.NoError(t, err)
	require.Len(t, plan.Mods, 1)
	assert.True(t, recordedChoices().Equal(plan.Mods[0].Fomod), "the plan says the choices are carried")
	_, err = f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{}, nil)
	require.NoError(t, err)

	alt, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "alt")
	require.NoError(t, err)
	require.NotNil(t, alt.FindRef("src", "a"))
	assert.True(t, recordedChoices().Equal(alt.FindRef("src", "a").Fomod))
	row, err := f.svc.GetInstalledMod(ctx, "src", "a", f.game.ID, "alt")
	require.NoError(t, err)
	assert.Equal(t, variantA, row.Variant, "the copied row names the tree its files are")

	// Deploying the copy deploys the variant tree.
	_, err = f.svc.PlanProfileSwitch(ctx, f.game, "alt")
	require.NoError(t, err)
	switchPlan, err := f.svc.PlanProfileSwitch(ctx, f.game, "alt")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, switchPlan, nil)
	require.NoError(t, err)
	assert.Equal(t, "VARIANT", f.deployedContent(t, "a.esp"))
	assert.Empty(t, f.deployedContent(t, "decoy.esp"))
}

func TestVariant_DuplicateAndExportCarryTheChoices(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedVariantRow(t, "default", "1.0", variantA, "VARIANT", false)
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}, Fomod: recordedChoices()})

	_, err := f.svc.DuplicateProfile(ctx, f.game.ID, "default", "dup")
	require.NoError(t, err)
	dup, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "dup")
	require.NoError(t, err)
	assert.True(t, recordedChoices().Equal(dup.FindRef("src", "a").Fomod))
	row, err := f.svc.GetInstalledMod(ctx, "src", "a", f.game.ID, "dup")
	require.NoError(t, err)
	assert.Equal(t, variantA, row.Variant)

	pm := f.svc.NewProfileManager()
	data, err := pm.Export(ctx, f.game.ID, "default")
	require.NoError(t, err)
	parsed, err := pm.ParseProfile(data)
	require.NoError(t, err)
	assert.True(t, recordedChoices().Equal(parsed.FindRef("src", "a").Fomod), "an export round trip keeps the choices:\n%s", data)
}

func TestVariant_PurgeRemovesTheVariantTree(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedVariantRow(t, "default", "1.0", variantA, "VARIANT", false)
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}})
	_, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	_, err = f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, f.deployedContent(t, "a.esp"))
	assert.True(t, f.svc.GetGameCache(f.game).Exists(f.variantEntry("1.0", variantA)), "purge keeps the cache")
}

// TestVariant_ACollidingPlainVersionIsNotTheVariant: a source version that
// spells the variant's directory ("1.0@<variant>") does not see the variant
// entry as its own, so it reads as uncached.
func TestVariant_ACollidingPlainVersionIsNotTheVariant(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedVariantRow(t, "default", "1.0", variantA, "VARIANT", false)
	colliding := &domain.InstalledMod{
		Mod:         domain.Mod{ID: "a", SourceID: "src", Name: "Mod a", Version: "1.0@" + variantA, GameID: f.game.ID},
		ProfileName: "alt", UpdatePolicy: domain.UpdatePinned, Enabled: true, FileIDs: []string{"f-a"},
	}
	require.NoError(t, f.svc.SaveInstalledMod(ctx, colliding))
	require.NoError(t, f.svc.SaveFileChecksum(ctx, "src", "a", f.game.ID, "alt", "f-a", "0123456789abcdef0123456789abcdef"))
	f.setRefs(t, "alt", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0@" + variantA, FileIDs: []string{"f-a"}})

	report, err := f.svc.VerifyReport(ctx, f.game, "alt", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	assert.NotZero(t, report.Result.Issues, "the colliding plain version is not cached")

	_, err = f.svc.UninstallMod(ctx, f.game, "alt", "src", "a", core.UninstallOptions{})
	require.NoError(t, err)
	assert.True(t, f.svc.GetGameCache(f.game).Exists(f.variantEntry("1.0", variantA)),
		"uninstalling the colliding plain version leaves the variant entry alone")
}

package core_test

// #657: a source collection pinning a file its source no longer offers -
// found at plan time, at most one listing call per mod, and decided per
// mod: the mod's current main file instead, or skip it. Apply refuses
// until every one is decided; the decision is recorded on the profile.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tenListing is mod 10's listing once its author removed its pinned file
// 100 and file 101 (version 2) is its main file.
func tenListing() []domain.DownloadableFile {
	return []domain.DownloadableFile{
		{ID: "101", FileName: "f101.zip", Name: "Ten 2", Version: "2", Category: "MAIN", IsPrimary: true},
	}
}

// newGonePinFixture is the three-mod collection whose list has no record
// of mod 10's pinned file (NexusMods removed it), with file 101 served as
// its current one.
func newGonePinFixture(t *testing.T) *sourceImportFixture {
	t.Helper()
	f := newSourceImportFixture(t, source.TierPremium)
	f.src.recipe.collection.Items[0].ModID = ""
	f.src.listing = map[string][]domain.DownloadableFile{"10": tenListing()}
	f.src.AddDownload("101", createTestZipBytes(t, map[string]string{"mod10.esp": "plugin 10 v2"}))
	return f
}

func itemNamed(c *core.ConvertedCollection, name string) core.ConvertedCollectionItem {
	for _, it := range c.Items {
		if it.Name == name {
			return it
		}
	}
	return core.ConvertedCollectionItem{}
}

func TestPlanSourceCollectionImport_FindsAGonePin(t *testing.T) {
	f := newGonePinFixture(t)
	plan := f.plan(t, core.SourceCollectionImportRequest{})

	c := plan.Collection.Source
	require.Len(t, c.GonePins, 1)
	pin := c.GonePins[0]
	assert.Equal(t, "nexusmods:10", pin.Key())
	assert.Equal(t, "Ten", pin.Name)
	assert.Equal(t, []string{"100"}, pin.FileIDs)
	assert.Equal(t, &core.CollectionCurrentFile{FileID: "101", Name: "Ten 2", Version: "2"}, pin.Current)
	assert.Empty(t, pin.Choice, "undecided")
	assert.Contains(t, itemNamed(c, "Ten").Note, "no longer offers")
	assert.Equal(t, map[string]int{"10": 1}, f.src.listCalls,
		"only the mod the list does not vouch for is listed, once; the optional item is not planned")

	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	var refused *core.CollectionGonePinsError
	require.ErrorAs(t, err, &refused)
	require.Len(t, refused.Pins, 1)
	assert.True(t, core.IsCollectionGonePins(err))
	assert.Contains(t, err.Error(), "--use-current")
	_, err = f.svc.NewProfileManager().Get(context.Background(), f.game.ID, plan.Profile.Name)
	assert.ErrorIs(t, err, domain.ErrProfileNotFound, "nothing was written")
}

func TestPlanSourceCollectionImport_AFileTheListLeavesOutIsChecked(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	f.src.recipe.collection.Items = f.src.recipe.collection.Items[1:]
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	assert.Empty(t, plan.Collection.Source.GonePins, "the listing offers file 100")
	assert.Equal(t, map[string]int{"10": 1}, f.src.listCalls)

	f.src.listCalls = nil
	f.src.listing = map[string][]domain.DownloadableFile{"10": tenListing()}
	plan = f.plan(t, core.SourceCollectionImportRequest{})
	require.Len(t, plan.Collection.Source.GonePins, 1, "a file the listing leaves out is gone")
}

func TestPlanSourceCollectionImport_UseCurrentInstallsTheCurrentFile(t *testing.T) {
	f := newGonePinFixture(t)
	plan := f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{UseCurrent: []string{"nexusmods:10"}}})

	c := plan.Collection.Source
	require.Len(t, c.GonePins, 1)
	assert.Equal(t, core.GonePinUseCurrent, c.GonePins[0].Choice)
	assert.Equal(t, []string{"nexusmods:10"}, c.UseCurrent, "the choice is echoed")
	assert.Contains(t, itemNamed(c, "Ten").Note, "Ten 2 (version 2) installs instead")
	ref := plan.Profile.FindRef("nexusmods", "10")
	require.NotNil(t, ref)
	assert.Equal(t, []string{"101"}, ref.FileIDs)
	assert.Equal(t, "2", ref.Version)
	assert.Equal(t, &domain.CollectionPin{Version: "1", FileIDs: []string{"100"}}, ref.SubstitutedFor)
	assert.Equal(t, 1, f.src.listCalls["10"], "the re-plan lists nothing again")

	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Installed)
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	saved10 := saved.FindRef("nexusmods", "10")
	require.NotNil(t, saved10)
	assert.Equal(t, []string{"101"}, saved10.FileIDs)
	assert.Equal(t, "2", saved10.Version)
	assert.True(t, saved10.FromCollection, "still the collection's")
	assert.Equal(t, &domain.CollectionPin{Version: "1", FileIDs: []string{"100"}}, saved10.SubstitutedFor)
	assert.Nil(t, saved.FindRef("nexusmods", "11").SubstitutedFor)
	row, err := f.svc.GetInstalledMod(context.Background(), "nexusmods", "10", "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, "2", row.Version)
}

func TestPlanSourceCollectionImport_SkipLeavesTheModOutAndRecordsIt(t *testing.T) {
	f := newGonePinFixture(t)
	plan := f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{Skip: []string{"nexusmods:10"}, UseCurrentAll: true}})

	c := plan.Collection.Source
	require.Len(t, c.GonePins, 1)
	assert.Equal(t, core.GonePinSkip, c.GonePins[0].Choice, "a named skip wins over use-current-all")
	assert.Equal(t, core.CollectionItemSkipped, itemNamed(c, "Ten").State)
	assert.Equal(t, 1, c.Counts[core.CollectionItemSkipped])
	assert.Nil(t, plan.Profile.FindRef("nexusmods", "10"))

	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Nil(t, saved.FindRef("nexusmods", "10"))
	require.NotNil(t, saved.CollectionBaseline)
	assert.Equal(t, []domain.CollectionSkip{{SourceID: "nexusmods", ModID: "10", Pin: domain.CollectionPin{Version: "1", FileIDs: []string{"100"}}}},
		saved.CollectionBaseline.Skipped)
}

func TestPlanSourceCollectionImport_UseCurrentAll(t *testing.T) {
	f := newGonePinFixture(t)
	plan := f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{UseCurrentAll: true}})
	assert.Equal(t, core.GonePinUseCurrent, plan.Collection.Source.GonePins[0].Choice)
	assert.True(t, plan.Collection.Source.UseCurrentAll)
}

func TestPlanSourceCollectionImport_AChoiceBothWaysIsRefused(t *testing.T) {
	f := newGonePinFixture(t)
	_, err := f.svc.PlanSourceCollectionImport(context.Background(), f.game, core.SourceCollectionImportRequest{
		SourceID: "nexusmods", Ref: "abcdef",
		GonePins: core.GonePinChoices{UseCurrent: []string{"nexusmods:10"}, Skip: []string{"nexusmods:10"}},
	})
	assert.True(t, core.IsBadGonePinChoice(err), "got %v", err)
}

func TestPlanSourceCollectionImport_AModGoneEntirelyCanOnlyBeSkipped(t *testing.T) {
	f := newGonePinFixture(t)
	f.src.listErr = map[string]error{"10": domain.ErrModNotFound}
	plan := f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{UseCurrentAll: true}})
	require.Len(t, plan.Collection.Source.GonePins, 1)
	pin := plan.Collection.Source.GonePins[0]
	assert.Nil(t, pin.Current)
	assert.Empty(t, pin.Choice, "there is no current file to use")
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	assert.True(t, core.IsCollectionGonePins(err))
	assert.Contains(t, err.Error(), "no current file")

	plan = f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{Skip: []string{"nexusmods:10"}}})
	_, err = f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
}

func TestPlanSourceCollectionImport_AListingThatFailsIsAWarning(t *testing.T) {
	f := newGonePinFixture(t)
	f.src.listErr = map[string]error{"10": errors.New("rate limited")}
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	assert.Empty(t, plan.Collection.Source.GonePins)
	assert.Contains(t, plan.Collection.Source.Notes[len(plan.Collection.Source.Notes)-1], "could not check")
}

func TestPlanSourceCollectionImport_WhatNeedsNoDownloadIsNotChecked(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)

	f.src.recipe.collection.Items[0].ModID = ""
	f.src.listing = map[string][]domain.DownloadableFile{"10": tenListing()}
	f.src.listCalls = nil
	plan = f.plan(t, core.SourceCollectionImportRequest{})
	assert.Empty(t, plan.Collection.Source.GonePins, "the profile has the mod at its pin already")
	assert.Zero(t, f.src.listCalls["10"])
}

// TestCollectionGonePin_ASubstituteAsksAboutTheCuratorsChoices: the
// curator's installer choices were made against the pinned archive, so the
// current file's installer asks rather than replay them (#609's pin).
func TestCollectionGonePin_ASubstituteAsksAboutTheCuratorsChoices(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	sum := md5.Sum(f.installer)
	f.src.md5["11"] = hex.EncodeToString(sum[:])
	f.src.recipe.collection.Items[1].ModID = ""
	f.src.listing = map[string][]domain.DownloadableFile{"11": {
		{ID: "111", FileName: "f111.zip", Name: "Installer 2", Version: "2", Category: "MAIN", IsPrimary: true},
	}}
	cfg := flowModuleConfig("4K")
	f.src.AddDownload("111", createTestZipBytes(t, map[string]string{
		"fomod/ModuleConfig.xml": cfg, "core/plugin.esp": "PLUGIN v2", "tex/2k.dds": "2K", "tex/4k.dds": "4K",
	}))
	plan, err := f.svc.PlanSourceCollectionImport(context.Background(), f.game, core.SourceCollectionImportRequest{
		SourceID: "nexusmods", Ref: "phased", GonePins: core.GonePinChoices{UseCurrent: []string{"nexusmods:11"}},
	})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	assert.Equal(t, core.FomodReasonArchiveMismatch, q.Mods[0].Reason)
}

// TestCollectionGonePin_ALaterInstallOfASubstituteAsks: after a
// --no-install import, the recipe's md5 for the pinned file is what a
// later install checks the current archive against.
func TestCollectionGonePin_ALaterInstallOfASubstituteAsks(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	sum := md5.Sum(f.installer)
	f.src.md5["11"] = hex.EncodeToString(sum[:])
	f.src.recipe.collection.Items[1].ModID = ""
	f.src.listing = map[string][]domain.DownloadableFile{"11": {
		{ID: "111", FileName: "f111.zip", Name: "Installer 2", Version: "2", Category: "MAIN", IsPrimary: true},
	}}
	f.src.filesByMod["11"] = []string{"111"}
	f.src.AddDownload("111", createTestZipBytes(t, map[string]string{
		"fomod/ModuleConfig.xml": flowModuleConfig("4K"), "core/plugin.esp": "PLUGIN v2", "tex/2k.dds": "2K", "tex/4k.dds": "4K",
	}))
	ctx := context.Background()
	plan, err := f.svc.PlanSourceCollectionImport(ctx, f.game, core.SourceCollectionImportRequest{
		SourceID: "nexusmods", Ref: "phased", GonePins: core.GonePinChoices{UseCurrentAll: true},
	})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, f.installerRef(t).SubstitutedFor)

	err = f.installInstaller(t, nil, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	assert.Equal(t, core.FomodReasonArchiveMismatch, q.Mods[0].Reason)
}

// TestPlanSourceCollectionImport_AnArchivedPinIsKept (owner ruling,
// 2026-11-15): NexusMods still serves an archived file, and the curator's
// exact file is the point of a collection - so it is no gone pin. The plan
// notes it is archived, and installs it.
func TestPlanSourceCollectionImport_AnArchivedPinIsKept(t *testing.T) {
	t.Run("the list says archived", func(t *testing.T) {
		f := newSourceImportFixture(t, source.TierPremium)
		f.src.recipe.collection.Items[0].FileCategory = "ARCHIVED"
		plan := f.plan(t, core.SourceCollectionImportRequest{})
		c := plan.Collection.Source
		assert.Empty(t, c.GonePins)
		assert.Zero(t, f.src.listCalls["10"], "the list vouches for an archived file: no listing")
		assert.Contains(t, itemNamed(c, "Ten").Note, "archived on NexusMods; installing the curator's file")
		assert.Equal(t, []string{"100"}, plan.Profile.FindRef("nexusmods", "10").FileIDs)
		result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
		require.NoError(t, err)
		assert.Equal(t, 2, result.Installed, "the curator's archived file installs")
	})
	t.Run("the listing says archived", func(t *testing.T) {
		f := newSourceImportFixture(t, source.TierPremium)
		f.src.recipe.collection.Items[0].ModID = ""
		f.src.listing = map[string][]domain.DownloadableFile{"10": append([]domain.DownloadableFile{
			{ID: "100", FileName: "f100.zip", Name: "Ten 1", Version: "1", Category: "ARCHIVED"}}, tenListing()...)}
		plan := f.plan(t, core.SourceCollectionImportRequest{})
		c := plan.Collection.Source
		assert.Empty(t, c.GonePins)
		assert.Contains(t, itemNamed(c, "Ten").Note, "archived on NexusMods")
		assert.Equal(t, []string{"100"}, plan.Profile.FindRef("nexusmods", "10").FileIDs)
	})
}

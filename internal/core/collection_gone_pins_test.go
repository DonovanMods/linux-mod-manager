package core_test

// #657: a source collection pinning a file its source no longer offers -
// found at plan time, at most one listing call per mod, and decided per
// mod: the mod's current main file instead (the default, #660), or skip it;
// the decision is recorded on the profile.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"strings"
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
	assert.Equal(t, core.GonePinUseCurrent, pin.Choice, "#660: the current file is the default")
	assert.True(t, pin.Default)
	assert.Contains(t, itemNamed(c, "Ten").Note, "no longer offers")
	// #654's summary counts it and lists it under Needs your attention.
	require.NotNil(t, c.Summary)
	assert.Equal(t, map[string]int{"10": 1}, f.src.listCalls,
		"only the mod the list does not vouch for is listed, once; the optional item is not planned")

	// #660: decided by default, so the import goes ahead.
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
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
	assert.Contains(t, itemNamed(c, "Ten").Note, "install its current main file Ten 2 (version 2) instead")
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
	assert.Equal(t, 1, c.Summary.Skipped)
	assert.Zero(t, c.Summary.GonePins, "decided")
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
	// #660: not a refusal - the item is left a gone_pin to-do.
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	require.Len(t, result.Todos, 1)
	assert.Equal(t, domain.TodoGonePin, result.Todos[0].Reason)

	plan = f.plan(t, core.SourceCollectionImportRequest{ProfileName: "skipping", GonePins: core.GonePinChoices{Skip: []string{"nexusmods:10"}}})
	result, err = f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Todos, "skipped, it is no to-do")
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
	// #660: the question is a to-do, not a refusal.
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	require.Len(t, result.Todos, 1)
	assert.Equal(t, domain.TodoInstallerQuestions, result.Todos[0].Reason)
	assert.Equal(t, core.FomodReasonArchiveMismatch, result.Todos[0].FomodReason)
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

// twoFileTenRecipe pins mod 10 at two files: its main file 100 and the
// optional 105 ("Ten Optional Patch"), beside mod 11.
const twoFileTenRecipe = `{"name": "Three Mods", "mods": [
	{"name": "Ten", "modId": "10", "fileId": "100", "logicalFilename": "Ten Main"},
	{"name": "Ten", "modId": "10", "fileId": "105", "logicalFilename": "Ten Optional Patch"},
	{"name": "Eleven", "modId": "11", "fileId": "110"}]}`

// newTwoFileFixture serves twoFileTenRecipe: the list has 100 as an old
// version of the main file and no record of 105 (removed); listing is mod
// 10's listing.
func newTwoFileFixture(t *testing.T, listing []domain.DownloadableFile) *sourceImportFixture {
	t.Helper()
	return newTwoFileFixtureNamed(t, "Ten Optional Patch", listing)
}

// newTwoFileFixtureNamed is newTwoFileFixture with the gone optional 105
// named optionalName.
func newTwoFileFixtureNamed(t *testing.T, optionalName string, listing []domain.DownloadableFile) *sourceImportFixture {
	t.Helper()
	f := newSourceImportFixtureWith(t, source.TierPremium, strings.Replace(twoFileTenRecipe, `"Ten Optional Patch"`, `"`+optionalName+`"`, 1))
	f.src.recipe.collection.Items = []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Ten", FileCategory: "OLD_VERSION"},
		{Kind: source.CollectionItemModFile, FileID: "105"},
		{Kind: source.CollectionItemModFile, ModID: "11", FileID: "110", Name: "Eleven", FileCategory: "MAIN"},
	}
	f.src.listing = map[string][]domain.DownloadableFile{"10": listing}
	for _, id := range []string{"101", "106"} {
		f.src.AddDownload(id, createTestZipBytes(t, map[string]string{"mod10-" + id + ".esp": id}))
	}
	return f
}

// tenOldMain is mod 10's listing with 100 kept as an old version, beside
// the current main 101.
func tenOldMain(extra ...domain.DownloadableFile) []domain.DownloadableFile {
	return append([]domain.DownloadableFile{
		{ID: "100", FileName: "f100.zip", Name: "Ten Main", Version: "1", Category: "OLD_VERSION"},
		{ID: "101", FileName: "f101.zip", Name: "Ten Main", Version: "2", Category: "MAIN", IsPrimary: true},
	}, extra...)
}

// TestGonePin_AGoneOptionalNeverGetsTheCurrentMain is the review's
// scenario (owner ruling, 2026-11-15): the entry pins 100 (its old main
// file, still offered as an old version) and 105 (an optional, removed).
// With no current optional matching 105, "use the current file" is not on
// offer - the current main is never put beside the pinned main - and the
// choices are leaving 105 out (the rest installs) or skipping the mod.
func TestGonePin_AGoneOptionalNeverGetsTheCurrentMain(t *testing.T) {
	f := newTwoFileFixture(t, tenOldMain(domain.DownloadableFile{ID: "107", FileName: "f107.zip", Name: "Something Else", Version: "2", Category: "OPTIONAL"}))
	plan := f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{UseCurrentAll: true}})
	pins := plan.Collection.Source.GonePins
	require.Len(t, pins, 1)
	pin := pins[0]
	assert.Equal(t, []string{"105"}, pin.FileIDs)
	require.Len(t, pin.Files, 1)
	assert.Equal(t, core.GoneFileSecondary, pin.Files[0].Role)
	assert.Nil(t, pin.Files[0].Replacement, "no current optional matches it")
	assert.Nil(t, pin.Current)
	assert.True(t, pin.CanSkipFile)
	assert.Empty(t, pin.Choice, "--use-current-all has nothing to install for it")

	_, err := f.svc.PlanSourceCollectionImport(context.Background(), f.game, core.SourceCollectionImportRequest{
		SourceID: "nexusmods", Ref: "abcdef", GonePins: core.GonePinChoices{UseCurrent: []string{"nexusmods:10"}}})
	assert.True(t, core.IsBadGonePinChoice(err), "naming it to use a current file is refused: %v", err)

	plan = f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{SkipFile: []string{"nexusmods:10"}}})
	assert.Equal(t, core.GonePinSkipFile, plan.Collection.Source.GonePins[0].Choice)
	ref := plan.Profile.FindRef("nexusmods", "10")
	require.NotNil(t, ref)
	assert.Equal(t, []string{"100"}, ref.FileIDs, "the gone optional is left out; no current main beside the pinned one")
	assert.Equal(t, "1", ref.Version, "the version follows the main file")
	assert.Equal(t, &domain.CollectionPin{Version: "1", FileIDs: []string{"100", "105"}}, ref.SubstitutedFor)
	_, err = f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
}

// TestGonePin_AGoneOptionalTakesTheMatchingCurrentOptional: a current
// optional whose name matches the gone one is its stand-in, and the
// version stays the main file's.
func TestGonePin_AGoneOptionalTakesTheMatchingCurrentOptional(t *testing.T) {
	f := newTwoFileFixture(t, tenOldMain(
		domain.DownloadableFile{ID: "106", FileName: "f106.zip", Name: "Ten Optional Patch v2", Version: "2", Category: "OPTIONAL"},
		domain.DownloadableFile{ID: "108", FileName: "f108.zip", Name: "Ten Other Patch", Version: "2", Category: "OPTIONAL"},
	))
	plan := f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{UseCurrentAll: true}})
	pin := plan.Collection.Source.GonePins[0]
	require.NotNil(t, pin.Files[0].Replacement)
	assert.Equal(t, "106", pin.Files[0].Replacement.FileID)
	assert.Equal(t, core.GonePinUseCurrent, pin.Choice)
	ref := plan.Profile.FindRef("nexusmods", "10")
	assert.Equal(t, []string{"100", "106"}, ref.FileIDs)
	assert.Equal(t, "1", ref.Version, "the version follows the main file, not the stand-in")
	assert.NotContains(t, ref.FileIDs, "101", "never a current main beside the pinned main")
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
}

// TestGonePin_TwoMatchingOptionalsAreNoMatch: a name two current files
// match identifies neither.
func TestGonePin_TwoMatchingOptionalsAreNoMatch(t *testing.T) {
	f := newTwoFileFixture(t, tenOldMain(
		domain.DownloadableFile{ID: "106", Name: "Ten Optional Patch v2", Version: "2", Category: "OPTIONAL"},
		domain.DownloadableFile{ID: "109", Name: "Ten Optional Patch (SE)", Version: "2", Category: "OPTIONAL"},
	))
	f.src.listing["10"][3].Name = "Ten Optional Patch v3"
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	assert.Nil(t, plan.Collection.Source.GonePins[0].Current)
}

// TestGonePin_EveryFileGoneTakesTheCurrentMain: with the main file gone
// too, the current main file stands in for the mod, its version is the
// entry's, and the optional it does not replace is said.
func TestGonePin_EveryFileGoneTakesTheCurrentMain(t *testing.T) {
	f := newTwoFileFixture(t, tenOldMain()[1:])
	f.src.recipe.collection.Items[0] = source.CollectionItem{Kind: source.CollectionItemModFile, FileID: "100"}
	plan := f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{UseCurrent: []string{"nexusmods:10"}}})
	pin := plan.Collection.Source.GonePins[0]
	require.Len(t, pin.Files, 2)
	assert.Equal(t, core.GoneFileMain, pin.Files[0].Role)
	assert.Equal(t, "101", pin.Current.FileID)
	assert.Equal(t, []string{"105"}, pin.NotReplaced)
	assert.Contains(t, itemNamed(plan.Collection.Source, "Ten").Note, "not replaced: Ten Optional Patch")
	ref := plan.Profile.FindRef("nexusmods", "10")
	assert.Equal(t, []string{"101"}, ref.FileIDs)
	assert.Equal(t, "2", ref.Version)
}

// TestGonePin_AChoiceNamingNoGonePinIsRefused (review nit): as an unknown
// --optional id is refused (#654), so is a --skip or --use-current naming
// a mod whose pin is not gone.
func TestGonePin_AChoiceNamingNoGonePinIsRefused(t *testing.T) {
	f := newGonePinFixture(t)
	_, err := f.svc.PlanSourceCollectionImport(context.Background(), f.game, core.SourceCollectionImportRequest{
		SourceID: "nexusmods", Ref: "abcdef", GonePins: core.GonePinChoices{Skip: []string{"nexusmods:11"}}})
	require.True(t, core.IsBadGonePinChoice(err), "got %v", err)
	assert.Contains(t, err.Error(), "nexusmods:11 names no mod whose pinned file is gone; the plan's are nexusmods:10")
}

// TestGonePin_AResolutionIsPartOfTheName (#657 re-review): 4K, 2K, 1080p,
// 512 and 2048 name what a file is, not its version, so a gone "Textures
// 4K" is not stood in for by the 2K file that is still offered - and
// --use-current-all cannot take it silently - while a version, after a "v"
// or dotted, still differs freely.
func TestGonePin_AResolutionIsPartOfTheName(t *testing.T) {
	f := newTwoFileFixtureNamed(t, "Textures 4K", tenOldMain(
		domain.DownloadableFile{ID: "106", FileName: "f106.zip", Name: "Textures 2K", Version: "2", Category: "OPTIONAL"}))
	plan := f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{UseCurrentAll: true}})
	pin := plan.Collection.Source.GonePins[0]
	assert.Nil(t, pin.Files[0].Replacement, "2K is not 4K")
	assert.Nil(t, pin.Current)
	assert.True(t, pin.CanSkipFile, "skip_file or skip only")
	assert.Empty(t, pin.Choice, "--use-current-all takes nothing")

	f = newTwoFileFixtureNamed(t, "Textures 4K v1.0", tenOldMain(
		domain.DownloadableFile{ID: "106", FileName: "f106.zip", Name: "Textures 2K v1.2", Version: "2", Category: "OPTIONAL"},
		domain.DownloadableFile{ID: "108", FileName: "f108.zip", Name: "Textures 4K v1.2", Version: "2", Category: "OPTIONAL"}))
	plan = f.plan(t, core.SourceCollectionImportRequest{GonePins: core.GonePinChoices{UseCurrentAll: true}})
	pin = plan.Collection.Source.GonePins[0]
	require.NotNil(t, pin.Files[0].Replacement)
	assert.Equal(t, "108", pin.Files[0].Replacement.FileID, "the same resolution at another version")
	assert.Equal(t, core.GonePinUseCurrent, pin.Choice)
}

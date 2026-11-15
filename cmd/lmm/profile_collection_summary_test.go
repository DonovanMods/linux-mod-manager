package main

// #654: a collection plan's grouped text - the summary first, then the
// optional mods with the ids --optional takes, then the items that need
// the user, then every item.

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateCollectionPlanGoldens = flag.Bool("update-collection-plan", false, "re-record cmd/lmm/testdata/collection_plan_golden/*.txt")

func assertCollectionPlanGolden(t *testing.T, name, actual string) {
	t.Helper()
	path := filepath.Join("testdata", "collection_plan_golden", name+".txt")
	if *updateCollectionPlanGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(actual), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden %s missing - record it with -update-collection-plan", path)
	assert.Equal(t, string(want), actual)
}

// groupedCollectionFixture has one item of every kind the plan groups.
func groupedCollectionFixture() *core.ConvertedCollection {
	return &core.ConvertedCollection{
		SourceID: "nexusmods", Slug: "abcdef", Revision: 3, Name: "Big",
		Items: []core.ConvertedCollectionItem{
			{ID: "1:11", Name: "Plain", Version: "1.0", ModID: "1", FileID: "11", State: core.CollectionItemWillInstall},
			{ID: "2:22", Name: "Asks", Version: "1.0", ModID: "2", FileID: "22", State: core.CollectionItemWillInstall, Installer: true,
				Note: "the collection records an installer for this item but no answers, so its installer will ask at install"},
			{ID: "3:33", Name: "Old", Version: "0.9", ModID: "3", FileID: "33", FileCategory: "OLD_VERSION", State: core.CollectionItemWillInstall},
			{ID: "4:44", Name: "Opt", Version: "2.0", ModID: "4", FileID: "44", State: core.CollectionItemOptionalSkipped, Optional: true,
				URL: "https://www.nexusmods.com/sdv/mods/4?tab=files&file_id=44", Instructions: "Recommended for 4K screens."},
			{ID: "5:55", Name: "Opt In", Version: "2.1", ModID: "5", FileID: "55", State: core.CollectionItemWillInstall, Optional: true},
			{ID: "6:66", Name: "Opt Refused", ModID: "6", FileID: "66", State: core.CollectionItemUnsupported, Optional: true, Features: []string{"file_overrides"}},
			{ID: "2400:240", Name: "SMAPI", Version: "4.0", ModID: "2400", FileID: "240", State: core.CollectionItemExternal,
				URL: "https://www.nexusmods.com/stardewvalley/mods/2400", Note: "SMAPI is installed with its own installer, not by lmm"},
			{ID: "item:7", Name: "Ext", State: core.CollectionItemExternal, URL: "https://ext.example", Instructions: "Unzip it."},
		},
		Counts: map[core.CollectionItemState]int{
			core.CollectionItemWillInstall: 4, core.CollectionItemOptionalSkipped: 1,
			core.CollectionItemUnsupported: 1, core.CollectionItemExternal: 2,
		},
		Optional: []string{"5:55"},
		Summary: &core.CollectionPlanSummary{
			Items: 8, Install: 4, Optional: 3, OptionalSelected: 1, InstallerQuestions: 1, External: 2, Unsupported: 1,
			Attention: []core.CollectionAttentionItem{
				{ID: "2:22", Reasons: []core.CollectionAttentionReason{core.CollectionAttentionInstallerQuestions}},
				{ID: "3:33", Reasons: []core.CollectionAttentionReason{core.CollectionAttentionOldFile}},
				{ID: "6:66", Reasons: []core.CollectionAttentionReason{core.CollectionAttentionUnsupported}},
				{ID: "2400:240", Reasons: []core.CollectionAttentionReason{core.CollectionAttentionPrerequisite}},
				{ID: "item:7", Reasons: []core.CollectionAttentionReason{core.CollectionAttentionExternal, "future_reason"}},
			},
		},
	}
}

func TestPrintSourceCollection_GroupedGolden(t *testing.T) {
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		printSourceCollection(groupedCollectionFixture(), "lmm profile import --nexus-collection")
		return nil
	})
	require.NoError(t, err)
	assertCollectionPlanGolden(t, "grouped", stdout)
}

// sectionsInOrder asserts each heading appears in text, after the one
// before it.
func sectionsInOrder(t *testing.T, text string, headings ...string) {
	t.Helper()
	at := 0
	for _, h := range headings {
		i := strings.Index(text[at:], h)
		require.GreaterOrEqual(t, i, 0, "%q missing after offset %d:\n%s", h, at, text)
		at += i + len(h)
	}
}

func TestPrintSourceCollection_SectionOrder(t *testing.T) {
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		printSourceCollection(groupedCollectionFixture(), "lmm profile import --nexus-collection")
		return nil
	})
	require.NoError(t, err)
	sectionsInOrder(t, stdout,
		"Summary: 4 will be installed, 3 optional (1 selected), 1 will ask installer questions, 2 external or prerequisite, 1 cannot be imported",
		"Optional mods (3)", "[ ] Opt 2.0  id 4:44", "Recommended for 4K screens.", "[x] Opt In 2.1  id 5:55", "[-] Opt Refused  id 6:66 (cannot be imported)",
		"Needs your attention (5):", "Asks: its installer will ask you", "Old: the curator pinned an older file",
		"Opt Refused: lmm cannot import it (needs file_overrides), so this optional mod is left out",
		"SMAPI: a prerequisite", "Ext: lmm does not fetch it: get it by hand; future reason",
		"Every item (8), in the collection's order:", "Plain 1.0")
}

// A plan built without a summary (an older core's document) prints the
// item list as before.
func TestPrintSourceCollection_NoSummaryKeepsTheList(t *testing.T) {
	c := groupedCollectionFixture()
	c.Summary = nil
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		printSourceCollection(c, "lmm profile import --nexus-collection")
		return nil
	})
	require.NoError(t, err)
	assert.NotContains(t, stdout, "Summary:")
	assert.NotContains(t, stdout, "Needs your attention")
	assert.Contains(t, stdout, "Plain 1.0")
}

func TestDoNexusCollectionImport_GroupsThePlan(t *testing.T) {
	svc, game, _ := nexusCollectionService(t)
	setNexusFlags(t, "abcdef", false)
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.NoError(t, err, stdout)
	sectionsInOrder(t, stdout,
		"Summary: 0 will be installed, 1 optional (0 selected), 1 to download by hand",
		"Optional mods (1)", "[ ] Twelve 2.0  id 12:120",
		"Needs your attention (1):", "1 item(s) to download by hand",
		"Every item (2), in the collection's order:", "⇣ Ten 1.0")
}

func setNexusOptional(t *testing.T, ids ...string) {
	t.Helper()
	old := profileImportOptional
	profileImportOptional = ids
	t.Cleanup(func() { profileImportOptional = old })
}

// #654: --optional opts one optional item in by the id the plan prints, as
// the web UI's per-item toggle does.
func TestDoNexusCollectionImport_OptionalByID(t *testing.T) {
	svc, game, _ := nexusCollectionService(t)
	setNexusFlags(t, "abcdef", false)
	setNexusOptional(t, "12:120")
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "1 optional (1 selected), 2 to download by hand")
	assert.Contains(t, stdout, "[x] Twelve 2.0  id 12:120")
	// Twelve's file is an old version: it needs the user now it is in.
	assert.Contains(t, stdout, "Twelve: the curator pinned an older file")
	saved, err := svc.NewProfileManager().Get(context.Background(), game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Len(t, saved.Mods, 2)
}

func TestRunProfileImport_OptionalNeedsANexusCollection(t *testing.T) {
	setNexusFlags(t, "", false)
	setNexusOptional(t, "12:120")
	err := runProfileImport(profileImportCmd, []string{"some.yaml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--optional opts one of a NexusMods collection's optional mods in; use it with --nexus-collection")
}

// #654 review: an --optional id that names no optional item of the
// collection is refused, naming it and the ids the plan lists, rather than
// importing nothing extra in silence.
func TestDoNexusCollectionImport_UnknownOptionalIDIsRefused(t *testing.T) {
	for _, id := range []string{"12:12", "10:100"} {
		t.Run(id, func(t *testing.T) {
			svc, game, _ := nexusCollectionService(t)
			setNexusFlags(t, "abcdef", false)
			setNexusOptional(t, id)
			stdout, _, err := captureStdoutStderrErr(t, func() error {
				return doNexusCollectionImport(context.Background(), svc, game)
			})
			require.ErrorIs(t, err, core.ErrUnknownOptionalItem, stdout)
			assert.Contains(t, err.Error(), id)
			assert.Contains(t, err.Error(), "its plan lists the optional mods with their ids: 12:120")
			_, getErr := svc.NewProfileManager().Get(context.Background(), game.ID, "Three Mods")
			assert.Error(t, getErr, "nothing is imported")
		})
	}
}

// The same check holds `lmm profile sync --collection --optional`.
func TestDoCollectionUpdate_UnknownOptionalIDIsRefused(t *testing.T) {
	svc, game, _ := nexusCollectionService(t)
	setNexusFlags(t, "abcdef", false)
	_, _, err := captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.NoError(t, err)
	setCollectionSyncFlags(t, func() { profileSyncOptional = []string{"99:1"} })
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doCollectionUpdate(context.Background(), svc, game, []string{"Three Mods"})
	})
	require.ErrorIs(t, err, core.ErrUnknownOptionalItem, stdout)
	assert.Contains(t, err.Error(), "99:1")
	assert.Contains(t, err.Error(), "12:120")
}

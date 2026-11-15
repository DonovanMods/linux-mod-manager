package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #654: a converted collection's plan summary - the counts both frontends
// show first, and the items that need the user, each with its reasons.

// summaryManifest is a collection with one item of every kind the summary
// counts: a plain mod, an installer with no recorded answers, an old file,
// a hand-fetched item, a prerequisite step, two optional mods and a
// required item lmm cannot import.
func summaryManifest() *source.CollectionManifest {
	asks := nexusMod("Asks", "2", "22", "bb")
	asks.InstallerType = "fomod"
	old := nexusMod("Old", "3", "33", "cc")
	opt := nexusMod("Opt", "4", "44", "dd")
	opt.Optional = true
	opt.Instructions = "Recommended for 4K screens."
	optAsks := nexusMod("Opt Asks", "5", "55", "ee")
	optAsks.Optional = true
	optAsks.InstallerType = "fomod"
	smapi := nexusMod("SMAPI", "2400", "240", "ff")
	smapi.ModType = "SMAPI"
	refused := nexusMod("Refused", "6", "66", "gg")
	refused.FileOverrides = []string{"a.txt"}
	ext := source.ManifestMod{Name: "Ext", SourceType: "browse", URL: "https://ext.example", UpdatePolicy: "exact"}
	return &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{
		nexusMod("Plain", "1", "11", "aa"), asks, old, opt, optAsks, smapi, refused, ext,
	}}
}

func summaryInput(m *source.CollectionManifest) convertInput {
	in := convertInputFor(m)
	in.game.Sources["nexusmods"] = "stardewvalley"
	in.list.Items = []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "3", FileID: "33", FileCategory: "OLD_VERSION"},
	}
	return in
}

func TestCollectionPlanSummary_CountsAndAttention(t *testing.T) {
	c := mustConvert(t, summaryInput(summaryManifest()))
	require.NotNil(t, c.Summary)
	assert.Equal(t, CollectionPlanSummary{
		Items: 8, Install: 3, Optional: 2, OptionalSelected: 0,
		InstallerQuestions: 1, External: 2, Unsupported: 1,
		Attention: []CollectionAttentionItem{
			{ID: "2:22", Reasons: []CollectionAttentionReason{CollectionAttentionInstallerQuestions}},
			{ID: "3:33", Reasons: []CollectionAttentionReason{CollectionAttentionOldFile}},
			{ID: "2400:240", Reasons: []CollectionAttentionReason{CollectionAttentionPrerequisite}},
			{ID: "6:66", Reasons: []CollectionAttentionReason{CollectionAttentionUnsupported}},
			{ID: "item:7", Reasons: []CollectionAttentionReason{CollectionAttentionExternal}},
		},
	}, *c.Summary)
}

func TestCollectionPlanSummary_OptingInCountsTheSelection(t *testing.T) {
	in := summaryInput(summaryManifest())
	in.optional = map[string]bool{"5:55": true}
	c := mustConvert(t, in)
	require.NotNil(t, c.Summary)
	assert.Equal(t, 1, c.Summary.OptionalSelected)
	assert.Equal(t, 4, c.Summary.Install, "the opted-in mod is installed")
	assert.Equal(t, 2, c.Summary.InstallerQuestions, "its installer asks too")
	assert.Contains(t, c.Summary.Attention,
		CollectionAttentionItem{ID: "5:55", Reasons: []CollectionAttentionReason{CollectionAttentionInstallerQuestions}})

	in = summaryInput(summaryManifest())
	in.withOptional = true
	c = mustConvert(t, in)
	assert.Equal(t, 2, c.Summary.OptionalSelected)
}

// A free account's items are every one a manual download: counted, but not
// listed one by one as needing attention - the account is the one reason,
// and the plan says it once.
func TestCollectionPlanSummary_ManualDownloadsAreCountedNotListed(t *testing.T) {
	c := mustConvert(t, summaryInput(summaryManifest()))
	for i := range c.Items {
		if c.Items[i].State == CollectionItemWillInstall {
			c.Items[i].State = CollectionItemManualDownload
		}
	}
	c.ManualDownload = true
	c.recount()
	assert.Equal(t, 0, c.Summary.Install)
	assert.Equal(t, 3, c.Summary.ManualDownload)
	assert.Equal(t, 1, c.Summary.InstallerQuestions, "an item downloaded by hand still asks its installer's questions")
	for _, a := range c.Summary.Attention {
		assert.NotContains(t, a.Reasons, CollectionAttentionReason("manual_download"))
	}
}

// An item already installed needs nothing: its installer ran, and its file
// is there.
func TestCollectionPlanSummary_InstalledItemsNeedNoAttention(t *testing.T) {
	c := mustConvert(t, summaryInput(summaryManifest()))
	for i := range c.Items {
		if c.Items[i].ID == "2:22" || c.Items[i].ID == "3:33" {
			c.Items[i].State = CollectionItemInstalled
		}
	}
	c.recount()
	assert.Equal(t, 2, c.Summary.Installed)
	assert.Equal(t, 1, c.Summary.Install)
	assert.Equal(t, 0, c.Summary.InstallerQuestions)
	for _, a := range c.Summary.Attention {
		assert.NotContains(t, []string{"2:22", "3:33"}, a.ID)
	}
}

package config_test

// #657: a collection entry whose pinned file was gone and was installed at
// the mod's current file records the pin it stands in for
// (`substituted_for:`), and the collection's mods the user skipped are kept
// in `collection_baseline: skipped:`. Both are this machine's records:
// saved in place, never exported.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func TestSaveProfile_SubstitutedForIsEditedInPlaceAndNeverExported(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	ref := profile.FindRef("local", "beta")
	ref.SubstitutedFor = &domain.CollectionPin{Version: "4.3.8a", FileIDs: []string{"733846"}}
	require.NoError(t, config.SaveProfile(dir, profile))
	block := "    substituted_for:\n      version: 4.3.8a\n      file_ids:\n          - \"733846\"\n"
	text := readFile(t, path)
	require.Contains(t, text, block)
	require.Equal(t, handEdited, strings.Replace(text, block, "", 1), "only the new block is added")

	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Equal(t, profile.Mods, reloaded.Mods)

	exported, err := config.ExportProfile(reloaded)
	require.NoError(t, err)
	require.NotContains(t, string(exported), "substituted_for", "an export never carries the record")
	doc := &domain.CollectionDocument{FormatVersion: 1, Name: "c",
		Items: []domain.CollectionItem{{Ref: *reloaded.FindRef("local", "beta"), Name: "Beta"}}}
	data, err := config.MarshalCollection(doc)
	require.NoError(t, err)
	require.NotContains(t, string(data), "substituted_for", "a collection document never carries the record")

	reloaded.FindRef("local", "beta").SubstitutedFor = nil
	require.NoError(t, config.SaveProfile(dir, reloaded))
	require.Equal(t, handEdited, readFile(t, path), "clearing it restores the file")
}

func TestSaveProfile_SkippedCollectionModsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	profile.CollectionBaseline = &domain.CollectionBaseline{Skipped: []domain.CollectionSkip{
		{SourceID: "nexusmods", ModID: "266", Pin: domain.CollectionPin{Version: "4.3.8a", FileIDs: []string{"733846"}}},
	}}
	require.False(t, profile.CollectionBaseline.IsZero())
	require.NoError(t, config.SaveProfile(dir, profile))
	require.Contains(t, readFile(t, path), "collection_baseline:\n    skipped:\n")
	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Equal(t, profile.CollectionBaseline, reloaded.CollectionBaseline)

	exported, err := config.ExportProfile(reloaded)
	require.NoError(t, err)
	require.NotContains(t, string(exported), "skipped")
}

func TestCollectionPin_Same(t *testing.T) {
	p := &domain.CollectionPin{Version: "1", FileIDs: []string{"a", "b"}}
	require.True(t, p.Same("1", []string{"b", "a"}))
	require.False(t, p.Same("2", []string{"a", "b"}))
	require.False(t, p.Same("1", []string{"a"}))
	var none *domain.CollectionPin
	require.False(t, none.Same("", nil))
	require.Nil(t, none.Clone())
}

package config_test

// #613: a mod reference a collection brought in records it as
// `from_collection: true` in the profile file, so a collection update can
// tell the collection's entries (which follow its new pins) from the
// user's. Absent means the user's, so every file written before the key
// existed is unchanged, and an in-place save adds or removes only that
// key. It is this machine's record, never a document's: an export and a
// collection document leave it out.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func TestSaveProfile_FromCollectionIsEditedInPlace(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)
	want := handEdited

	step := func(name string, change func(p *domain.Profile), edit func(string) string) {
		t.Helper()
		profile, err := config.LoadProfile(dir, "tg", "default")
		require.NoError(t, err, name)
		change(profile)
		require.NoError(t, config.SaveProfile(dir, profile), name)
		want = edit(want)
		require.Equal(t, want, readFile(t, path), name)
		reloaded, err := config.LoadProfile(dir, "tg", "default")
		require.NoError(t, err, name)
		require.Equal(t, profile.Mods, reloaded.Mods, name)
	}
	byID := func(p *domain.Profile, id string) *domain.ModReference { return p.FindRef("local", id) }

	step("mark beta", func(p *domain.Profile) {
		byID(p, "beta").FromCollection = true
	}, func(s string) string {
		return strings.Replace(s, "    version: \"1.0\"\n\n", "    version: \"1.0\"\n    from_collection: true\n\n", 1)
	})
	step("mark alpha (flow)", func(p *domain.Profile) {
		byID(p, "alpha").FromCollection = true
	}, func(s string) string {
		return strings.Replace(s, `version: "1.0"}`, `version: "1.0", from_collection: true}`, 1)
	})
	step("clear beta", func(p *domain.Profile) {
		byID(p, "beta").FromCollection = false
	}, func(s string) string {
		return strings.Replace(s, "    from_collection: true\n", "", 1)
	})
	step("clear alpha", func(p *domain.Profile) {
		byID(p, "alpha").FromCollection = false
	}, func(s string) string {
		return strings.Replace(s, `, from_collection: true}`, `}`, 1)
	})
	require.Equal(t, handEdited, want, "clearing every marker restores the file")
}

func TestSaveProfile_FromCollectionIsNeverExported(t *testing.T) {
	dir := t.TempDir()
	profile := &domain.Profile{Name: "p", GameID: "tg", Mods: []domain.ModReference{
		{SourceID: "nexusmods", ModID: "1", Version: "1.0", FromCollection: true},
		{SourceID: "nexusmods", ModID: "2", Version: "1"},
	}}
	require.NoError(t, config.SaveProfile(dir, profile))
	reloaded, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.True(t, reloaded.Mods[0].FromCollection)
	require.False(t, reloaded.Mods[1].FromCollection)
	path, err := config.ProfilePath(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(readFile(t, path), "from_collection:"), "an unmarked entry writes no key")

	exported, err := config.ExportProfile(reloaded)
	require.NoError(t, err)
	require.NotContains(t, string(exported), "from_collection", "an export never carries the marker")
	imported, err := config.ImportProfile([]byte(string(exported) + ""))
	require.NoError(t, err)
	require.False(t, imported.Mods[0].FromCollection)

	doc := &domain.CollectionDocument{FormatVersion: 1, Name: "c",
		Items: []domain.CollectionItem{{Ref: reloaded.Mods[0], Name: "One"}}}
	data, err := config.MarshalCollection(doc)
	require.NoError(t, err)
	require.NotContains(t, string(data), "from_collection", "a collection document never carries the marker")
}

// #613: the collection's last applied plugin list and override hashes are
// kept in the profile file as `collection_baseline:`, saved in place like
// any other block, and never exported.
func TestSaveProfile_CollectionBaselineRoundTripsAndIsNeverExported(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Nil(t, profile.CollectionBaseline)
	profile.CollectionBaseline = &domain.CollectionBaseline{
		Plugins:   domain.ProfilePlugins{Order: []domain.PluginEntry{{Name: "A.esp", Enabled: true}, {Name: "B.esp"}}},
		Overrides: map[string]string{"Config/Game.ini": "ab12"},
	}
	require.NoError(t, config.SaveProfile(dir, profile))
	text := readFile(t, path)
	require.True(t, strings.HasPrefix(text, handEdited), "only the new block is added")
	require.Contains(t, text, "collection_baseline:")
	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Equal(t, profile.CollectionBaseline, reloaded.CollectionBaseline)

	exported, err := config.ExportProfile(reloaded)
	require.NoError(t, err)
	require.NotContains(t, string(exported), "collection_baseline")

	reloaded.CollectionBaseline = nil
	require.NoError(t, config.SaveProfile(dir, reloaded))
	require.Equal(t, handEdited, readFile(t, path), "clearing it restores the file")
}

// #613 re-review: an empty baseline is a record (`collection_baseline: {}`),
// not the same as none.
func TestSaveProfile_AnEmptyCollectionBaselineIsKept(t *testing.T) {
	dir := t.TempDir()
	profile := &domain.Profile{Name: "p", GameID: "tg", CollectionBaseline: &domain.CollectionBaseline{}}
	require.NoError(t, config.SaveProfile(dir, profile))
	path, err := config.ProfilePath(dir, "tg", "p")
	require.NoError(t, err)
	require.Contains(t, readFile(t, path), "collection_baseline: {}")
	reloaded, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.NotNil(t, reloaded.CollectionBaseline, "empty, not none")

	reloaded.CollectionBaseline = nil
	require.NoError(t, config.SaveProfile(dir, reloaded))
	require.NotContains(t, readFile(t, path), "collection_baseline")
}

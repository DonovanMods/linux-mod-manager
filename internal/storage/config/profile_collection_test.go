package config_test

// #621: a profile imported from a collection document records the
// collection's identity (`collection:`) and the items lmm cannot fetch
// (`external:`). Both are top-level, omitempty keys: a profile without them
// is unchanged, and an in-place save adds or removes only those keys.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func collectionInfoFixture() *domain.CollectionInfo {
	return &domain.CollectionInfo{
		ID:          "2f8c7a3e-0000-4000-8000-000000000001",
		Version:     3,
		Title:       "Welcome to Night City",
		Author:      "curator",
		Description: "A curated list.\nTwo lines.",
		Origin:      &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "iszwwe", Revision: 484, URL: "https://example.invalid/c/iszwwe", Author: "someone"},
	}
}

func TestSaveProfile_CollectionAndExternalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	profile := &domain.Profile{
		Name: "p", GameID: "tg",
		Mods:       []domain.ModReference{{SourceID: "local", ModID: "a", Version: "1"}},
		Collection: collectionInfoFixture(),
		External:   []domain.CollectionExternalItem{{Name: "Tool", URL: "https://example.invalid/tool", Instructions: "Install it by hand."}},
	}
	require.NoError(t, config.SaveProfile(dir, profile))
	reloaded, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, profile.Collection, reloaded.Collection)
	require.Equal(t, profile.External, reloaded.External)
}

func TestSaveProfile_CollectionIsEditedInPlace(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)

	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Nil(t, profile.Collection, "a file without the key has no collection")
	require.Empty(t, profile.External)

	profile.Collection = &domain.CollectionInfo{ID: "abc", Version: 2}
	profile.External = []domain.CollectionExternalItem{{Name: "Tool"}}
	require.NoError(t, config.SaveProfile(dir, profile))
	got := readFile(t, path)
	require.True(t, strings.HasPrefix(got, handEdited), "the author's text is kept, the keys appended:\n%s", got)
	require.Contains(t, got, "collection:\n")
	require.Contains(t, got, "external:\n")

	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Equal(t, profile.Collection, reloaded.Collection)
	require.Equal(t, profile.External, reloaded.External)

	reloaded.Collection = nil
	reloaded.External = nil
	require.NoError(t, config.SaveProfile(dir, reloaded))
	require.Equal(t, handEdited, readFile(t, path), "clearing both restores the file")
}

// TestImportProfile_V0IgnoresCollectionAndExternal is #621 review S4: a
// version-0 document never carried a collection: or external: block, so the
// version-0 path neither writes nor reads one - a stranger's document
// cannot plant a collection id or unchecked items. The JSON value (--json
// export, snapshots) still carries the profile's own.
func TestImportProfile_V0IgnoresCollectionAndExternal(t *testing.T) {
	profile := &domain.Profile{
		Name: "p", GameID: "tg",
		Mods:       []domain.ModReference{{SourceID: "local", ModID: "a", Version: "1"}},
		Collection: collectionInfoFixture(),
		External:   []domain.CollectionExternalItem{{Name: "Tool"}},
	}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)
	require.NotContains(t, string(data), "collection:")
	require.NotContains(t, string(data), "external:")

	planted := []byte("name: p\ngame_id: tg\nmods: []\ncollection:\n  id: someone-elses-id\n  version: 9\nexternal:\n  - name: \"\\e[2Jevil\"\n")
	back, err := config.ImportProfile(planted)
	require.NoError(t, err)
	require.Nil(t, back.Collection, "a version-0 document cannot plant a collection identity")
	require.Empty(t, back.External)

	value := config.ExportProfileValue(profile)
	require.Equal(t, profile.Collection, value.Collection)
	require.Equal(t, profile.External, value.External)
	restored := config.ProfileFromExported(value)
	require.Equal(t, profile.Collection, restored.Collection, "a snapshot (lmm's own record) restores them")
	require.Equal(t, profile.External, restored.External)
}

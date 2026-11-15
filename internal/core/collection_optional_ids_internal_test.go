package core

import (
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #654 review: an --optional id (or the web plan's optional list) that is
// not an optional item of the collection is refused, naming it and the ids
// that are, rather than silently opting nothing in.
func TestConvert_RefusesAnIDThatIsNotAnOptionalItem(t *testing.T) {
	for name, ids := range map[string][]string{
		"a typo":              {"4:4"},
		"a required item":     {"1:11"},
		"one good, one wrong": {"4:44", "99:1"},
	} {
		t.Run(name, func(t *testing.T) {
			in := summaryInput(summaryManifest())
			in.optional = map[string]bool{}
			for _, id := range ids {
				in.optional[id] = true
			}
			_, err := convertCollection(in)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrUnknownOptionalItem), "%v", err)
			var unknown *UnknownOptionalItemError
			require.ErrorAs(t, err, &unknown)
			assert.NotContains(t, unknown.IDs, "4:44", "a real optional item is not named")
			assert.Equal(t, []string{"4:44", "5:55"}, unknown.Optional, "the collection's optional ids are named")
			assert.Contains(t, err.Error(), "4:44, 5:55")
		})
	}
}

func TestConvert_OptionalIDsOfOptionalItemsPass(t *testing.T) {
	in := summaryInput(summaryManifest())
	in.optional = map[string]bool{"4:44": true, "5:55": true}
	c, err := convertCollection(in)
	require.NoError(t, err)
	assert.Equal(t, []string{"4:44", "5:55"}, c.Optional)
}

func TestConvert_AnOptionalIDOnACollectionWithNone(t *testing.T) {
	in := convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa")}})
	in.optional = map[string]bool{"1:11": true}
	_, err := convertCollection(in)
	require.ErrorIs(t, err, ErrUnknownOptionalItem)
	assert.Contains(t, err.Error(), "has no optional mods")
}

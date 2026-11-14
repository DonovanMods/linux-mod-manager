package core

// #620: a recipe's fbLoadOrders become the profile's load_orders: list, for
// a game whose adapter writes that order; anywhere else the collection is
// refused for file_load_order:<id>, as it was before.

import (
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func archiveLoadOrderManifest(files ...source.ManifestFileLoadOrderEntry) *source.CollectionManifest {
	m1 := nexusMod("Quest Fixes", "29152", "11", "aa")
	m2 := nexusMod("Shadows Fix", "33808", "22", "bb")
	return &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{m1, m2},
		FileLoadOrders: []source.ManifestFileLoadOrder{{ID: "cyberpunk2077-archive", Entries: len(files), Files: files}}}
}

func archiveEntry(name, hint string) source.ManifestFileLoadOrderEntry {
	return source.ManifestFileLoadOrderEntry{Name: name, Enabled: true, ModHint: hint}
}

func withArchiveOrder(in convertInput) convertInput {
	in.loadOrders = func(id string) bool { return id == "cyberpunk2077-archive" }
	return in
}

func TestConvert_FileLoadOrderBecomesTheProfileList(t *testing.T) {
	m := archiveLoadOrderManifest(
		archiveEntry("! quest_fixes.archive", "Cyberpunk Story Quest Fixes 29152 1.21.1 2026-10-31T14-47Z LACBAIvts"),
		archiveEntry("basilisk_shadows.archive", "Basilisk Shadows Fix 1.0.0-33808-1-0-0-1763824954"),
		archiveEntry("loose.archive", "Something Else 99999 1.0"),
		archiveEntry("! QUEST_FIXES.archive", "dup"),
	)
	c := mustConvert(t, withArchiveOrder(convertInputFor(m)))
	assert.Empty(t, c.Unsupported, "the game's adapter writes this order")
	assert.Contains(t, c.Features, "file_load_order:cyberpunk2077-archive")
	assert.Equal(t, []string{"! quest_fixes.archive", "basilisk_shadows.archive", "loose.archive"}, c.Document.LoadOrders["cyberpunk2077-archive"],
		"names kept as written, a repeat (ignoring case) keeps its first place")
	assert.Contains(t, c.Document.Requires, "file_load_order:cyberpunk2077-archive")
	require.Len(t, c.LoadOrders, 1)
	assert.Equal(t, ConvertedLoadOrder{ID: "cyberpunk2077-archive", Files: 3, Matched: 2}, c.LoadOrders[0])
	assert.Equal(t, map[string]string{"! quest_fixes.archive": "Quest Fixes", "basilisk_shadows.archive": "Shadows Fix"},
		c.loadOrderMods["cyberpunk2077-archive"], "an entry whose hint names one member mod is that mod's")
	assert.True(t, containsSubstring(c.Notes, "more than once"), "%v", c.Notes)
}

func TestConvert_FileLoadOrderUnsupportedForTheGame(t *testing.T) {
	m := archiveLoadOrderManifest(archiveEntry("a.archive", ""))
	c := mustConvert(t, convertInputFor(m))
	assert.Equal(t, []string{"file_load_order:cyberpunk2077-archive"}, featureNames(c.Unsupported))
	assert.Nil(t, c.Document.LoadOrders, "an order the game cannot write is not carried")
}

// A disabled entry is listed in place, as Vortex's extension writes it: the
// game's list cannot switch a file off.
func TestConvert_FileLoadOrderDisabledEntryIsListed(t *testing.T) {
	off := archiveEntry("b.archive", "")
	off.Enabled = false
	c := mustConvert(t, withArchiveOrder(convertInputFor(archiveLoadOrderManifest(archiveEntry("a.archive", ""), off))))
	assert.Equal(t, []string{"a.archive", "b.archive"}, c.Document.LoadOrders["cyberpunk2077-archive"])
	assert.True(t, containsSubstring(c.Notes, "disabled"), "%v", c.Notes)
}

// A stranger's file names are checked, never rewritten: one that could
// name a path, or holds a control or bidi character, refuses the document.
func TestConvert_FileLoadOrderRefusesUnsafeNames(t *testing.T) {
	for _, bad := range []string{"../x.archive", `sub\x.archive`, "a/b.archive", "x\x1b.archive", "x\u202e.archive", ".."} {
		_, err := convertCollection(withArchiveOrder(convertInputFor(archiveLoadOrderManifest(archiveEntry(bad, "")))))
		require.Error(t, err, "%q", bad)
		assert.True(t, errors.Is(err, ErrInvalidCollection), "%q: %v", bad, err)
	}
}

// #620 review: an order whose entries could not be read refuses the recipe
// only when the game writes that order.
func TestUnreadableLoadOrder(t *testing.T) {
	m := &source.CollectionManifest{FileLoadOrders: []source.ManifestFileLoadOrder{
		{ID: "cyberpunk2077-redmod", Entries: 1, Invalid: "entries[0].id is not a string"},
	}}
	archive := func(id string) bool { return id == "cyberpunk2077-archive" }
	assert.NoError(t, unreadableLoadOrder(m, archive), "an unsupported order is refused by its feature instead")
	assert.NoError(t, unreadableLoadOrder(m, nil))
	m.FileLoadOrders[0].ID = "cyberpunk2077-archive"
	err := unreadableLoadOrder(m, archive)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrInvalidManifest)

	// The converter still raises the unsupported order's feature.
	m.FileLoadOrders[0].ID = "cyberpunk2077-redmod"
	m.Name, m.Mods = "C", []source.ManifestMod{nexusMod("A", "1", "11", "aa")}
	c := mustConvert(t, withArchiveOrder(convertInputFor(m)))
	assert.Equal(t, []string{"file_load_order:cyberpunk2077-redmod"}, featureNames(c.Unsupported))
}

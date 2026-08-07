package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModDetailsFromItem: the local-first seed. Opening details must render
// immediately from the row already in hand, so everything the row knows has
// to survive the conversion - the network fetch only ADDS.
func TestModDetailsFromItem(t *testing.T) {
	item := ModItem{
		ID: "a", Name: "Mod A", Version: "1.5", Author: "Author A",
		Source: "src", Summary: "A short summary.",
		Endorsements: 42, HasEndorsements: true,
		UpdatePolicy: "notify", Locked: true, LockedVersion: "1.2.3",
		Status: "installed",
	}

	d := modDetailsFromItem(item)

	assert.Equal(t, "Mod A", d.Name)
	assert.Equal(t, "1.5", d.Version)
	assert.Equal(t, "Author A", d.Author)
	assert.Equal(t, "A short summary.", d.Summary)
	assert.Equal(t, int64(42), d.Endorsements)
	assert.True(t, d.HasEndorsements)
	require.NotNil(t, d.Installed, "an installed row must seed the Installed block locally")
	assert.Equal(t, "1.5", d.Installed.Version)
	assert.True(t, d.Installed.Locked)
	assert.Equal(t, "1.2.3", d.Installed.LockedVersion)
	assert.Empty(t, d.Description, "description is network-only; the seed must not invent one")
}

// TestModDetailsFromItem_NotInstalled: a search hit for an uninstalled mod
// gets no Installed block, matching mod show's omit rule.
func TestModDetailsFromItem_NotInstalled(t *testing.T) {
	d := modDetailsFromItem(ModItem{ID: "a", Name: "Mod A", Status: "available"})
	assert.Nil(t, d.Installed)
}

// TestModDetailsFromItem_ConvertPaks covers the three-state ConvertPaks
// distinction a parked finding from Task 2's review flagged as unexercised:
// nil means "does not apply to this mod at all" (not a compile-deploy game,
// or the mod has no pak merge source), while a non-nil pointer to false
// means "applies, and is switched off" - collapsing those two states would
// render a mod that CAN'T convert as one that merely has conversion off.
func TestModDetailsFromItem_ConvertPaks(t *testing.T) {
	installedItem := func(compileGame, hasPakSource, convertPaks bool) ModItem {
		return ModItem{
			ID: "a", Name: "Mod A", Status: "installed",
			CompileGame: compileGame, HasPakSource: hasPakSource, ConvertPaks: convertPaks,
		}
	}

	t.Run("not applicable when not a compile-deploy game", func(t *testing.T) {
		d := modDetailsFromItem(installedItem(false, true, true))
		require.NotNil(t, d.Installed)
		assert.Nil(t, d.Installed.ConvertPaks, "nil = not applicable, not \"off\"")
	})

	t.Run("not applicable when the mod has no pak merge source", func(t *testing.T) {
		d := modDetailsFromItem(installedItem(true, false, true))
		require.NotNil(t, d.Installed)
		assert.Nil(t, d.Installed.ConvertPaks, "nil = not applicable, not \"off\"")
	})

	t.Run("applies and is on", func(t *testing.T) {
		d := modDetailsFromItem(installedItem(true, true, true))
		require.NotNil(t, d.Installed)
		require.NotNil(t, d.Installed.ConvertPaks)
		assert.True(t, *d.Installed.ConvertPaks)
	})

	t.Run("applies and is off", func(t *testing.T) {
		d := modDetailsFromItem(installedItem(true, true, false))
		require.NotNil(t, d.Installed)
		require.NotNil(t, d.Installed.ConvertPaks, "a non-nil pointer to false must survive - this is the collapse Task 2's review flagged")
		assert.False(t, *d.Installed.ConvertPaks)
	})
}

// TestPrototypeGetModDetails: the prototype provider must serve details too,
// or `lmm tui --prototype` and most TUI tests can't exercise the view.
func TestPrototypeGetModDetails(t *testing.T) {
	p := newPrototypeProviderConcrete()
	_, items, err := p.Overview(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, items)

	d, err := p.GetModDetails(t.Context(), items[0])
	require.NoError(t, err)
	assert.Equal(t, items[0].Name, d.Name)
	assert.NotEmpty(t, d.Description, "the prototype must serve a description, or the view has nothing to show")
	assert.NotEmpty(t, d.SourceURL)
}

package adapter_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
)

// orderGame is a fake adapter with the file-load-order capability: one
// valid order and one with no list file, which the helpers drop.
type orderGame struct{ adapter.Generic }

func (orderGame) FileLoadOrders(*domain.Game) []adapter.FileLoadOrderSpec {
	return []adapter.FileLoadOrderSpec{
		{ID: "game-archive", Dir: adapter.RootedPath{Path: "archive/pc/mod"}, Extensions: []string{".archive"},
			ListFile: "modlist.txt", Format: adapter.FileListLines},
		{ID: "broken", Dir: adapter.RootedPath{Path: "x"}, Extensions: []string{".x"}, Format: adapter.FileListLines},
	}
}

func TestFileLoadOrders_AbsentCapabilityHasNone(t *testing.T) {
	assert.Empty(t, adapter.FileLoadOrders(adapter.Generic{}, &domain.Game{ID: "g"}))
	_, ok := adapter.FileLoadOrder(adapter.Generic{}, &domain.Game{ID: "g"}, "game-archive")
	assert.False(t, ok)
}

func TestFileLoadOrders_KeepsOnlyValidSpecs(t *testing.T) {
	specs := adapter.FileLoadOrders(orderGame{}, &domain.Game{ID: "g"})
	if assert.Len(t, specs, 1) {
		assert.Equal(t, "game-archive", specs[0].ID)
	}
	spec, ok := adapter.FileLoadOrder(orderGame{}, &domain.Game{ID: "g"}, "game-archive")
	assert.True(t, ok)
	assert.Equal(t, "modlist.txt", spec.ListFile)
	_, ok = adapter.FileLoadOrder(orderGame{}, &domain.Game{ID: "g"}, "broken")
	assert.False(t, ok, "an invalid spec is no load order")
}

func TestFileLoadOrderSpec_Covers(t *testing.T) {
	spec := adapter.FileLoadOrderSpec{Extensions: []string{".archive"}, ListFile: "modlist.txt"}
	assert.True(t, spec.Covers("basegame_x.archive"))
	assert.True(t, spec.Covers("! Fix.ARCHIVE"), "extensions compare case-insensitively")
	assert.False(t, spec.Covers(".archive"), "an extension alone names no file")
	assert.False(t, spec.Covers("readme.txt"))
	assert.False(t, spec.Covers("modlist.txt"))
	assert.Equal(t, "lines", adapter.FileListLines.String())
	assert.Equal(t, "none", adapter.FileListNone.String())
}

package cyberpunk2077_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/cyberpunk2077"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The archive order's table, fact by fact (package doc's [RED] and [CPX]).
func TestArchiveOrder(t *testing.T) {
	a := cyberpunk2077.New()
	assert.Equal(t, "cyberpunk2077", a.ID())
	spec, ok := adapter.FileLoadOrder(a, &domain.Game{ID: "cyberpunk2077"}, "cyberpunk2077-archive")
	require.True(t, ok)
	assert.Equal(t, adapter.RootedPath{Root: adapter.SpecRootGame, Path: "archive/pc/mod"}, spec.Dir)
	assert.Equal(t, "modlist.txt", spec.ListFile)
	assert.Equal(t, []string{".archive"}, spec.Extensions)
	assert.Equal(t, adapter.FileListLines, spec.Format)
	assert.Equal(t, "\r\n", spec.LineEnding)
	assert.True(t, spec.FirstWins)
	assert.Len(t, adapter.FileLoadOrders(a, &domain.Game{}), 1, "the REDmod order is not supported")
}

// The adapter is otherwise the identity: no layout, no per-file routing.
func TestIdentityLayout(t *testing.T) {
	l, err := cyberpunk2077.New().NormalizeArchive(adapter.NormalizeRequest{Members: []string{"archive/pc/mod/a.archive"}})
	require.NoError(t, err)
	assert.False(t, l.Applies())
	assert.True(t, adapter.IsLayoutNeutral(cyberpunk2077.New()))
	_, routes := any(cyberpunk2077.New()).(adapter.RootRouter)
	assert.False(t, routes)
}

package adapter_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
)

// prefixAdapter is a generic adapter that declares the #605 prefix
// capability, answering want.
type prefixAdapter struct {
	adapter.Generic
	want bool
}

func (p prefixAdapter) UsesPrefix(*domain.Game) bool { return p.want }

// TestUsesPrefix: an adapter without the capability needs no prefix -
// every adapter lmm ships today - and one with it answers for itself.
func TestUsesPrefix(t *testing.T) {
	g := &domain.Game{ID: "skyrim-se"}
	assert.False(t, adapter.UsesPrefix(adapter.Generic{}, g))
	assert.False(t, adapter.UsesPrefix(nil, g), "nil-safe, like every dispatch helper")
	assert.True(t, adapter.UsesPrefix(prefixAdapter{want: true}, g))
	assert.False(t, adapter.UsesPrefix(prefixAdapter{want: false}, g))
}

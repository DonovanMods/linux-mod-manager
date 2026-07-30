package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProfile_FindRef_Found guards the normal case: a ref matching
// sourceID+modID is returned, and it's a live pointer into p.Mods (not a
// copy) so a caller can read fields like Locked/Version off it directly.
func TestProfile_FindRef_Found(t *testing.T) {
	p := &Profile{Mods: []ModReference{
		{SourceID: "src", ModID: "a", Version: "1.0"},
		{SourceID: "src", ModID: "b", Version: "2.0", Locked: true},
	}}

	ref := p.FindRef("src", "b")

	require.NotNil(t, ref)
	assert.Equal(t, "2.0", ref.Version)
	assert.True(t, ref.Locked)
	assert.Same(t, &p.Mods[1], ref, "must be a pointer into p.Mods, not a copy")
}

// TestProfile_FindRef_NotFound guards the negative case: no match, wrong
// source, and an empty profile all return nil rather than a zero-value
// ModReference a caller might mistake for a real (if empty) match.
func TestProfile_FindRef_NotFound(t *testing.T) {
	p := &Profile{Mods: []ModReference{
		{SourceID: "src", ModID: "a", Version: "1.0"},
	}}

	assert.Nil(t, p.FindRef("src", "missing"))
	assert.Nil(t, p.FindRef("other-src", "a"), "sourceID must match too, not just modID")
	assert.Nil(t, (&Profile{}).FindRef("src", "a"), "empty profile")
}

// TestProfile_FindRef_NilReceiver guards the nil-receiver-safe contract the
// doc comment promises - callers that fall through a failed
// config.LoadProfile with a nil *Profile (the "_ = " tolerant-load
// precedent) can call FindRef unconditionally without a separate nil guard.
func TestProfile_FindRef_NilReceiver(t *testing.T) {
	var p *Profile

	assert.Nil(t, p.FindRef("src", "a"))
}

package core

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #613 follow-up: an update's target may name only its container's own
// bundled items and the ones the profile already records; any other
// bundled reference is refused, not let through by a blanket bypass.
func TestBundledRefAllowed_IsScopedToTheContainerAndTheProfile(t *testing.T) {
	b := &pendingBundle{collectionID: "c0ffee", names: map[string]string{"patch": "My Patch"}}
	current := &domain.Profile{Mods: []domain.ModReference{
		{SourceID: domain.SourceCollection, ModID: "other.tool", Version: "v1"},
		{SourceID: "nexusmods", ModID: "1", Version: "1.0"},
	}}
	allowed := bundledRefAllowed(b, current)
	ref := func(id, version string) domain.ModReference {
		return domain.ModReference{SourceID: domain.SourceCollection, ModID: id, Version: version}
	}
	assert.True(t, allowed(ref("c0ffee.patch", "any")), "the container's own item")
	assert.True(t, allowed(ref("other.tool", "v1")), "the profile's own, at the version it records")
	assert.False(t, allowed(ref("other.tool", "v2")), "not at another version")
	assert.False(t, allowed(ref("c0ffee.tool", "v1")), "an item the container does not name")
	assert.False(t, allowed(ref("evil.patch", "v1")), "another collection's item")
	assert.False(t, bundledRefAllowed(nil, &domain.Profile{})(ref("c0ffee.patch", "v1")), "no container, no profile: nothing")

	// planImport refuses a foreign bundled reference under the scoped check.
	svc := newBareService(t)
	game := &domain.Game{ID: "g", Name: "G", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	data, err := config.ExportProfile(&domain.Profile{Name: "p", GameID: "g", Mods: []domain.ModReference{
		ref("c0ffee.patch", "d1"), ref("evil.patch", "d1"),
	}})
	require.NoError(t, err)
	_, err = svc.planImport(context.Background(), game, data, b.carries)
	require.ErrorIs(t, err, ErrUnsafeImport)
	assert.Contains(t, err.Error(), "evil.patch")

	data, err = config.ExportProfile(&domain.Profile{Name: "p", GameID: "g", Mods: []domain.ModReference{ref("c0ffee.patch", "d1")}})
	require.NoError(t, err)
	_, err = svc.planImport(context.Background(), game, data, b.carries)
	require.NoError(t, err, "the container's own item plans")
}

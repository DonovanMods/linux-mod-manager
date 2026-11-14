package core_test

// #620: the profile's load_orders: block is carried by every flow that
// copies or restores a whole profile - a duplicate, a snapshot and its
// restore. (The collection format's carriage is loadorders_collection_test.go's.)

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var carriedLoadOrders = domain.FileLoadOrders{"cyberpunk2077-archive": {"b.archive", "a.archive"}}

func setProfileLoadOrders(t *testing.T, svc *core.Service, gameID, profileName string, orders domain.FileLoadOrders) {
	t.Helper()
	profile, err := config.LoadProfile(svc.ConfigDir(), gameID, profileName)
	require.NoError(t, err)
	profile.LoadOrders = orders
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))
}

func TestDuplicateProfile_CarriesTheLoadOrders(t *testing.T) {
	svc, game := duplicateFixture(t)
	setProfileLoadOrders(t, svc, game.ID, "default", carriedLoadOrders)
	_, err := svc.DuplicateProfile(context.Background(), game.ID, "default", "copy")
	require.NoError(t, err)
	dup, err := config.LoadProfile(svc.ConfigDir(), game.ID, "copy")
	require.NoError(t, err)
	assert.Equal(t, carriedLoadOrders, dup.LoadOrders)
}

func TestApplySnapshotRestore_RestoresTheLoadOrders(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()
	setProfileLoadOrders(t, svc, "g1", "default", carriedLoadOrders)
	_, err := svc.CreateSnapshot(ctx, game, "default", "with-orders")
	require.NoError(t, err)

	setProfileLoadOrders(t, svc, "g1", "default", domain.FileLoadOrders{"cyberpunk2077-archive": {"a.archive"}})
	plan, err := svc.PlanSnapshotRestore(ctx, game, "with-orders")
	require.NoError(t, err)
	assert.True(t, plan.ProfileChanged, "a load order that differs is a profile change")

	_, err = svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{NoSafetySnapshot: true}, nil)
	require.NoError(t, err)
	restored, err := config.LoadProfile(svc.ConfigDir(), "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, carriedLoadOrders, restored.LoadOrders)
}

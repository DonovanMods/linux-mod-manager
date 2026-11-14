package core_test

// #616: the profile's plugins: block is carried by every flow that copies
// or restores a whole profile - a duplicate, a snapshot and its restore.
// (The collection format's carriage is collection_plugins_test.go's.)

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setProfilePlugins(t *testing.T, svc *core.Service, gameID, profileName string, plugins domain.ProfilePlugins) {
	t.Helper()
	profile, err := config.LoadProfile(svc.ConfigDir(), gameID, profileName)
	require.NoError(t, err)
	profile.Plugins = plugins
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))
}

func TestDuplicateProfile_CarriesThePluginList(t *testing.T) {
	svc, game := duplicateFixture(t)
	setProfilePlugins(t, svc, game.ID, "default", collectionPlugins)
	_, err := svc.DuplicateProfile(context.Background(), game.ID, "default", "copy")
	require.NoError(t, err)
	dup, err := config.LoadProfile(svc.ConfigDir(), game.ID, "copy")
	require.NoError(t, err)
	assert.Equal(t, collectionPlugins, dup.Plugins)
}

func TestApplySnapshotRestore_RestoresThePluginList(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()
	setProfilePlugins(t, svc, "g1", "default", collectionPlugins)
	_, err := svc.CreateSnapshot(ctx, game, "default", "with-plugins")
	require.NoError(t, err)

	setProfilePlugins(t, svc, "g1", "default", domain.ProfilePlugins{Order: []domain.PluginEntry{{Name: "Other.esp", Enabled: true}}})
	plan, err := svc.PlanSnapshotRestore(ctx, game, "with-plugins")
	require.NoError(t, err)
	assert.True(t, plan.ProfileChanged, "a plugin list that differs is a profile change")

	_, err = svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{NoSafetySnapshot: true}, nil)
	require.NoError(t, err)
	restored, err := config.LoadProfile(svc.ConfigDir(), "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, collectionPlugins, restored.Plugins)
}

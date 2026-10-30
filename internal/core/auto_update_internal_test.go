package core

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestService_SetAutoUpdateDoesNotWaitForARunningMutation (#586 review):
// Setup > Updates' switch is a config.yaml write, not a mutation, so it
// answers at once while a deploy holds the mutation slot.
func TestService_SetAutoUpdateDoesNotWaitForARunningMutation(t *testing.T) {
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	release, err := svc.beginOp(context.Background())
	require.NoError(t, err)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	require.NoError(t, svc.SetAutoUpdate(ctx, false))
	assert.Less(t, time.Since(start), time.Second)
	assert.False(t, autoUpdateEnabled(svc.configDir))
}

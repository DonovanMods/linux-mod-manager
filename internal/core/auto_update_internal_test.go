package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

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

// TestConfigWrites_SerializeAcrossProcesses (#586 review): a config.yaml
// write - the web UI's switch, here - waits for another lmm process's
// read-modify-write of it, which holds the config dir's lock, rather than
// writing between that process's read and its write and losing its own
// setting.
func TestConfigWrites_SerializeAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	// The other process: it takes the lock and reads config.yaml...
	other, err := os.OpenFile(filepath.Join(dir, configLockFile), os.O_RDWR|os.O_CREATE, 0o600)
	require.NoError(t, err)
	defer func() { _ = other.Close() }()
	require.NoError(t, syscall.Flock(int(other.Fd()), syscall.LOCK_EX))
	cfg, err := config.Load(dir)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- saveAutoUpdate(dir, false) }()
	select {
	case err := <-done:
		t.Fatalf("the write did not wait for the other process's lock (err %v)", err)
	case <-time.After(200 * time.Millisecond):
	}

	// ...and writes its own setting, then lets go.
	cfg.DefaultGame = "skyrim"
	require.NoError(t, cfg.Save(dir))
	require.NoError(t, syscall.Flock(int(other.Fd()), syscall.LOCK_UN))
	require.NoError(t, <-done)

	got, err := config.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "skyrim", got.DefaultGame, "the other process's write stands")
	assert.False(t, got.AutoUpdateEnabled(), "and this one, made after it, too")
}

// TestConfigWrites_TwoWritersLoseNothing: every config.yaml writer - the
// default game and auto_update - goes through the same lock, so writers
// racing each other keep both settings.
func TestConfigWrites_TwoWritersLoseNothing(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); assert.NoError(t, saveDefaultGame(dir, fmt.Sprintf("game-%d", i))) }()
		go func() { defer wg.Done(); assert.NoError(t, saveAutoUpdate(dir, i%2 == 0)) }()
	}
	wg.Wait()
	require.NoError(t, saveAutoUpdate(dir, false))
	got, err := config.Load(dir)
	require.NoError(t, err)
	assert.NotEmpty(t, got.DefaultGame)
	assert.False(t, got.AutoUpdateEnabled())
}

package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoUpdate_OnByDefaultAndOffOnlyWhenSaidSo pins #586's opt-out: an
// absent auto_update means ON, only an explicit false turns it off, a save
// of a config that never set it does not spell the default out, and the
// opt-out survives every save.
func TestAutoUpdate_OnByDefaultAndOffOnlyWhenSaidSo(t *testing.T) {
	dir := t.TempDir()

	cfg, err := config.Load(dir)
	require.NoError(t, err)
	assert.True(t, cfg.AutoUpdateEnabled(), "no config.yaml at all")
	require.NoError(t, cfg.Save(dir))
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "auto_update")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("default_game: x\n"), 0o644))
	cfg, err = config.Load(dir)
	require.NoError(t, err)
	assert.True(t, cfg.AutoUpdateEnabled(), "config.yaml without the key")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("auto_update: false\n"), 0o644))
	cfg, err = config.Load(dir)
	require.NoError(t, err)
	assert.False(t, cfg.AutoUpdateEnabled())
	cfg.DefaultGame = "y" // an unrelated write must keep the opt-out
	require.NoError(t, cfg.Save(dir))
	data, err = os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "auto_update: false", "an explicit opt-out survives a save")
	cfg, err = config.Load(dir)
	require.NoError(t, err)
	assert.False(t, cfg.AutoUpdateEnabled())

	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("auto_update: true\n"), 0o644))
	cfg, err = config.Load(dir)
	require.NoError(t, err)
	assert.True(t, cfg.AutoUpdateEnabled(), "a v2.2.0 opt-in still reads as on")
}

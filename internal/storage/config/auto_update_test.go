package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoUpdate_OffByDefaultAndOnlyWhenSaidSo pins #569's opt-in: an
// absent auto_update means OFF, only an explicit true turns it on, and a
// save of a config that never set it does not spell the default out.
func TestAutoUpdate_OffByDefaultAndOnlyWhenSaidSo(t *testing.T) {
	dir := t.TempDir()

	cfg, err := config.Load(dir)
	require.NoError(t, err)
	assert.False(t, cfg.AutoUpdate, "no config.yaml at all")
	require.NoError(t, cfg.Save(dir))
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "auto_update")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("auto_update: true\n"), 0o644))
	cfg, err = config.Load(dir)
	require.NoError(t, err)
	assert.True(t, cfg.AutoUpdate)
	require.NoError(t, cfg.Save(dir))
	data, err = os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "auto_update: true", "the opt-in survives a save")
}

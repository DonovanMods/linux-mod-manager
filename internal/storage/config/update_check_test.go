package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateCheck_OnByDefaultAndOffOnlyWhenSaidSo pins #568's setting: an
// absent update_check means ON, only an explicit false turns it off, and a
// save of a config that never set it does not spell the default out.
func TestUpdateCheck_OnByDefaultAndOffOnlyWhenSaidSo(t *testing.T) {
	dir := t.TempDir()

	cfg, err := config.Load(dir)
	require.NoError(t, err)
	assert.True(t, cfg.UpdateCheckEnabled(), "no config.yaml at all")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("default_game: x\n"), 0o644))
	cfg, err = config.Load(dir)
	require.NoError(t, err)
	assert.True(t, cfg.UpdateCheckEnabled(), "config.yaml without the key")
	require.NoError(t, cfg.Save(dir))
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "update_check")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("update_check: false\n"), 0o644))
	cfg, err = config.Load(dir)
	require.NoError(t, err)
	assert.False(t, cfg.UpdateCheckEnabled())
	require.NoError(t, cfg.Save(dir))
	data, err = os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "update_check: false", "an explicit opt-out survives a save")
}

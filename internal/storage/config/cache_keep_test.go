package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCacheKeepPreviousVersions_DefaultsAndSpellings pins #678's setting:
// cache.keep_previous_versions is 3 when absent, a count as written (0
// keeps only the installed versions), and "unlimited" or any negative
// count means unlimited (CacheKeepPreviousVersions answers -1).
func TestCacheKeepPreviousVersions_DefaultsAndSpellings(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) *config.Config {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644))
		cfg, err := config.Load(dir)
		require.NoError(t, err)
		return cfg
	}

	cfg, err := config.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, config.DefaultCacheKeepPreviousVersions, cfg.CacheKeepPreviousVersions(), "no config.yaml")
	assert.Equal(t, 3, config.DefaultCacheKeepPreviousVersions)

	assert.Equal(t, 3, write("default_game: x\n").CacheKeepPreviousVersions())
	assert.Equal(t, 5, write("cache:\n  keep_previous_versions: 5\n").CacheKeepPreviousVersions())
	assert.Equal(t, 0, write("cache:\n  keep_previous_versions: 0\n").CacheKeepPreviousVersions())
	assert.Equal(t, config.UnlimitedPreviousVersions, write("cache:\n  keep_previous_versions: unlimited\n").CacheKeepPreviousVersions())
	assert.Equal(t, config.UnlimitedPreviousVersions, write("cache:\n  keep_previous_versions: -1\n").CacheKeepPreviousVersions())
	assert.Equal(t, config.UnlimitedPreviousVersions, write("cache:\n  keep_previous_versions: -7\n").CacheKeepPreviousVersions())
	assert.Equal(t, 3, write("cache: {}\n").CacheKeepPreviousVersions())
}

// TestCacheKeepPreviousVersions_RefusesNonsense: a word other than
// "unlimited", or a fraction, is a config error that names the key.
func TestCacheKeepPreviousVersions_RefusesNonsense(t *testing.T) {
	for _, body := range []string{
		"cache:\n  keep_previous_versions: lots\n",
		"cache:\n  keep_previous_versions: 2.5\n",
		"cache:\n  keep_previous_versions: [1]\n",
	} {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644))
		_, err := config.Load(dir)
		require.Error(t, err, body)
		assert.Contains(t, err.Error(), "keep_previous_versions", body)
	}
}

// TestCacheKeepPreviousVersions_SaveRoundTrip: a config that never set the
// key is saved without it; a set count survives a save, and unlimited is
// written as the word.
func TestCacheKeepPreviousVersions_SaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(dir)
	require.NoError(t, err)
	require.NoError(t, cfg.Save(dir))
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "cache:")

	cfg.SetCacheKeepPreviousVersions(7)
	require.NoError(t, cfg.Save(dir))
	cfg, err = config.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, 7, cfg.CacheKeepPreviousVersions())

	cfg.SetCacheKeepPreviousVersions(0)
	require.NoError(t, cfg.Save(dir))
	cfg, err = config.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.CacheKeepPreviousVersions())

	cfg.SetCacheKeepPreviousVersions(-5)
	require.NoError(t, cfg.Save(dir))
	data, err = os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "keep_previous_versions: unlimited")
	cfg, err = config.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, config.UnlimitedPreviousVersions, cfg.CacheKeepPreviousVersions())
}

package domain_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeployedPath_RoundTrip(t *testing.T) {
	key := domain.InstallPathKey("enbseries/a.fx")
	rel, root := domain.SplitDeployedPath(key)
	assert.True(t, root)
	assert.Equal(t, "enbseries/a.fx", rel)
	assert.True(t, domain.IsInstallPathKey(key))

	rel, root = domain.SplitDeployedPath("textures/a.dds")
	assert.False(t, root)
	assert.Equal(t, "textures/a.dds", rel)
	assert.False(t, domain.IsInstallPathKey("textures/a.dds"))
}

func TestDisplayDeployedPath(t *testing.T) {
	assert.Equal(t, "<game root>/d3d11.dll", domain.DisplayDeployedPath(domain.InstallPathKey("d3d11.dll")))
	assert.Equal(t, "textures/a.dds", domain.DisplayDeployedPath("textures/a.dds"))
}

// The key's point: a path joined onto a directory without asking its root
// is one no system call accepts, so a code path that forgot cannot touch
// the mod_path file of the same name.
func TestInstallPathKey_UnjoinedKeyIsRefusedBySyscalls(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "d3d11.dll"), []byte("x"), 0o644))
	_, err := os.Lstat(filepath.Join(dir, domain.InstallPathKey("d3d11.dll")))
	require.Error(t, err)
	assert.False(t, os.IsNotExist(err), "a NUL in a path is refused outright, not reported missing")
}

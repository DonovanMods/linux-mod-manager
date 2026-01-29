package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadProfile_WithHooks(t *testing.T) {
	tempDir := t.TempDir()
	profileDir := filepath.Join(tempDir, "games", "skyrim-se", "profiles")
	require.NoError(t, os.MkdirAll(profileDir, 0755))

	profileYAML := `name: modded
game_id: skyrim-se
mods: []
hooks:
  install:
    after_all: ""
  uninstall:
    after_all: "~/.config/lmm/hooks/custom-cleanup.sh"
`
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "modded.yaml"), []byte(profileYAML), 0644))

	profile, err := LoadProfile(tempDir, "skyrim-se", "modded")
	require.NoError(t, err)

	// Empty string means explicitly disabled
	assert.Equal(t, "", profile.Hooks.Install.AfterAll)
	assert.True(t, profile.HooksExplicit.Install.AfterAll)

	// Custom hook with tilde expansion
	assert.Contains(t, profile.Hooks.Uninstall.AfterAll, "custom-cleanup.sh")
	assert.True(t, profile.HooksExplicit.Uninstall.AfterAll)

	// Unset hooks should not be marked explicit
	assert.False(t, profile.HooksExplicit.Install.BeforeAll)
	assert.False(t, profile.HooksExplicit.Uninstall.BeforeAll)
}

func TestLoadProfile_NoHooks(t *testing.T) {
	tempDir := t.TempDir()
	profileDir := filepath.Join(tempDir, "games", "skyrim-se", "profiles")
	require.NoError(t, os.MkdirAll(profileDir, 0755))

	profileYAML := `name: default
game_id: skyrim-se
mods: []
`
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "default.yaml"), []byte(profileYAML), 0644))

	profile, err := LoadProfile(tempDir, "skyrim-se", "default")
	require.NoError(t, err)

	// No hooks should be marked as explicit
	assert.False(t, profile.HooksExplicit.Install.BeforeAll)
	assert.False(t, profile.HooksExplicit.Install.AfterAll)
	assert.False(t, profile.HooksExplicit.Uninstall.AfterAll)
}

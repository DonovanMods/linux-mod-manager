package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

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

// TestSaveProfile_RoundTripsHooks guards #295: SaveProfile must serialize
// profile.Hooks/HooksExplicit back to the hooks: block it reads via
// parseProfileHooks, or a load -> mutate -> SaveProfile cycle (every
// ProfileManager mutation - UpsertMod/SetModLock/ClearModLock/RemoveMod/
// ReorderMods/SetDefault) silently wipes a hand-edited hooks override.
// Reuses TestLoadProfile_WithHooks's exact fixture, including its
// explicitly-empty install.after_all override (the *string-pointer "unset
// vs set-to-empty" distinction parseProfileHooks/its serializer must both
// honor) alongside the explicit non-empty uninstall.after_all.
func TestSaveProfile_RoundTripsHooks(t *testing.T) {
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

	loaded, err := LoadProfile(tempDir, "skyrim-se", "modded")
	require.NoError(t, err)
	require.True(t, loaded.HooksExplicit.Install.AfterAll, "fixture precondition: install.after_all must be explicit")
	require.True(t, loaded.HooksExplicit.Uninstall.AfterAll, "fixture precondition: uninstall.after_all must be explicit")

	// The mutate step every ProfileManager write performs: load, touch
	// something unrelated to hooks, save.
	loaded.IsDefault = true
	require.NoError(t, SaveProfile(tempDir, loaded))

	reloaded, err := LoadProfile(tempDir, "skyrim-se", "modded")
	require.NoError(t, err)

	assert.Equal(t, loaded.Hooks, reloaded.Hooks, "Hooks must survive a save/reload cycle byte-for-byte")
	assert.Equal(t, loaded.HooksExplicit, reloaded.HooksExplicit, "HooksExplicit must survive a save/reload cycle byte-for-byte")

	// The explicitly-empty override must still be explicit after the
	// round trip, not silently dropped back to "inherit from game".
	assert.Equal(t, "", reloaded.Hooks.Install.AfterAll)
	assert.True(t, reloaded.HooksExplicit.Install.AfterAll)
	assert.Contains(t, reloaded.Hooks.Uninstall.AfterAll, "custom-cleanup.sh")
	assert.True(t, reloaded.HooksExplicit.Uninstall.AfterAll)
	assert.False(t, reloaded.HooksExplicit.Install.BeforeAll)
	assert.False(t, reloaded.HooksExplicit.Uninstall.BeforeAll)

	// The written YAML must still carry the hooks: block on disk, not just
	// round-trip correctly through the parser.
	raw, err := os.ReadFile(filepath.Join(profileDir, "modded.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "hooks:")
	assert.Contains(t, string(raw), "after_all:")
	assert.Contains(t, string(raw), "custom-cleanup.sh")
}

// TestSaveProfile_NoHooksProfile_ByteIdenticalOutput pins SaveProfile's
// output for a profile carrying no hook overrides to the exact bytes it
// produced before #295's fix (captured from e67580f via a throwaway
// SaveProfile call), so the serializer this fix adds must not perturb the
// common case: no hooks: block, no other field reordered.
func TestSaveProfile_NoHooksProfile_ByteIdenticalOutput(t *testing.T) {
	tempDir := t.TempDir()
	profile := &domain.Profile{
		Name:   "modded",
		GameID: "skyrim-se",
		Mods: []domain.ModReference{
			{SourceID: "nexus", ModID: "123", Version: "1.0", FileIDs: []string{"f1"}, Locked: false},
		},
	}
	require.NoError(t, SaveProfile(tempDir, profile))

	data, err := os.ReadFile(filepath.Join(tempDir, "games", "skyrim-se", "profiles", "modded.yaml"))
	require.NoError(t, err)

	const wantPreFixOutput = "name: modded\ngame_id: skyrim-se\nmods:\n    - source_id: nexus\n      mod_id: \"123\"\n      version: \"1.0\"\n      file_ids:\n        - f1\n"
	assert.Equal(t, wantPreFixOutput, string(data))
}

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

// --- #296: profile export/import round-trips hook overrides ---

// exportedNoHooksYAML is what ExportProfile produced for a hookless profile
// BEFORE #296, captured verbatim off the pre-change implementation. It is
// the backward-compatibility pin: adding the hooks block must not change one
// byte of an export that has no hooks to write.
const exportedNoHooksYAML = `name: default
game_id: skyrim-se
mods:
    - source_id: nexusmods
      mod_id: "42"
      version: 1.0.0
      file_ids:
        - f1
`

func hooklessExportProfile() *domain.Profile {
	return &domain.Profile{
		Name: "default", GameID: "skyrim-se",
		Mods: []domain.ModReference{{SourceID: "nexusmods", ModID: "42", Version: "1.0.0", FileIDs: []string{"f1"}}},
	}
}

// TestExportProfile_NoHooks_BytesUnchanged pins the pre-#296 bytes: a
// profile with no explicit hook overrides exports EXACTLY what it always
// exported, with no hooks: key at all.
func TestExportProfile_NoHooks_BytesUnchanged(t *testing.T) {
	data, err := ExportProfile(hooklessExportProfile())
	require.NoError(t, err)
	assert.Equal(t, exportedNoHooksYAML, string(data))
}

// TestImportProfile_NoHooks_ReadsLegacyExport is the other half: an export
// recorded before #296 (no hooks: key) still imports, with nothing marked
// explicit, so a shared profile file from an older lmm keeps working.
func TestImportProfile_NoHooks_ReadsLegacyExport(t *testing.T) {
	profile, err := ImportProfile([]byte(exportedNoHooksYAML))
	require.NoError(t, err)
	assert.Equal(t, "default", profile.Name)
	assert.Equal(t, domain.GameHooks{}, profile.Hooks)
	assert.Equal(t, domain.GameHooksExplicit{}, profile.HooksExplicit)
}

// hookedExportProfile carries both halves of the *string-pointer encoding:
// an explicitly-DISABLED hook (explicit, empty value) and an explicitly-SET
// one. A round trip that loses the distinction turns "don't run the game's
// install.after_all for this profile" back into "inherit it".
func hookedExportProfile() *domain.Profile {
	return &domain.Profile{
		Name: "modded", GameID: "skyrim-se",
		Mods: []domain.ModReference{{SourceID: "nexusmods", ModID: "42", Version: "1.0.0"}},
		Hooks: domain.GameHooks{
			Install:   domain.HookConfig{AfterAll: ""},
			Uninstall: domain.HookConfig{AfterAll: "/opt/lmm/cleanup.sh", BeforeEach: "/opt/lmm/pre.sh"},
		},
		HooksExplicit: domain.GameHooksExplicit{
			Install:   domain.HookExplicitFlags{AfterAll: true},
			Uninstall: domain.HookExplicitFlags{AfterAll: true, BeforeEach: true},
		},
	}
}

// TestExportProfile_RoundTripsHooks is #296: export -> import must preserve
// the hook overrides AND their explicit flags, and re-exporting the imported
// profile must produce byte-identical YAML.
func TestExportProfile_RoundTripsHooks(t *testing.T) {
	original := hookedExportProfile()

	data, err := ExportProfile(original)
	require.NoError(t, err)
	assert.Contains(t, string(data), "hooks:", "an explicit hook override must reach the exported YAML")

	imported, err := ImportProfile(data)
	require.NoError(t, err)
	assert.Equal(t, original.Hooks, imported.Hooks)
	assert.Equal(t, original.HooksExplicit, imported.HooksExplicit)

	again, err := ExportProfile(imported)
	require.NoError(t, err)
	assert.Equal(t, string(data), string(again), "export -> import -> export must be byte-for-byte stable")
}

// TestExportProfile_HooksUseTheProfileFileEncoding pins the wire shape: the
// same *string-pointer "unset vs set-to-empty" encoding a profile file uses
// (parseProfileHooks/serializeProfileHooks), not a flat domain.GameHooks
// dump - an unset hook is absent, an explicitly-disabled one is present and
// empty.
func TestExportProfile_HooksUseTheProfileFileEncoding(t *testing.T) {
	data, err := ExportProfile(hookedExportProfile())
	require.NoError(t, err)

	var cfg struct {
		Hooks ProfileHooksYAML `yaml:"hooks"`
	}
	require.NoError(t, yaml.Unmarshal(data, &cfg))

	require.NotNil(t, cfg.Hooks.Install.AfterAll)
	assert.Equal(t, "", *cfg.Hooks.Install.AfterAll, "an explicitly-disabled hook is present-but-empty")
	assert.Nil(t, cfg.Hooks.Install.BeforeAll, "an unset hook is absent, so it still inherits from the game")
	require.NotNil(t, cfg.Hooks.Uninstall.AfterAll)
	assert.Equal(t, "/opt/lmm/cleanup.sh", *cfg.Hooks.Uninstall.AfterAll)
}

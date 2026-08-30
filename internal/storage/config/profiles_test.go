package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedName adapts a constant payload to invalidProfileNames' shape.
func fixedName(name string) func(string) string {
	return func(string) string { return name }
}

// invalidProfileNames builds names that must be rejected before any
// filesystem access: they are empty, or would resolve to a path outside the
// profiles directory once joined (filepath.Join collapses ".." segments).
// Each payload is a func of the test's temp root so path-shaped payloads
// stay inside it — even a guard regression can only touch the sandbox.
var invalidProfileNames = map[string]func(root string) string{
	"empty":            fixedName(""),
	"whitespace only":  fixedName("   "),
	"parent traversal": fixedName("../evil"),
	"deep traversal":   fixedName("../../../etc/cron.d/evil"),
	"subdirectory":     fixedName("a/b"),
	"absolute path": func(root string) string {
		return filepath.Join(root, "outside", "evil")
	},
	"bare dotdot": fixedName(".."),
	"backslash":   fixedName(`a\b`),
}

// listRegularFiles returns every regular file under root, relative to root.
func listRegularFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, rel)
		}
		return nil
	})
	require.NoError(t, err)
	return files
}

func TestSaveProfile_RejectsInvalidName(t *testing.T) {
	for label, makeName := range invalidProfileNames {
		t.Run(label, func(t *testing.T) {
			// configDir is nested so traversal payloads land inside the
			// walkable temp root instead of escaping it.
			tempDir := t.TempDir()
			configDir := filepath.Join(tempDir, "deep", "nested", "config")
			require.NoError(t, os.MkdirAll(configDir, 0755))

			profile := &domain.Profile{Name: makeName(tempDir), GameID: "skyrim-se"}
			err := SaveProfile(configDir, profile)

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidProfileName)
			assert.Empty(t, listRegularFiles(t, tempDir), "no file may be written for an invalid profile name")
		})
	}
}

func TestLoadProfile_RejectsInvalidName(t *testing.T) {
	for label, makeName := range invalidProfileNames {
		t.Run(label, func(t *testing.T) {
			tempDir := t.TempDir()
			_, err := LoadProfile(tempDir, "skyrim-se", makeName(tempDir))

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidProfileName)
		})
	}
}

func TestSaveProfile_RejectsInvalidGameID(t *testing.T) {
	for label, makeID := range invalidProfileNames {
		t.Run(label, func(t *testing.T) {
			tempDir := t.TempDir()
			configDir := filepath.Join(tempDir, "deep", "nested", "config")
			require.NoError(t, os.MkdirAll(configDir, 0755))

			profile := &domain.Profile{Name: "good", GameID: makeID(tempDir)}
			err := SaveProfile(configDir, profile)

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidGameID)
			assert.Empty(t, listRegularFiles(t, tempDir), "no file may be written for an invalid game ID")
		})
	}
}

func TestLoadProfile_RejectsInvalidGameID(t *testing.T) {
	for label, makeID := range invalidProfileNames {
		t.Run(label, func(t *testing.T) {
			tempDir := t.TempDir()
			_, err := LoadProfile(tempDir, makeID(tempDir), "good")

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidGameID)
		})
	}
}

func TestListProfiles_RejectsInvalidGameID(t *testing.T) {
	for label, makeID := range invalidProfileNames {
		t.Run(label, func(t *testing.T) {
			tempDir := t.TempDir()
			_, err := ListProfiles(tempDir, makeID(tempDir))

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidGameID)
		})
	}
}

func TestDeleteProfile_RejectsInvalidGameID(t *testing.T) {
	for label, makeID := range invalidProfileNames {
		t.Run(label, func(t *testing.T) {
			tempDir := t.TempDir()
			err := DeleteProfile(tempDir, makeID(tempDir), "good")

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidGameID)
		})
	}
}

func TestDeleteProfile_RejectsInvalidName(t *testing.T) {
	for label, makeName := range invalidProfileNames {
		t.Run(label, func(t *testing.T) {
			// Plant a file where "../evil" would resolve to prove Delete
			// never touches paths outside the profiles directory.
			tempDir := t.TempDir()
			configDir := filepath.Join(tempDir, "config")
			gameDir := filepath.Join(configDir, "games", "skyrim-se")
			require.NoError(t, os.MkdirAll(filepath.Join(gameDir, "profiles"), 0755))
			victim := filepath.Join(gameDir, "evil.yaml")
			require.NoError(t, os.WriteFile(victim, []byte("do not delete"), 0644))

			err := DeleteProfile(configDir, "skyrim-se", makeName(tempDir))

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidProfileName)
			assert.FileExists(t, victim, "delete must not remove files outside the profiles directory")
		})
	}
}

// profileYAML reads a saved profile back as raw text, so tests can assert on the
// keys actually written rather than on what a round-trip happens to reconstruct.
func profileYAML(t *testing.T, configDir, gameID, profileName string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, "games", gameID, "profiles", profileName+".yaml"))
	require.NoError(t, err)
	return string(data)
}

// TestSaveProfile_OmitsLinkMethodWhenNotExplicit guards the regression that made
// the profile-level override unimplementable: LinkMethod.String() never returns
// "", so `omitempty` never fired and every profile file gained a phantom
// `link_method: symlink` — indistinguishable from a deliberate choice.
func TestSaveProfile_OmitsLinkMethodWhenNotExplicit(t *testing.T) {
	configDir := t.TempDir()
	profile := &domain.Profile{Name: "default", GameID: "skyrim-se"}

	require.NoError(t, SaveProfile(configDir, profile))

	assert.NotContains(t, profileYAML(t, configDir, "skyrim-se", "default"), "link_method",
		"a profile with no explicit link method must not write the key")
}

func TestSaveProfile_WritesExplicitLinkMethod(t *testing.T) {
	configDir := t.TempDir()
	profile := &domain.Profile{
		Name:               "default",
		GameID:             "skyrim-se",
		LinkMethod:         domain.LinkCopy,
		LinkMethodExplicit: true,
	}

	require.NoError(t, SaveProfile(configDir, profile))

	assert.Contains(t, profileYAML(t, configDir, "skyrim-se", "default"), "link_method: copy")
}

// TestSaveProfile_PreservesExplicitSymlink pins the case the explicit flag exists
// for: symlink is the zero value, so without the flag it is indistinguishable
// from unset and would be dropped on save.
func TestSaveProfile_PreservesExplicitSymlink(t *testing.T) {
	configDir := t.TempDir()
	profile := &domain.Profile{
		Name:               "default",
		GameID:             "skyrim-se",
		LinkMethod:         domain.LinkSymlink,
		LinkMethodExplicit: true,
	}

	require.NoError(t, SaveProfile(configDir, profile))

	assert.Contains(t, profileYAML(t, configDir, "skyrim-se", "default"), "link_method: symlink")
}

func TestLoadProfile_TracksLinkMethodExplicit(t *testing.T) {
	tests := map[string]struct {
		yaml           string
		wantExplicit   bool
		wantLinkMethod domain.LinkMethod
	}{
		"absent":   {"name: default\ngame_id: skyrim-se\n", false, domain.LinkSymlink},
		"hardlink": {"name: default\ngame_id: skyrim-se\nlink_method: hardlink\n", true, domain.LinkHardlink},
		"symlink":  {"name: default\ngame_id: skyrim-se\nlink_method: symlink\n", true, domain.LinkSymlink},
	}

	for label, tc := range tests {
		t.Run(label, func(t *testing.T) {
			configDir := t.TempDir()
			profileDir := filepath.Join(configDir, "games", "skyrim-se", "profiles")
			require.NoError(t, os.MkdirAll(profileDir, 0755))
			require.NoError(t, os.WriteFile(filepath.Join(profileDir, "default.yaml"), []byte(tc.yaml), 0644))

			profile, err := LoadProfile(configDir, "skyrim-se", "default")
			require.NoError(t, err)

			assert.Equal(t, tc.wantExplicit, profile.LinkMethodExplicit)
			assert.Equal(t, tc.wantLinkMethod, profile.LinkMethod)
		})
	}
}

// TestSaveProfile_RoundTripDoesNotInventLinkMethod is the end-to-end shape of the
// bug: every profile mutation goes load -> modify -> save, so a phantom key added
// on save would be read back as explicit on the next load and become permanent.
func TestSaveProfile_RoundTripDoesNotInventLinkMethod(t *testing.T) {
	configDir := t.TempDir()
	require.NoError(t, SaveProfile(configDir, &domain.Profile{Name: "default", GameID: "skyrim-se"}))

	loaded, err := LoadProfile(configDir, "skyrim-se", "default")
	require.NoError(t, err)
	require.False(t, loaded.LinkMethodExplicit)

	require.NoError(t, SaveProfile(configDir, loaded))

	assert.NotContains(t, profileYAML(t, configDir, "skyrim-se", "default"), "link_method")
}

func TestExportProfile_OmitsLinkMethodWhenNotExplicit(t *testing.T) {
	data, err := ExportProfile(&domain.Profile{Name: "default", GameID: "skyrim-se"})
	require.NoError(t, err)

	assert.NotContains(t, string(data), "link_method",
		"an exported profile must not carry a link method the user never set")
}

func TestExportProfile_WritesExplicitLinkMethod(t *testing.T) {
	data, err := ExportProfile(&domain.Profile{
		Name:               "default",
		GameID:             "skyrim-se",
		LinkMethod:         domain.LinkHardlink,
		LinkMethodExplicit: true,
	})
	require.NoError(t, err)

	assert.Contains(t, string(data), "link_method: hardlink")
}

// TestExportProfileValue_MatchesExportProfileFields pins that
// ExportProfileValue (the shared building block for `lmm profile export
// --json`'s core.Service.ExportProfile) carries the exact fields
// ExportProfile marshals to YAML, unwrapped from the pointer-hook encoding
// (#309): LinkMethod only when explicit, Overrides as strings, Mods/Hooks/
// HooksExplicit copied straight from the domain.Profile.
func TestExportProfileValue_MatchesExportProfileFields(t *testing.T) {
	profile := &domain.Profile{
		Name: "default", GameID: "skyrim-se",
		Mods:               []domain.ModReference{{SourceID: "src", ModID: "a", Version: "1.0"}},
		LinkMethod:         domain.LinkHardlink,
		LinkMethodExplicit: true,
		Overrides:          map[string][]byte{"config/game.ini": []byte("[General]\n")},
		Hooks:              domain.GameHooks{Install: domain.HookConfig{AfterAll: "scripts/after_all.sh"}},
		HooksExplicit:      domain.GameHooksExplicit{Install: domain.HookExplicitFlags{AfterAll: true}},
	}

	got := ExportProfileValue(profile)
	require.NotNil(t, got)
	assert.Equal(t, "default", got.Name)
	assert.Equal(t, "skyrim-se", got.GameID)
	assert.Equal(t, profile.Mods, got.Mods)
	assert.Equal(t, "hardlink", got.LinkMethod)
	assert.Equal(t, map[string]string{"config/game.ini": "[General]\n"}, got.Overrides)
	assert.Equal(t, profile.Hooks, got.Hooks)
	assert.Equal(t, profile.HooksExplicit, got.HooksExplicit)
}

// TestExportProfileValue_OmitsLinkMethodWhenNotExplicit mirrors
// TestExportProfile_OmitsLinkMethodWhenNotExplicit: the JSON path must not
// claim a link method the user never set either.
func TestExportProfileValue_OmitsLinkMethodWhenNotExplicit(t *testing.T) {
	got := ExportProfileValue(&domain.Profile{Name: "default", GameID: "skyrim-se"})
	assert.Empty(t, got.LinkMethod)
}

func TestListProfiles_MissingDir(t *testing.T) {
	tempDir := t.TempDir()

	profiles, err := ListProfiles(tempDir, "skyrim-se")

	require.NoError(t, err)
	assert.Empty(t, profiles)
}

func TestListProfiles_EmptyDir(t *testing.T) {
	tempDir := t.TempDir()
	profileDir := filepath.Join(tempDir, "games", "skyrim-se", "profiles")
	require.NoError(t, os.MkdirAll(profileDir, 0755))

	profiles, err := ListProfiles(tempDir, "skyrim-se")

	require.NoError(t, err)
	assert.Empty(t, profiles)
}

// TestListProfiles_IgnoresNonYAMLAndDirs pins that ListProfiles only reports
// regular files with a ".yaml" suffix, stripped of the extension: non-YAML
// files are skipped, and entries that are themselves directories are skipped
// even if their name happens to end in ".yaml".
func TestListProfiles_IgnoresNonYAMLAndDirs(t *testing.T) {
	tempDir := t.TempDir()
	profileDir := filepath.Join(tempDir, "games", "skyrim-se", "profiles")
	require.NoError(t, os.MkdirAll(profileDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "default.yaml"), []byte("name: default\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "hardcore.yaml"), []byte("name: hardcore\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "notes.txt"), []byte("ignored"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(profileDir, "subdir.yaml"), 0755))

	profiles, err := ListProfiles(tempDir, "skyrim-se")

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"default", "hardcore"}, profiles)
}

func TestDeleteProfile(t *testing.T) {
	configDir := t.TempDir()
	require.NoError(t, SaveProfile(configDir, &domain.Profile{Name: "default", GameID: "skyrim-se"}))

	err := DeleteProfile(configDir, "skyrim-se", "default")
	require.NoError(t, err)

	_, err = LoadProfile(configDir, "skyrim-se", "default")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound)
}

func TestDeleteProfile_Missing(t *testing.T) {
	configDir := t.TempDir()

	err := DeleteProfile(configDir, "skyrim-se", "does-not-exist")

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrProfileNotFound)
}

func TestImportProfile_TracksLinkMethodExplicit(t *testing.T) {
	imported, err := ImportProfile([]byte("name: default\ngame_id: skyrim-se\n"))
	require.NoError(t, err)
	assert.False(t, imported.LinkMethodExplicit, "absent link_method must not import as explicit")

	imported, err = ImportProfile([]byte("name: default\ngame_id: skyrim-se\nlink_method: copy\n"))
	require.NoError(t, err)
	assert.True(t, imported.LinkMethodExplicit)
	assert.Equal(t, domain.LinkCopy, imported.LinkMethod)
}

// TestImportProfile_RejectsUnknownLinkMethod pins #172's fail-loud contract
// for imported profiles: a non-empty, unrecognized link_method is a
// load-time error naming the field, offending value, the profile/game, and
// valid options, instead of silently defaulting.
func TestImportProfile_RejectsUnknownLinkMethod(t *testing.T) {
	_, err := ImportProfile([]byte("name: default\ngame_id: skyrim-se\nlink_method: bogus\n"))

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidLinkMethod)
	assert.Contains(t, err.Error(), "default")
	assert.Contains(t, err.Error(), "skyrim-se")
	assert.Contains(t, err.Error(), "link_method")
	assert.Contains(t, err.Error(), "bogus")
}

// TestSaveProfile_PreservesLockedMarker guards that Locked is written to YAML when true,
// omitted when false (omitempty), verified against the raw saved YAML
// (LoadProfile round-trip behavior is covered by the sibling Load tests).
func TestSaveProfile_PreservesLockedMarker(t *testing.T) {
	configDir := t.TempDir()

	// Create and save a profile with one locked mod and one unlocked mod
	profile := &domain.Profile{
		Name:   "default",
		GameID: "skyrim-se",
		Mods: []domain.ModReference{
			{
				SourceID: "nexusmods",
				ModID:    "123",
				Version:  "1.0.0",
				Locked:   true,
			},
			{
				SourceID: "nexusmods",
				ModID:    "456",
				Version:  "2.0.0",
				Locked:   false,
			},
		},
	}

	require.NoError(t, SaveProfile(configDir, profile))

	yaml := profileYAML(t, configDir, "skyrim-se", "default")
	assert.Contains(t, yaml, "locked: true", "locked mod should have locked: true in YAML")

	// Marshal-format-independent assertions (Copilot PR #142 round-3, also
	// flagged by our own T1 review): the previous version of this test
	// scanned for a quoted `mod_id: "456"` line to find the second entry,
	// but yaml.v3 may emit an unquoted scalar instead depending on
	// content, so that scan could silently never match and the test would
	// false-negative - passing even if `locked:` were wrongly written on
	// the unlocked entry. Assert on the raw YAML directly instead: exactly
	// ONE "locked:" occurrence total (the locked mod's own line), and NO
	// "locked: false" anywhere - omitempty must drop the key entirely for
	// the unlocked mod, not merely write it as false. (Sanity check: if
	// omitempty were removed, the unlocked mod would gain its own
	// "locked: false" line, making the count 2 and the NotContains fail -
	// either assertion alone still catches that regression.)
	assert.Equal(t, 1, strings.Count(yaml, "locked:"), "exactly one mod (the locked one) should have a locked key in YAML; omitempty must drop it for the unlocked mod")
	assert.NotContains(t, yaml, "locked: false", "omitempty should drop the key entirely, never write locked: false")
}

// TestLoadProfile_PreservesLockedMarker guards that Locked is read correctly from YAML.
func TestLoadProfile_PreservesLockedMarker(t *testing.T) {
	tests := map[string]struct {
		yaml        string
		wantLocked  bool
		wantVersion string
	}{
		"absent locked key": {"name: default\ngame_id: skyrim-se\nmods:\n  - source_id: nexusmods\n    mod_id: \"123\"\n    version: 1.0.0\n", false, "1.0.0"},
		"locked true":       {"name: default\ngame_id: skyrim-se\nmods:\n  - source_id: nexusmods\n    mod_id: \"123\"\n    version: 1.0.0\n    locked: true\n", true, "1.0.0"},
		"locked false":      {"name: default\ngame_id: skyrim-se\nmods:\n  - source_id: nexusmods\n    mod_id: \"123\"\n    version: 1.0.0\n    locked: false\n", false, "1.0.0"},
	}

	for label, tc := range tests {
		t.Run(label, func(t *testing.T) {
			configDir := t.TempDir()
			profileDir := filepath.Join(configDir, "games", "skyrim-se", "profiles")
			require.NoError(t, os.MkdirAll(profileDir, 0755))
			require.NoError(t, os.WriteFile(filepath.Join(profileDir, "default.yaml"), []byte(tc.yaml), 0644))

			profile, err := LoadProfile(configDir, "skyrim-se", "default")
			require.NoError(t, err)

			require.Len(t, profile.Mods, 1)
			assert.Equal(t, tc.wantLocked, profile.Mods[0].Locked, "locked marker should match")
			assert.Equal(t, tc.wantVersion, profile.Mods[0].Version, "version should match")
		})
	}
}

// TestLoadProfile_RoundTripPreservesLockedMarker guards that a locked mod survives
// load → save → load without modification.
func TestLoadProfile_RoundTripPreservesLockedMarker(t *testing.T) {
	configDir := t.TempDir()
	profile := &domain.Profile{
		Name:   "default",
		GameID: "skyrim-se",
		Mods: []domain.ModReference{
			{
				SourceID: "nexusmods",
				ModID:    "123",
				Version:  "1.0.0",
				Locked:   true,
			},
		},
	}

	require.NoError(t, SaveProfile(configDir, profile))

	loaded, err := LoadProfile(configDir, "skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, loaded.Mods, 1)
	assert.True(t, loaded.Mods[0].Locked, "locked marker should survive round-trip")

	require.NoError(t, SaveProfile(configDir, loaded))

	loaded2, err := LoadProfile(configDir, "skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, loaded2.Mods, 1)
	assert.True(t, loaded2.Mods[0].Locked, "locked marker should survive second round-trip")
}

// TestExportProfile_PreservesLockedMarker guards that Locked survives export.
func TestExportProfile_PreservesLockedMarker(t *testing.T) {
	profile := &domain.Profile{
		Name:   "default",
		GameID: "skyrim-se",
		Mods: []domain.ModReference{
			{
				SourceID: "nexusmods",
				ModID:    "123",
				Version:  "1.0.0",
				Locked:   true,
			},
		},
	}

	data, err := ExportProfile(profile)
	require.NoError(t, err)

	yaml := string(data)
	assert.Contains(t, yaml, "locked: true", "locked marker should be preserved in export")
}

// TestImportProfile_PreservesLockedMarker guards that Locked survives import.
func TestImportProfile_PreservesLockedMarker(t *testing.T) {
	yaml := "name: default\ngame_id: skyrim-se\nmods:\n  - source_id: nexusmods\n    mod_id: \"123\"\n    version: 1.0.0\n    locked: true\n"

	imported, err := ImportProfile([]byte(yaml))
	require.NoError(t, err)

	require.Len(t, imported.Mods, 1)
	assert.True(t, imported.Mods[0].Locked, "locked marker should be preserved on import")
}

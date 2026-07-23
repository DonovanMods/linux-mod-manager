package config

import (
	"io/fs"
	"os"
	"path/filepath"
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

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

// invalidProfileNames are names that must be rejected before any filesystem
// access: they are empty, or would resolve to a path outside the profiles
// directory once joined (filepath.Join collapses ".." segments).
var invalidProfileNames = map[string]string{
	"empty":            "",
	"whitespace only":  "   ",
	"parent traversal": "../evil",
	"deep traversal":   "../../../etc/cron.d/evil",
	"subdirectory":     "a/b",
	"absolute path":    "/etc/evil",
	"bare dotdot":      "..",
	"backslash":        `a\b`,
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
	for label, name := range invalidProfileNames {
		t.Run(label, func(t *testing.T) {
			// configDir is nested so traversal payloads land inside the
			// walkable temp root instead of escaping it.
			tempDir := t.TempDir()
			configDir := filepath.Join(tempDir, "deep", "nested", "config")
			require.NoError(t, os.MkdirAll(configDir, 0755))

			profile := &domain.Profile{Name: name, GameID: "skyrim-se"}
			err := SaveProfile(configDir, profile)

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidProfileName)
			assert.Empty(t, listRegularFiles(t, tempDir), "no file may be written for an invalid profile name")
		})
	}
}

func TestLoadProfile_RejectsInvalidName(t *testing.T) {
	for label, name := range invalidProfileNames {
		t.Run(label, func(t *testing.T) {
			_, err := LoadProfile(t.TempDir(), "skyrim-se", name)

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidProfileName)
		})
	}
}

func TestDeleteProfile_RejectsInvalidName(t *testing.T) {
	for label, name := range invalidProfileNames {
		t.Run(label, func(t *testing.T) {
			// Plant a file where "../evil" would resolve to prove Delete
			// never touches paths outside the profiles directory.
			tempDir := t.TempDir()
			configDir := filepath.Join(tempDir, "config")
			gameDir := filepath.Join(configDir, "games", "skyrim-se")
			require.NoError(t, os.MkdirAll(filepath.Join(gameDir, "profiles"), 0755))
			victim := filepath.Join(gameDir, "evil.yaml")
			require.NoError(t, os.WriteFile(victim, []byte("do not delete"), 0644))

			err := DeleteProfile(configDir, "skyrim-se", name)

			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidProfileName)
			assert.FileExists(t, victim, "delete must not remove files outside the profiles directory")
		})
	}
}

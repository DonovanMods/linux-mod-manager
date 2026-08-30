package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/require"
)

// TestSaveGameWritesLinkMethodAndDeployModeAsPlainStrings is a pin, not a
// bug-fix test: it passes today and is expected to keep passing. It exists
// because LinkMethod and DeployMode gained MarshalText (Task 18), and
// yaml.v3 honours encoding.TextMarshaler - a struct that yaml-marshals
// either domain type directly would silently switch from writing an int to
// writing its name. GameConfig avoids that landmine by using plain string
// fields for link_method/deploy_mode; this test asserts that stays true
// through a real Save/Load round-trip (final review, Important #2 / #282).
func TestSaveGameWritesLinkMethodAndDeployModeAsPlainStrings(t *testing.T) {
	tempDir := t.TempDir()

	game := &domain.Game{
		ID:                 "testgame",
		Name:               "Test Game",
		InstallPath:        "/tmp/testgame",
		ModPath:            "/tmp/testgame/mods",
		LinkMethod:         domain.LinkHardlink,
		LinkMethodExplicit: true,
		DeployMode:         domain.DeployCompile,
	}
	require.NoError(t, SaveGame(tempDir, game))

	data, err := os.ReadFile(filepath.Join(tempDir, "games.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "link_method: hardlink")
	require.Contains(t, string(data), "deploy_mode: compile")

	games, err := LoadGames(tempDir)
	require.NoError(t, err)
	reloaded, ok := games["testgame"]
	require.True(t, ok, "DTO-written games.yaml must read back")
	require.Equal(t, domain.LinkHardlink, reloaded.LinkMethod)
	require.Equal(t, domain.DeployCompile, reloaded.DeployMode)
}

// TestSaveProfileWritesLinkMethodAsPlainString is ProfileConfig's half of
// the same pin - see TestSaveGameWritesLinkMethodAndDeployModeAsPlainStrings.
// UpdatePolicy has no YAML DTO at all today (it's persisted only via
// database/sql in installed_mods, which does not consult TextMarshaler -
// confirmed empirically by the final review's twin-binary DB comparison),
// so there is nothing to pin for it here.
func TestSaveProfileWritesLinkMethodAsPlainString(t *testing.T) {
	tempDir := t.TempDir()

	profile := &domain.Profile{
		Name:               "default",
		GameID:             "testgame",
		LinkMethod:         domain.LinkHardlink,
		LinkMethodExplicit: true,
	}
	require.NoError(t, SaveProfile(tempDir, profile))

	profilePath := filepath.Join(tempDir, "games", "testgame", "profiles", "default.yaml")
	data, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	require.Contains(t, string(data), "link_method: hardlink")

	reloaded, err := LoadProfile(tempDir, "testgame", "default")
	require.NoError(t, err, "DTO-written profile YAML must read back")
	require.Equal(t, domain.LinkHardlink, reloaded.LinkMethod)
}

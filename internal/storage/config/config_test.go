package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_DefaultValues(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(dir)
	require.NoError(t, err)

	assert.Equal(t, domain.LinkSymlink, cfg.DefaultLinkMethod)
	assert.Equal(t, "vim", cfg.Keybindings)
}

func TestLoadConfig_FromFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := `
default_link_method: hardlink
keybindings: standard
`
	err := os.WriteFile(configPath, []byte(content), 0644)
	require.NoError(t, err)

	cfg, err := config.Load(dir)
	require.NoError(t, err)

	assert.Equal(t, domain.LinkHardlink, cfg.DefaultLinkMethod)
	assert.Equal(t, "standard", cfg.Keybindings)
}

func TestLoadGames_Empty(t *testing.T) {
	dir := t.TempDir()
	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Empty(t, games)
}

func TestLoadGames_FromFile(t *testing.T) {
	dir := t.TempDir()
	gamesPath := filepath.Join(dir, "games.yaml")

	content := `
games:
  skyrim-se:
    name: Skyrim Special Edition
    install_path: /games/skyrim
    mod_path: /games/skyrim/Data
    sources:
      nexusmods: skyrimspecialedition
    link_method: symlink
`
	err := os.WriteFile(gamesPath, []byte(content), 0644)
	require.NoError(t, err)

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	require.Len(t, games, 1)

	game := games["skyrim-se"]
	assert.Equal(t, "Skyrim Special Edition", game.Name)
	assert.Equal(t, "/games/skyrim", game.InstallPath)
	assert.Equal(t, "/games/skyrim/Data", game.ModPath)
	assert.Equal(t, "skyrimspecialedition", game.SourceIDs["nexusmods"])
}

func TestSaveGame(t *testing.T) {
	dir := t.TempDir()

	game := &domain.Game{
		ID:          "test-game",
		Name:        "Test Game",
		InstallPath: "/games/test",
		ModPath:     "/games/test/mods",
		SourceIDs:   map[string]string{"nexusmods": "testgame"},
		LinkMethod:  domain.LinkSymlink,
	}

	err := config.SaveGame(dir, game)
	require.NoError(t, err)

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Contains(t, games, "test-game")
}

func TestLoadProfile(t *testing.T) {
	dir := t.TempDir()
	profileDir := filepath.Join(dir, "games", "skyrim-se", "profiles")
	err := os.MkdirAll(profileDir, 0755)
	require.NoError(t, err)

	content := `
name: default
game_id: skyrim-se
mods:
  - source_id: nexusmods
    mod_id: "12345"
    version: "1.0.0"
  - source_id: nexusmods
    mod_id: "67890"
    version: ""
link_method: symlink
`
	err = os.WriteFile(filepath.Join(profileDir, "default.yaml"), []byte(content), 0644)
	require.NoError(t, err)

	profile, err := config.LoadProfile(dir, "skyrim-se", "default")
	require.NoError(t, err)

	assert.Equal(t, "default", profile.Name)
	assert.Equal(t, "skyrim-se", profile.GameID)
	require.Len(t, profile.Mods, 2)
	assert.Equal(t, "12345", profile.Mods[0].ModID)
}

func TestSaveProfile(t *testing.T) {
	dir := t.TempDir()

	profile := &domain.Profile{
		Name:   "test-profile",
		GameID: "skyrim-se",
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "111", Version: "1.0"},
		},
		LinkMethod: domain.LinkSymlink,
	}

	err := config.SaveProfile(dir, profile)
	require.NoError(t, err)

	loaded, err := config.LoadProfile(dir, "skyrim-se", "test-profile")
	require.NoError(t, err)
	assert.Equal(t, profile.Name, loaded.Name)
}

func TestSaveProfile_WithFileIDs(t *testing.T) {
	dir := t.TempDir()

	profile := &domain.Profile{
		Name:   "test-profile",
		GameID: "skyrim-se",
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "111", Version: "1.0", FileIDs: []string{"12345", "67890"}},
			{SourceID: "nexusmods", ModID: "222", Version: "2.0", FileIDs: []string{"99999"}},
		},
		LinkMethod: domain.LinkSymlink,
	}

	err := config.SaveProfile(dir, profile)
	require.NoError(t, err)

	// Read the raw file to verify FileIDs are in the YAML
	profilePath := filepath.Join(dir, "games", "skyrim-se", "profiles", "test-profile.yaml")
	data, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	t.Logf("Raw YAML:\n%s", string(data))

	// Verify file_ids appears in the raw YAML
	assert.Contains(t, string(data), "file_ids:")
	assert.Contains(t, string(data), "12345")
	assert.Contains(t, string(data), "67890")
	assert.Contains(t, string(data), "99999")

	// Load and verify FileIDs are preserved
	loaded, err := config.LoadProfile(dir, "skyrim-se", "test-profile")
	require.NoError(t, err)
	assert.Equal(t, profile.Name, loaded.Name)
	require.Len(t, loaded.Mods, 2)
	assert.Equal(t, []string{"12345", "67890"}, loaded.Mods[0].FileIDs)
	assert.Equal(t, []string{"99999"}, loaded.Mods[1].FileIDs)
}

func TestLoadGames_ExpandsTilde(t *testing.T) {
	dir := t.TempDir()
	gamesPath := filepath.Join(dir, "games.yaml")

	content := `
games:
  test-game:
    name: Test Game
    install_path: ~/games/test
    mod_path: ~/games/test/mods
    sources:
      nexusmods: testgame
`
	err := os.WriteFile(gamesPath, []byte(content), 0644)
	require.NoError(t, err)

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	require.Len(t, games, 1)

	game := games["test-game"]
	home, _ := os.UserHomeDir()

	// Paths should be expanded, not contain literal ~
	assert.NotContains(t, game.InstallPath, "~")
	assert.NotContains(t, game.ModPath, "~")
	assert.Equal(t, filepath.Join(home, "games/test"), game.InstallPath)
	assert.Equal(t, filepath.Join(home, "games/test/mods"), game.ModPath)
}

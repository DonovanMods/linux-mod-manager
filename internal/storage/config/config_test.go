package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

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

// TestLoadConfig_RejectsUnknownDefaultLinkMethod pins #172's fail-loud
// contract: a non-empty, unrecognized default_link_method is a load-time
// error naming the field, offending value, and valid options, instead of
// silently defaulting to symlink.
func TestLoadConfig_RejectsUnknownDefaultLinkMethod(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := "default_link_method: bogus\n"
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0644))

	_, err := config.Load(dir)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidLinkMethod)
	assert.Contains(t, err.Error(), "default_link_method")
	assert.Contains(t, err.Error(), "bogus")
	assert.Contains(t, err.Error(), "symlink")
	assert.Contains(t, err.Error(), "hardlink")
	assert.Contains(t, err.Error(), "copy")
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

// TestLoadGames_RejectsUnknownLinkMethod and TestLoadGames_RejectsUnknownDeployMode
// pin #172's fail-loud contract for games.yaml: a non-empty, unrecognized
// value is a load-time error naming the field, offending value, the game ID,
// and valid options, instead of silently defaulting.
func TestLoadGames_RejectsUnknownLinkMethod(t *testing.T) {
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
    link_method: bogus
`
	require.NoError(t, os.WriteFile(gamesPath, []byte(content), 0644))

	_, err := config.LoadGames(dir)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidLinkMethod)
	assert.Contains(t, err.Error(), "skyrim-se")
	assert.Contains(t, err.Error(), "link_method")
	assert.Contains(t, err.Error(), "bogus")
}

func TestLoadGames_RejectsUnknownDeployMode(t *testing.T) {
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
    deploy_mode: compil
`
	require.NoError(t, os.WriteFile(gamesPath, []byte(content), 0644))

	_, err := config.LoadGames(dir)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidDeployMode)
	assert.Contains(t, err.Error(), "skyrim-se")
	assert.Contains(t, err.Error(), "deploy_mode")
	assert.Contains(t, err.Error(), "compil")
	// Review #172 round 1: the valid-options list had gone stale (pre-rebase
	// "extract, copy" only) and silently dropped "compile" after the Icarus
	// epic merge added it as a real DeployMode value.
	assert.Contains(t, err.Error(), "compile", "valid-options list must include compile")
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

	// Empty hooks must not be written so global config hooks apply
	raw, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "hooks:", "games.yaml must not contain empty hooks so global hooks apply")
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

// TestLoadProfile_RejectsUnknownLinkMethod pins #172's fail-loud contract
// for profile-level link_method: a non-empty, unrecognized value is a
// load-time error naming the field, offending value, the profile/game, and
// valid options.
func TestLoadProfile_RejectsUnknownLinkMethod(t *testing.T) {
	dir := t.TempDir()
	profileDir := filepath.Join(dir, "games", "skyrim-se", "profiles")
	require.NoError(t, os.MkdirAll(profileDir, 0755))

	content := "name: default\ngame_id: skyrim-se\nlink_method: bogus\n"
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "default.yaml"), []byte(content), 0644))

	_, err := config.LoadProfile(dir, "skyrim-se", "default")

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidLinkMethod)
	assert.Contains(t, err.Error(), "default")
	assert.Contains(t, err.Error(), "skyrim-se")
	assert.Contains(t, err.Error(), "link_method")
	assert.Contains(t, err.Error(), "bogus")
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

func TestLoad_HookTimeout(t *testing.T) {
	t.Run("default timeout", func(t *testing.T) {
		tempDir := t.TempDir()
		cfg, err := config.Load(tempDir)
		require.NoError(t, err)
		assert.Equal(t, 60, cfg.HookTimeout)
	})

	t.Run("custom timeout", func(t *testing.T) {
		tempDir := t.TempDir()
		configYAML := `hook_timeout: 120`
		require.NoError(t, os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(configYAML), 0644))

		cfg, err := config.Load(tempDir)
		require.NoError(t, err)
		assert.Equal(t, 120, cfg.HookTimeout)
	})
}

func TestConfigSave_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{
		DefaultLinkMethod: domain.LinkHardlink,
		DefaultGame:       "skyrim-se",
		Keybindings:       "standard",
		CachePath:         "/custom/cache",
		HookTimeout:       120,
	}

	require.NoError(t, cfg.Save(dir))

	loaded, err := config.Load(dir)
	require.NoError(t, err)

	assert.Equal(t, domain.LinkHardlink, loaded.DefaultLinkMethod)
	assert.Equal(t, "skyrim-se", loaded.DefaultGame)
	assert.Equal(t, "standard", loaded.Keybindings)
	assert.Equal(t, "/custom/cache", loaded.CachePath)
	assert.Equal(t, 120, loaded.HookTimeout)
}

// TestConfigSave_DefaultFieldsRoundTrip pins that a zero-value Config still
// round-trips: LinkMethodStr is always populated from String() (LinkSymlink's
// zero value included), so an "empty" config saves and reloads with the
// documented defaults, not with an absent/blank link method.
func TestConfigSave_DefaultFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{}
	require.NoError(t, cfg.Save(dir))

	loaded, err := config.Load(dir)
	require.NoError(t, err)

	assert.Equal(t, domain.LinkSymlink, loaded.DefaultLinkMethod)
	assert.Empty(t, loaded.DefaultGame)
	assert.Empty(t, loaded.Keybindings, "Save does not re-apply Load's default keybindings")
}

func TestConfigSave_WriteError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks, so this EACCES setup can't fail")
	}

	parent := t.TempDir()
	dir := filepath.Join(parent, "cfgdir")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.Chmod(dir, 0500)) // read+exec, no write
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })

	cfg := &config.Config{}
	err := cfg.Save(dir)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing config")
}

func TestDeleteGame(t *testing.T) {
	dir := t.TempDir()

	game := &domain.Game{
		ID:          "test-game",
		Name:        "Test Game",
		InstallPath: "/games/test",
		ModPath:     "/games/test/mods",
	}
	require.NoError(t, config.SaveGame(dir, game))

	require.NoError(t, config.DeleteGame(dir, "test-game"))

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.NotContains(t, games, "test-game")
}

func TestDeleteGame_Missing(t *testing.T) {
	dir := t.TempDir()

	err := config.DeleteGame(dir, "does-not-exist")

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrGameNotFound)
}

// TestDeleteGame_PersistsAfterReload guards against a delete that only
// mutates the in-memory map without writing games.yaml back out.
func TestDeleteGame_PersistsAfterReload(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, config.SaveGame(dir, &domain.Game{ID: "game-a", Name: "A"}))
	require.NoError(t, config.SaveGame(dir, &domain.Game{ID: "game-b", Name: "B"}))

	require.NoError(t, config.DeleteGame(dir, "game-a"))

	// Reload from disk (not the in-process map) to prove the write happened.
	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.NotContains(t, games, "game-a")
	assert.Contains(t, games, "game-b")
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

package core_test

import (
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// game_add_modpath_test.go owns the write side's mod_path rule.
//
// #313 made the LOADER resolve a relative mod_path against install_path and
// had the write side REFUSE one outright, reasoning that "a frontend has no
// reason to send one". #363 reversed that half: the same string now means
// the same thing everywhere - `lmm game add`, `game add --from-detected`,
// POST /api/v1/games and the web form all resolve it the way the loader
// does, and write the resolved absolute path.

// TestAddGame_RelativeModPathResolvesAgainstInstallPath is #363's headline:
// "Data" plus an install path writes "<install>/Data", so every later run -
// from any working directory - reads the same directory. That was the
// property #313 was protecting; resolving at write time keeps it without
// making one games.yaml value mean two things.
func TestAddGame_RelativeModPathResolvesAgainstInstallPath(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()

	entry, err := svc.AddGame(t.Context(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "fakegame", Name: "Fake Game",
		InstallPath: install, ModPath: "Data",
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(install, "Data"), entry.ModPath)

	// The value that LANDS in games.yaml is the resolved one - the whole
	// point of resolving on the write side rather than at every read.
	games, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	require.Contains(t, games, "fakegame")
	assert.Equal(t, filepath.Join(install, "Data"), games["fakegame"].ModPath)
}

// TestAddGame_RelativeModPathAgreesWithTheLoader pins the two against each
// other directly: a divergence here is exactly the bug #363 reported, and
// a nested relative path is the shape real curated games use (Icarus's
// Content/Paks/mods).
func TestAddGame_RelativeModPathAgreesWithTheLoader(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()
	rel := filepath.Join("Icarus", "Content", "Paks", "mods")

	entry, err := svc.AddGame(t.Context(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "fakegame", Name: "Fake Game",
		InstallPath: install, ModPath: rel,
	})
	require.NoError(t, err)

	want, err := config.ResolveModPath(install, rel)
	require.NoError(t, err)
	assert.Equal(t, want, entry.ModPath)
}

// TestAddGame_RelativeModPathWithNoInstallPathIsRefused pins the refusal
// that SURVIVES #363 (review M6): a relative value with nothing to resolve
// it against. install_path is required and validated first, so the refusal
// names install_path - the actual fault - rather than blaming a mod_path
// that would have been fine.
func TestAddGame_RelativeModPathWithNoInstallPathIsRefused(t *testing.T) {
	svc := newGameAddService(t)

	_, err := svc.AddGame(t.Context(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "fakegame", Name: "Fake Game",
		ModPath: "Data",
	})
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "install_path", specErr.Field)
}

// TestAddGame_AbsoluteModPathIsUntouched pins that #363 changed nothing for
// the value every existing caller sends.
func TestAddGame_AbsoluteModPathIsUntouched(t *testing.T) {
	svc := newGameAddService(t)
	install, mods := t.TempDir(), t.TempDir()

	entry, err := svc.AddGame(t.Context(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "fakegame", Name: "Fake Game",
		InstallPath: install, ModPath: mods,
	})
	require.NoError(t, err)
	assert.Equal(t, mods, entry.ModPath)
}

// TestAddGame_TildeModPathIsExpandedNotJoined is the trap #363's resolution
// opens if expansion is skipped: "~/mods" is not absolute, so a naive
// resolve would join it onto the install path and write
// "<install>/~/mods". Both paths are ExpandPath-ed first - the same
// expansion config.LoadGames performs, in the same order - so a "~" typed
// at a prompt (where no shell expanded it) means the home directory here
// too.
func TestAddGame_TildeModPathIsExpandedNotJoined(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()

	entry, err := svc.AddGame(t.Context(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "fakegame", Name: "Fake Game",
		InstallPath: install, ModPath: "~/lmm-modpath-test",
	})
	require.NoError(t, err)
	assert.Equal(t, config.ExpandPath("~/lmm-modpath-test"), entry.ModPath)
	assert.NotContains(t, entry.ModPath, "~", "a tilde must be expanded, never joined onto install_path")
}

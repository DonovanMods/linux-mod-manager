package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// A hand-written games.yaml may carry a mod_path relative to the game's
// install directory ("Data"); before #313 it was used verbatim, so every
// deploy resolved it against the process CWD instead. LoadGames now joins
// a relative value onto install_path.
func TestLoadGamesJoinsRelativeModPathToInstallPath(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "steamapps", "common", "Skyrim")
	yaml := "games:\n  skyrim:\n    name: Skyrim\n    install_path: \"" + install + "\"\n    mod_path: Data\n    sources:\n      nexusmods: skyrimspecialedition\n"
	if err := os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	games, err := config.LoadGames(dir)
	if err != nil {
		t.Fatalf("LoadGames: %v", err)
	}
	got := games["skyrim"].ModPath
	if want := filepath.Join(install, "Data"); got != want {
		t.Errorf("ModPath = %q, want %q", got, want)
	}
}

// An absolute mod_path is untouched, and a "~" one still expands.
func TestLoadGamesKeepsAbsoluteModPath(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "install")
	mods := filepath.Join(dir, "elsewhere", "mods")
	yaml := "games:\n  g:\n    name: G\n    install_path: \"" + install + "\"\n    mod_path: \"" + mods + "\"\n    sources:\n      nexusmods: g\n"
	if err := os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	games, err := config.LoadGames(dir)
	if err != nil {
		t.Fatalf("LoadGames: %v", err)
	}
	if got := games["g"].ModPath; got != mods {
		t.Errorf("ModPath = %q, want %q", got, mods)
	}
}

// lmm itself must never WRITE a relative mod_path: the value it writes is
// the one every later run resolves, and a relative one is CWD-dependent.
func TestSaveGameRefusesRelativeModPath(t *testing.T) {
	dir := t.TempDir()
	err := config.SaveGame(dir, &domain.Game{
		ID:          "g",
		Name:        "G",
		InstallPath: filepath.Join(dir, "install"),
		ModPath:     "Data",
		SourceIDs:   map[string]string{"nexusmods": "g"},
	})
	if err == nil {
		t.Fatal("SaveGame accepted a relative mod_path")
	}
	if !strings.Contains(err.Error(), "mod_path") {
		t.Errorf("error does not name mod_path: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "games.yaml")); statErr == nil {
		t.Error("games.yaml was written despite the refusal")
	}
}

// #313 (review M6): a relative mod_path with no install_path to resolve it
// against is exactly the pre-#313 behaviour - a path resolved against the
// process CWD, a different directory per shell - and it used to pass
// through silently. install_path is documented as required; nothing
// enforced that, so this hole was reachable from the one thing #313 exists
// for, a hand-written file. It is now refused at load, by the same
// ErrRelativeModPath the write side refuses with.
func TestLoadGamesRefusesRelativeModPathWithNoInstallPath(t *testing.T) {
	dir := t.TempDir()
	yaml := "games:\n  g:\n    name: G\n    mod_path: Data\n    sources:\n      nexusmods: g\n"
	if err := os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	games, err := config.LoadGames(dir)
	if err == nil {
		t.Fatalf("LoadGames accepted a CWD-relative mod_path: ModPath = %q", games["g"].ModPath)
	}
	if !errors.Is(err, config.ErrRelativeModPath) {
		t.Errorf("error is not ErrRelativeModPath: %v", err)
	}
	for _, want := range []string{"games.yaml", "mod_path", "install_path", `"g"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

// An empty mod_path with no install_path is not a path at all and stays
// legal: the game simply deploys into its install path (or wherever the
// rest of the config says), which is what every games.yaml without the key
// already means.
func TestLoadGamesAllowsEmptyModPathWithNoInstallPath(t *testing.T) {
	dir := t.TempDir()
	yaml := "games:\n  g:\n    name: G\n    sources:\n      nexusmods: g\n"
	if err := os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	games, err := config.LoadGames(dir)
	if err != nil {
		t.Fatalf("LoadGames: %v", err)
	}
	if got := games["g"].ModPath; got != "" {
		t.Errorf("ModPath = %q, want empty", got)
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadGames_DataDumpPath(t *testing.T) {
	dir := t.TempDir()
	yaml := "games:\n  icarus:\n    name: Icarus\n    install_path: /games/icarus\n" +
		"    mod_path: /games/icarus/mods\n    data_dump_path: /dumps/week243\n"
	if err := os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	games, err := LoadGames(dir)
	if err != nil {
		t.Fatalf("LoadGames: %v", err)
	}
	if got := games["icarus"].BaseDataPath; got != "/dumps/week243" {
		t.Errorf("BaseDataPath = %q, want /dumps/week243", got)
	}
}

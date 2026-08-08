package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertPaksDefaultAndRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	gamesYAML := `games:
    icarus:
        name: Icarus
        install_path: /tmp/icarus
        mod_path: /tmp/icarus/mods
        sources:
            icarus: icarus
        deploy_mode: compile
    explicit-off:
        name: Off
        install_path: /tmp/off
        mod_path: /tmp/off/mods
        sources:
            icarus: icarus
        deploy_mode: compile
        convert_paks: false
`
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "games.yaml"), []byte(gamesYAML), 0644))

	games, err := LoadGames(tempDir)
	require.NoError(t, err)

	if !games["icarus"].ConvertPaks || games["icarus"].ConvertPaksExplicit {
		t.Fatalf("absent convert_paks must default true/implicit, got %+v", games["icarus"])
	}
	if games["explicit-off"].ConvertPaks || !games["explicit-off"].ConvertPaksExplicit {
		t.Fatalf("explicit false must load false/explicit, got %+v", games["explicit-off"])
	}

	// Round-trip: saving the explicit-off game must preserve convert_paks: false.
	g := games["explicit-off"]
	if err := SaveGame(tempDir, g); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := LoadGames(tempDir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded["explicit-off"].ConvertPaks {
		t.Fatal("convert_paks: false lost on save round-trip")
	}
}

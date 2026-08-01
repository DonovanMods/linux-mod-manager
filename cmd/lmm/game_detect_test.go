package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/steam"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestGameFromDetected pins #177's conversion from a steam.DetectedGame to
// the domain.Game runGameDetect saves — table-driven so the untouched-shape
// case (every existing known game: NexusID only, no DeployMode/Sources) and
// the new Icarus-shape case (multi-key Sources map + compile DeployMode) are
// pinned side by side.
func TestGameFromDetected(t *testing.T) {
	tests := []struct {
		name string
		in   steam.DetectedGame
		want *domain.Game
	}{
		{
			name: "NexusMods-only game keeps today's exact shape",
			in: steam.DetectedGame{
				Slug:        "skyrim-se",
				Name:        "Skyrim Special Edition",
				InstallPath: "/games/skyrim",
				ModPath:     "/games/skyrim/Data",
				NexusID:     "skyrimspecialedition",
			},
			want: &domain.Game{
				ID:          "skyrim-se",
				Name:        "Skyrim Special Edition",
				InstallPath: "/games/skyrim",
				ModPath:     "/games/skyrim/Data",
				SourceIDs:   map[string]string{"nexusmods": "skyrimspecialedition"},
				LinkMethod:  domain.LinkSymlink,
				DeployMode:  domain.DeployExtract,
			},
		},
		{
			name: "Icarus: explicit Sources map + compile DeployMode",
			in: steam.DetectedGame{
				Slug:        "icarus",
				Name:        "Icarus",
				InstallPath: "/games/Icarus",
				ModPath:     "/games/Icarus/Icarus/Content/Paks/mods",
				DeployMode:  "compile",
				Sources:     map[string]string{"icarus": "icarus"},
			},
			want: &domain.Game{
				ID:          "icarus",
				Name:        "Icarus",
				InstallPath: "/games/Icarus",
				ModPath:     "/games/Icarus/Icarus/Content/Paks/mods",
				SourceIDs:   map[string]string{"icarus": "icarus"},
				LinkMethod:  domain.LinkSymlink,
				DeployMode:  domain.DeployCompile,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gameFromDetected(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestGameFromDetected_Icarus_ProducesReadmeEquivalentValues proves the
// #177 acceptance criterion directly: saving a detected Icarus produces a
// games.yaml entry whose values match the README's hand-written example
// (the one users no longer need to type themselves) — this asserts the
// parsed field values, not the YAML's byte-for-byte formatting, since only
// the values are actually part of the contract.
func TestGameFromDetected_Icarus_ProducesReadmeEquivalentValues(t *testing.T) {
	dir := t.TempDir()
	detected := steam.DetectedGame{
		Slug:        "icarus",
		Name:        "Icarus",
		InstallPath: "/path/to/Steam/steamapps/common/Icarus",
		ModPath:     "/path/to/Steam/steamapps/common/Icarus/Icarus/Content/Paks/mods",
		DeployMode:  "compile",
		Sources:     map[string]string{"icarus": "icarus"},
	}
	require.NoError(t, config.SaveGame(dir, gameFromDetected(detected)))

	data, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)

	var parsed struct {
		Games map[string]struct {
			Name        string            `yaml:"name"`
			InstallPath string            `yaml:"install_path"`
			ModPath     string            `yaml:"mod_path"`
			Sources     map[string]string `yaml:"sources"`
			DeployMode  string            `yaml:"deploy_mode"`
		} `yaml:"games"`
	}
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	got, ok := parsed.Games["icarus"]
	require.True(t, ok, "games.yaml should have an 'icarus' entry")
	assert.Equal(t, "Icarus", got.Name)
	assert.Equal(t, "/path/to/Steam/steamapps/common/Icarus", got.InstallPath)
	assert.Equal(t, "/path/to/Steam/steamapps/common/Icarus/Icarus/Content/Paks/mods", got.ModPath)
	assert.Equal(t, "compile", got.DeployMode)
	assert.Equal(t, map[string]string{"icarus": "icarus"}, got.Sources)
}

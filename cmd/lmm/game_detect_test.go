package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/steam"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestRunGameDetect_OpensServiceOnFreshInstall guards C1 (#279 Unit B final
// review): runGameDetect used to open its *core.Service via a raw
// core.NewService call, bypassing app.Open's directory preparation. On a
// fresh install - where DataDir does not exist yet - that made 'lmm game
// detect' fail with "unable to open database file", even though it is
// typically the first command a new user runs. configDir/dataDir here point
// at nested paths t.TempDir() never created, reproducing a fresh install;
// HOME is isolated to a fresh temp dir so the test never picks up a real
// Steam library on the machine running it, which would divert the flow into
// reading stdin for a selection.
func TestRunGameDetect_OpensServiceOnFreshInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configDir = filepath.Join(t.TempDir(), "config", "nested")
	dataDir = filepath.Join(t.TempDir(), "data", "nested")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var buf strings.Builder
	cmd.SetOut(&buf)

	err := runGameDetect(cmd, nil)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No moddable Steam games found.")
}

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
			got, err := gameFromDetected(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestGameFromDetected_RejectsUnknownDeployMode pins #172's fail-loud
// contract for the known-games schema's deploy_mode (steam-games.yaml,
// built-in or user override): a non-empty, unrecognized value is a load-time
// error naming the field, the offending value, and the game, instead of
// silently defaulting to extract.
func TestGameFromDetected_RejectsUnknownDeployMode(t *testing.T) {
	_, err := gameFromDetected(steam.DetectedGame{
		Slug:       "icarus",
		Name:       "Icarus",
		DeployMode: "compil",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidDeployMode)
	assert.Contains(t, err.Error(), "icarus")
	assert.Contains(t, err.Error(), "deploy_mode")
	assert.Contains(t, err.Error(), "compil")
}

// TestGameFromDetected_RequiresSourcesOrNexusID guards a Copilot release-
// review finding on #203: a known-games entry with neither Sources NOR a
// non-empty NexusID used to silently produce {"nexusmods": ""} - a garbage
// source mapping (an empty NexusMods game ID) that would propagate into
// games.yaml unnoticed. This is a misconfigured known-games entry (every
// legitimate one sets at least one of the two), so it must fail loud,
// naming the game, instead. Both legs: the failing case, and the two
// success cases (Sources-only, NexusID-only) that must remain unaffected.
func TestGameFromDetected_RequiresSourcesOrNexusID(t *testing.T) {
	t.Run("neither Sources nor NexusID fails loud", func(t *testing.T) {
		_, err := gameFromDetected(steam.DetectedGame{
			Slug: "misconfigured-game",
			Name: "Misconfigured Game",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "misconfigured-game")
	})

	t.Run("NexusID alone is sufficient", func(t *testing.T) {
		game, err := gameFromDetected(steam.DetectedGame{
			Slug:    "skyrim-se",
			Name:    "Skyrim Special Edition",
			NexusID: "skyrimspecialedition",
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, game.SourceIDs)
	})

	t.Run("Sources alone is sufficient", func(t *testing.T) {
		game, err := gameFromDetected(steam.DetectedGame{
			Slug:    "icarus",
			Name:    "Icarus",
			Sources: map[string]string{"icarus": "icarus"},
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"icarus": "icarus"}, game.SourceIDs)
	})

	// #204 round-2 review: YAML unmarshaling an explicit `sources: {}` line
	// produces map[string]string{} (empty, non-nil), which the original
	// `sources == nil` check let slide through, still landing at
	// {"nexusmods": ""} when NexusID is also unset. Empty must be treated
	// identically to nil.
	t.Run("empty-but-non-nil Sources map fails the same as nil", func(t *testing.T) {
		_, err := gameFromDetected(steam.DetectedGame{
			Slug:    "empty-sources-game",
			Name:    "Empty Sources Game",
			Sources: map[string]string{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty-sources-game")
	})
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
	game, err := gameFromDetected(detected)
	require.NoError(t, err)
	require.NoError(t, config.SaveGame(dir, game))

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

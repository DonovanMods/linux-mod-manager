package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestGameFromDetected pins #177's conversion from a domain.DetectedGame to
// the domain.Game ApplyGameDetect saves - table-driven so the
// untouched-shape case (every existing known game: NexusID only, no
// DeployMode/Sources) and the Icarus-shape case (multi-key Sources map +
// compile DeployMode) are pinned side by side. Moved verbatim from
// cmd/lmm's gameFromDetected (v2 Phase 2 Task 21).
func TestGameFromDetected(t *testing.T) {
	tests := []struct {
		name string
		in   domain.DetectedGame
		want *domain.Game
	}{
		{
			name: "NexusMods-only game keeps today's exact shape",
			in: domain.DetectedGame{
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
			in: domain.DetectedGame{
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
			got, err := core.GameFromDetected(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestGameFromDetected_RejectsUnknownDeployMode pins #172's fail-loud
// contract for the known-games schema's deploy_mode (steam-games.yaml,
// built-in or user override): a non-empty, unrecognized value is a
// load-time error naming the field, the offending value, and the game,
// instead of silently defaulting to extract.
func TestGameFromDetected_RejectsUnknownDeployMode(t *testing.T) {
	_, err := core.GameFromDetected(domain.DetectedGame{
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
		_, err := core.GameFromDetected(domain.DetectedGame{
			Slug: "misconfigured-game",
			Name: "Misconfigured Game",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "misconfigured-game")
	})

	t.Run("NexusID alone is sufficient", func(t *testing.T) {
		game, err := core.GameFromDetected(domain.DetectedGame{
			Slug:    "skyrim-se",
			Name:    "Skyrim Special Edition",
			NexusID: "skyrimspecialedition",
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, game.SourceIDs)
	})

	t.Run("Sources alone is sufficient", func(t *testing.T) {
		game, err := core.GameFromDetected(domain.DetectedGame{
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
		_, err := core.GameFromDetected(domain.DetectedGame{
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
// (the one users no longer need to type themselves) - this asserts the
// parsed field values, not the YAML's byte-for-byte formatting, since only
// the values are actually part of the contract.
func TestGameFromDetected_Icarus_ProducesReadmeEquivalentValues(t *testing.T) {
	dir := t.TempDir()
	detected := domain.DetectedGame{
		Slug:        "icarus",
		Name:        "Icarus",
		InstallPath: "/path/to/Steam/steamapps/common/Icarus",
		ModPath:     "/path/to/Steam/steamapps/common/Icarus/Icarus/Content/Paks/mods",
		DeployMode:  "compile",
		Sources:     map[string]string{"icarus": "icarus"},
	}
	game, err := core.GameFromDetected(detected)
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

// newGameDetectTestService builds a real *core.Service (opSem initialized,
// unlike a struct literal) so ApplyGameDetect's beginOp doesn't panic.
func newGameDetectTestService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// TestApplyGameDetect_SavesGamesAndCreatesDefaultProfiles pins the happy
// path: every game is written to games.yaml, gets a fresh "default"
// profile, and both land in the result in input order.
func TestApplyGameDetect_SavesGamesAndCreatesDefaultProfiles(t *testing.T) {
	svc := newGameDetectTestService(t)

	games := []*domain.Game{
		{ID: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data", SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}},
		{ID: "icarus", Name: "Icarus", InstallPath: "/games/Icarus", ModPath: "/games/Icarus/mods", SourceIDs: map[string]string{"icarus": "icarus"}},
	}

	result, err := svc.ApplyGameDetect(context.Background(), games)
	require.NoError(t, err)
	assert.Equal(t, []string{"skyrim-se", "icarus"}, result.Saved)
	assert.Equal(t, []string{"skyrim-se/default", "icarus/default"}, result.Profiles)
	assert.Empty(t, result.Warnings)

	for _, g := range games {
		saved, err := svc.GetGame(g.ID)
		require.NoError(t, err)
		assert.Equal(t, g.Name, saved.Name)

		profile, err := svc.NewProfileManager().Get(g.ID, "default")
		require.NoError(t, err)
		assert.True(t, profile.IsDefault)
		assert.Empty(t, profile.Mods)
	}
}

// TestApplyGameDetect_OverwritesExistingDefaultProfileMods is the core-level
// twin of cmd/lmm's characterization tests
// (TestDoGameDetect_RepairWipesExistingDefaultProfileMods,
// TestDoGameAdd_OverwritesExistingDefaultProfileMods): re-running
// ApplyGameDetect against an already-configured game wipes its default
// profile's mod list, matching the pre-lift unconditional
// config.SaveProfile overwrite exactly.
func TestApplyGameDetect_OverwritesExistingDefaultProfileMods(t *testing.T) {
	svc := newGameDetectTestService(t)
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data", SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}

	_, err := svc.ApplyGameDetect(context.Background(), []*domain.Game{game})
	require.NoError(t, err)
	require.NoError(t, svc.NewProfileManager().UpsertMod(game.ID, "default", domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.0"}))

	before, err := svc.NewProfileManager().Get(game.ID, "default")
	require.NoError(t, err)
	require.NotEmpty(t, before.Mods, "test setup: profile must have a mod before the repair")

	result, err := svc.ApplyGameDetect(context.Background(), []*domain.Game{game})
	require.NoError(t, err)
	assert.Equal(t, []string{"skyrim-se"}, result.Saved)
	assert.Equal(t, []string{"skyrim-se/default"}, result.Profiles)

	after, err := svc.NewProfileManager().Get(game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, after.Mods, "repairing a configured game must wipe its default profile's mod list")
}

// TestApplyGameDetect_StopsAtFirstProfileFailure pins the stop-on-first-
// failure contract mirrored from the pre-lift cmd loop: games.yaml is
// written per game before its profile is (re)created, so a profile failure
// on a later game leaves that game's games.yaml write in place (Saved) but
// its profile step unrecorded (Profiles), and the wrapped error names the
// failing game exactly as doGameDetect's own error text did.
func TestApplyGameDetect_StopsAtFirstProfileFailure(t *testing.T) {
	svc := newGameDetectTestService(t)

	games := []*domain.Game{
		{ID: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data", SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}},
		// A gameID containing a path separator saves fine to games.yaml
		// (config.SaveGame has no ID-format validation) but fails
		// ProfileManager.CreateOrResetDefault's path-segment guard.
		{ID: "bad/game", Name: "Bad Game", InstallPath: "/games/bad", ModPath: "/games/bad/mods", SourceIDs: map[string]string{"nexusmods": "bad"}},
	}

	result, err := svc.ApplyGameDetect(context.Background(), games)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating default profile for bad/game")
	assert.ErrorIs(t, err, domain.ErrInvalidGameID)
	assert.Equal(t, []string{"skyrim-se", "bad/game"}, result.Saved)
	assert.Equal(t, []string{"skyrim-se/default"}, result.Profiles)
}

package core_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
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

	games := []domain.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data", NexusID: "skyrimspecialedition"},
		{Slug: "icarus", Name: "Icarus", InstallPath: "/games/Icarus", ModPath: "/games/Icarus/mods", Sources: map[string]string{"icarus": "icarus"}},
	}

	result, err := svc.ApplyGameDetect(context.Background(), games)
	require.NoError(t, err)
	assert.Equal(t, []string{"skyrim-se", "icarus"}, result.Saved)
	assert.Equal(t, []string{"skyrim-se/default", "icarus/default"}, result.Profiles)
	assert.Empty(t, result.Warnings)

	for _, g := range games {
		saved, err := svc.GetGame(g.Slug)
		require.NoError(t, err)
		assert.Equal(t, g.Name, saved.Name)

		profile, err := svc.NewProfileManager().Get(context.Background(), g.Slug, "default")
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
	game := domain.DetectedGame{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data", NexusID: "skyrimspecialedition"}

	_, err := svc.ApplyGameDetect(context.Background(), []domain.DetectedGame{game})
	require.NoError(t, err)
	require.NoError(t, svc.NewProfileManager().UpsertMod(context.Background(), game.Slug, "default", domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.0"}))

	before, err := svc.NewProfileManager().Get(context.Background(), game.Slug, "default")
	require.NoError(t, err)
	require.NotEmpty(t, before.Mods, "test setup: profile must have a mod before the repair")

	result, err := svc.ApplyGameDetect(context.Background(), []domain.DetectedGame{game})
	require.NoError(t, err)
	assert.Equal(t, []string{"skyrim-se"}, result.Saved)
	assert.Equal(t, []string{"skyrim-se/default"}, result.Profiles)

	after, err := svc.NewProfileManager().Get(context.Background(), game.Slug, "default")
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

	games := []domain.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data", NexusID: "skyrimspecialedition"},
		// A gameID containing a path separator saves fine to games.yaml
		// (config.SaveGame has no ID-format validation) but fails
		// ProfileManager.CreateOrResetDefault's path-segment guard.
		{Slug: "bad/game", Name: "Bad Game", InstallPath: "/games/bad", ModPath: "/games/bad/mods", NexusID: "bad"},
	}

	result, err := svc.ApplyGameDetect(context.Background(), games)
	require.Error(t, err)
	// Exact-match, not Contains: a Contains check on this prefix alone
	// passed both before and after the whole-branch review's Important #1
	// fix (2026-08-29) - CreateOrResetDefault's now-removed inner "saving
	// default profile: " wrap only added a substring in the middle of the
	// message, which Contains couldn't see.
	assert.EqualError(t, err, `creating default profile for bad/game: invalid game ID: "bad/game" must not contain path separators or ".."`)
	assert.ErrorIs(t, err, domain.ErrInvalidGameID)
	assert.Equal(t, []string{"skyrim-se", "bad/game"}, result.Saved)
	assert.Equal(t, []string{"skyrim-se/default"}, result.Profiles)
}

// TestApplyGameDetect_ProfileWriteFailureNotDoubleWrapped pins the
// whole-branch review's Important #1 fix (2026-08-29): a profile *write*
// failure (as opposed to the path-validation failure above) must surface
// exactly the pre-lift text - "creating default profile for <slug>: " (the
// label 'lmm game detect' has always applied, see 'git show
// 9cfaf37:cmd/lmm/game.go') directly wrapping config.SaveProfile's own
// error, with no extra "saving default profile: " segment from
// ProfileManager.CreateOrResetDefault in between. Reproduced the same way
// the review did live on the twin binaries: a read-only <configDir>/games
// directory forces SaveProfile's MkdirAll to fail.
func TestApplyGameDetect_ProfileWriteFailureNotDoubleWrapped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}

	svc := newGameDetectTestService(t)
	gamesDir := filepath.Join(svc.ConfigDir(), "games")
	require.NoError(t, os.MkdirAll(gamesDir, 0755))
	require.NoError(t, os.Chmod(gamesDir, 0555))
	t.Cleanup(func() { _ = os.Chmod(gamesDir, 0755) })

	games := []domain.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data", NexusID: "skyrimspecialedition"},
	}

	result, err := svc.ApplyGameDetect(context.Background(), games)
	require.Error(t, err)
	want := fmt.Sprintf("creating default profile for skyrim-se: creating profiles dir: mkdir %s: permission denied", filepath.Join(gamesDir, "skyrim-se"))
	assert.EqualError(t, err, want)
	assert.Equal(t, []string{"skyrim-se"}, result.Saved)
	assert.Empty(t, result.Profiles)
}

// TestApplyGameDetect_StopsAtFirstConversionFailure pins the same partial-
// persistence contract as the cmd-level regression test
// (TestDoGameDetect_LaterConversionFailureLeavesEarlierGamesPersisted in
// cmd/lmm): converting each detected game happens per game, right before
// that game's own save, not for the whole batch up front - so a later
// game's conversion failure (e.g. an unrecognized deploy_mode) must not
// undo or block persistence of every earlier game that already converted
// and saved cleanly (Task 21 review Important #1, 2026-08-28).
func TestApplyGameDetect_StopsAtFirstConversionFailure(t *testing.T) {
	svc := newGameDetectTestService(t)

	games := []domain.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data", NexusID: "skyrimspecialedition"},
		{Slug: "bad-game", Name: "Bad Game", DeployMode: "bogus"},
	}

	result, err := svc.ApplyGameDetect(context.Background(), games)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "converting detected game bad-game")
	assert.ErrorIs(t, err, domain.ErrInvalidDeployMode)
	assert.Equal(t, []string{"skyrim-se"}, result.Saved)
	assert.Equal(t, []string{"skyrim-se/default"}, result.Profiles)

	saved, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, "Skyrim Special Edition", saved.Name)

	_, err = svc.GetGame("bad-game")
	assert.Error(t, err, "a game that failed conversion must not be persisted")
}

// TestApplyGameDetect_CancellationBetweenGameSaveAndDefaultProfileCreate is
// the class-(A) shape test for the task 18 re-review round-2 NEW-4 finding:
// ApplyGameDetect saves the game to games.yaml (a committed write) BEFORE
// creating its default profile, so a cancellation landing in that window is
// a class-(A) completing write like every other Ruling 16 (A) site - the
// profile creation must still finish, not be refused by
// CreateOrResetDefault's own ctx.Err() guard, leaving a configured game with
// no default profile.
//
// Against a version of CreateOrResetDefaultAfterGameSave that just calls
// CreateOrResetDefault directly (no completeProfileWrite), this fails at the
// profile existence check below: the guard fires before config.SaveProfile
// ever runs, so "skyrim-se"'s default profile is never written.
func TestApplyGameDetect_CancellationBetweenGameSaveAndDefaultProfileCreate(t *testing.T) {
	svc := newGameDetectTestService(t)
	game := domain.DetectedGame{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data", NexusID: "skyrimspecialedition"}

	inner, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &cancelAtCompletingProfileWrite{Context: inner, cancel: cancel}

	result, err := svc.ApplyGameDetect(ctx, []domain.DetectedGame{game})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "the run must end with the cancellation, not absorb it")
	require.True(t, ctx.fired.Load(), "the cancellation must have landed on the completing profile write")
	assert.Equal(t, []string{"skyrim-se"}, result.Saved)
	assert.Empty(t, result.Profiles, "the profile step must not be recorded past the cancellation")

	saved, err := svc.GetGame("skyrim-se")
	require.NoError(t, err, "the game save committed before the cancellation")
	assert.Equal(t, "Skyrim Special Edition", saved.Name)

	profile, err := svc.NewProfileManager().Get(context.Background(), "skyrim-se", "default")
	require.NoError(t, err, "Ruling 16 (A): the default profile must exist even though the run was cancelled")
	assert.True(t, profile.IsDefault)
}

// TestGameDetectListing_WorkshopBearingUncuratedRowsAreListedByDefault is
// #368's headline, one layer below either frontend: the two games Tier 1
// exists for were hidden behind --include-unknown, because the default
// listing filtered on Known alone. A row with Workshop items already
// downloaded is now listed by default; a plain uncurated row still is not,
// and neither gains an index - listed is not selectable, and the numbering
// a selection uses must not shift.
func TestGameDetectListing_WorkshopBearingUncuratedRowsAreListedByDefault(t *testing.T) {
	svc := newGameAddService(t)
	scan := []domain.DetectedGame{
		{SteamAppID: "489830", Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", Known: true},
		{SteamAppID: "1133870", Slug: "space-engineers-2", Name: "Space Engineers 2", InstallPath: "/games/se2",
			Sources: map[string]string{"steamworkshop": "1133870"}, WorkshopItems: 30},
		{SteamAppID: "526870", Slug: "satisfactory", Name: "Satisfactory", InstallPath: "/games/sf"},
	}

	listing, err := svc.GameDetectListing(context.Background(), scan, nil, core.GameDetectListingOptions{})
	require.NoError(t, err)
	require.Len(t, listing.Games, 2, "the Workshop-bearing row joins the default listing; the plain uncurated one does not")
	assert.Equal(t, "skyrim-se", listing.Games[0].Slug)
	assert.Equal(t, 1, listing.Games[0].Index)
	workshop := listing.Games[1]
	assert.Equal(t, "space-engineers-2", workshop.Slug)
	assert.Equal(t, 30, workshop.WorkshopItems)
	assert.False(t, workshop.Known)
	assert.Equal(t, 0, workshop.Index, "an uncurated row is listed, not selectable by index")

	all, err := svc.GameDetectListing(context.Background(), scan, nil, core.GameDetectListingOptions{IncludeUnknown: true})
	require.NoError(t, err)
	require.Len(t, all.Games, 3, "--include-unknown still adds everything else")
	assert.Equal(t, 1, all.Games[0].Index, "known numbering must not shift when the wider rows join")
}

// #368 review Minor 8: the two-path apply a detect selection needs since
// #368 - a CURATED row configured from its known-games entry, an UNCURATED
// one added the way `lmm game add --from-detected` adds it - lived in the
// CLI, so the web's detect selection could not do what the CLI's prompt
// does, one user action took N+1 mutation slots instead of one, and the
// GameDetectResult it reported was assembled by hand. These tests are the
// CLI's own, moved down to the seam both frontends now call.

// detectSelectionService is a Service with the source ids these rows map
// registered - AddGame refuses an unregistered one - plus real directories,
// since AddGame validates that an install path exists.
func detectSelectionService(t *testing.T) *core.Service {
	t.Helper()
	svc := newGameAddService(t)
	svc.RegisterSource(&catalogLessSource{id: "steamworkshop", name: "Steam Workshop"})
	return svc
}

// TestApplyDetectSelection_ConfiguresBothKindsOfRowUnderOneSlot pins the
// shape of the selection: curated rows first (their repair semantics), then
// uncurated ones through the from-detected prefill, with the result naming
// every game and profile in that order.
func TestApplyDetectSelection_ConfiguresBothKindsOfRowUnderOneSlot(t *testing.T) {
	svc := detectSelectionService(t)
	// One directory per app, as Steam itself installs them: since #406
	// review F1 an install path games.yaml already covers IS that game, so
	// two rows sharing one path would be one game, not two.
	install := t.TempDir()
	se2Install := t.TempDir()
	curated := domain.DetectedGame{
		SteamAppID: "489830", Slug: "skyrim-se", Name: "Skyrim Special Edition",
		InstallPath: install, ModPath: filepath.Join(install, "Data"),
		NexusID: "skyrimspecialedition", Known: true,
	}
	uncurated := domain.DetectedGame{
		SteamAppID: "1133870", Slug: "space-engineers-2", Name: "Space Engineers 2",
		InstallPath: se2Install, Sources: map[string]string{"steamworkshop": "1133870"}, WorkshopItems: 30,
	}

	// Typed uncurated-first: the apply reorders to curated-first, and says so
	// through the rows it returns, so a caller can name each added game
	// beside its result row.
	applied, result, err := svc.ApplyDetectSelection(context.Background(),
		[]domain.DetectedGame{uncurated, curated})
	require.NoError(t, err)
	assert.Equal(t, []string{"skyrim-se", "space-engineers-2"}, result.Saved)
	assert.Equal(t, []string{"skyrim-se/default", "space-engineers-2/default"}, result.Profiles)
	require.Len(t, applied, 2)
	assert.Equal(t, []string{"skyrim-se", "space-engineers-2"},
		[]string{applied[0].Slug, applied[1].Slug})

	saved, err := svc.GetGame("space-engineers-2")
	require.NoError(t, err)
	assert.Equal(t, "Space Engineers 2", saved.Name)
	assert.Equal(t, map[string]string{"steamworkshop": "1133870"}, saved.SourceIDs)
	assert.Equal(t, filepath.Join(se2Install, "mods"), saved.ModPath,
		"an uncurated candidate has no curated mod path, so GameSpecFromDetected's <install>/mods default applies")

	profile, err := svc.NewProfileManager().Get(context.Background(), "space-engineers-2", "default")
	require.NoError(t, err)
	assert.True(t, profile.IsDefault)
}

// TestApplyDetectSelection_CuratedRowRepairsAndUncuratedRowRefuses pins the
// one deliberate asymmetry: naming an already-configured CURATED row is the
// documented repair (it overwrites), while an uncurated one has no curated
// entry to repair FROM, so it is refused with ErrGameExists rather than
// destroying an existing game's default profile from a surface that says
// "add".
func TestApplyDetectSelection_CuratedRowRepairsAndUncuratedRowRefuses(t *testing.T) {
	svc := detectSelectionService(t)
	install := t.TempDir()
	curated := domain.DetectedGame{
		Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: install,
		ModPath: filepath.Join(install, "Data"), NexusID: "skyrimspecialedition", Known: true,
	}
	// Its own directory, as Steam installs it - see the sibling test.
	uncurated := domain.DetectedGame{
		SteamAppID: "1133870", Slug: "space-engineers-2", Name: "Space Engineers 2",
		InstallPath: t.TempDir(), Sources: map[string]string{"steamworkshop": "1133870"}, WorkshopItems: 30,
	}

	_, _, err := svc.ApplyDetectSelection(context.Background(), []domain.DetectedGame{curated, uncurated})
	require.NoError(t, err)
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(context.Background(), "space-engineers-2", "default",
		domain.ModReference{SourceID: "steamworkshop", ModID: "42", Version: "1"}))

	applied, result, err := svc.ApplyDetectSelection(context.Background(),
		[]domain.DetectedGame{curated, uncurated})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrGameExists)
	assert.Contains(t, err.Error(), "space-engineers-2")
	assert.Equal(t, []string{"skyrim-se"}, result.Saved, "the curated row's repair still happened")
	require.Len(t, applied, 2, "both attempted rows are reported, curated first")

	after, err := pm.Get(context.Background(), "space-engineers-2", "default")
	require.NoError(t, err)
	assert.Len(t, after.Mods, 1, "the refused row's default profile is untouched")
}

// TestApplyDetectSelection_UncuratedRowWithNoSourceIsRefused: nothing tells
// lmm where such a game keeps its mods, so there is nothing to write - the
// same refusal GameSpec makes for a game with no source at all.
func TestApplyDetectSelection_UncuratedRowWithNoSourceIsRefused(t *testing.T) {
	svc := detectSelectionService(t)

	_, result, err := svc.ApplyDetectSelection(context.Background(), []domain.DetectedGame{
		{SteamAppID: "526870", Slug: "satisfactory", Name: "Satisfactory", InstallPath: t.TempDir()},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "satisfactory")
	assert.Empty(t, result.Saved)
}

// TestSelectDetectedGames_AcceptsAnAddableUncuratedRowBySlug: the selection
// rule and the apply have to agree about what is selectable, or the shared
// apply is unreachable from the web for exactly the rows #368 added. An
// uncurated row detection prefilled a source map for is addable; one with
// nothing is still ErrUnknownDetectedGame.
func TestSelectDetectedGames_AcceptsAnAddableUncuratedRowBySlug(t *testing.T) {
	scan := []domain.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition", Known: true},
		{SteamAppID: "1133870", Slug: "space-engineers-2", Name: "Space Engineers 2", InstallPath: "/games/se2",
			Sources: map[string]string{"steamworkshop": "1133870"}, WorkshopItems: 30},
		{SteamAppID: "526870", Slug: "satisfactory", Name: "Satisfactory", InstallPath: "/games/sf"},
	}

	selected, err := core.SelectDetectedGames(scan, []string{"space-engineers-2"})
	require.NoError(t, err)
	require.Len(t, selected, 1)
	assert.Equal(t, "space-engineers-2", selected[0].Slug)

	_, err = core.SelectDetectedGames(scan, []string{"satisfactory"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrUnknownDetectedGame)

	byIndex, err := core.SelectDetectedGames(scan, []string{"1"})
	require.NoError(t, err)
	require.Len(t, byIndex, 1)
	assert.Equal(t, "skyrim-se", byIndex[0].Slug, "an index still counts the known rows only")
}

// TestSelectDetectedGames_RefusesASelectorThatIsBothAnIndexAndASlug is #368
// re-review N4, the CLI's Important 1 one layer down: the selector resolved
// a number as an index FIRST and only fell through to the slug map when
// strconv failed, so on a listing whose rows include a game slugged "2",
// selecting that game by its slug silently configured known row 2 instead.
// Uncurated rows reach this path since #368 and their slugs are machine-
// derived from the Steam title, so an all-digits slug is reachable.
//
// It is refused rather than guessed, and both explicit spellings resolve:
// "#2" is always the index, "slug:2" always the slug.
func TestSelectDetectedGames_RefusesASelectorThatIsBothAnIndexAndASlug(t *testing.T) {
	scan := []domain.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition", Known: true},
		{Slug: "fallout-4", Name: "Fallout 4", InstallPath: "/games/fo4", NexusID: "fallout4", Known: true},
		{SteamAppID: "1133870", Slug: "2", Name: "2", InstallPath: "/games/two",
			Sources: map[string]string{"steamworkshop": "1133870"}, WorkshopItems: 3},
	}

	_, err := core.SelectDetectedGames(scan, []string{"2"})
	require.Error(t, err, "a bare \"2\" is row 2 AND the slug of another row")
	assert.Contains(t, err.Error(), "ambiguous selection")
	assert.Contains(t, err.Error(), `"#2"`)
	assert.Contains(t, err.Error(), `"slug:2"`)

	byIndex, err := core.SelectDetectedGames(scan, []string{"#2"})
	require.NoError(t, err)
	require.Len(t, byIndex, 1)
	assert.Equal(t, "fallout-4", byIndex[0].Slug)

	bySlug, err := core.SelectDetectedGames(scan, []string{"slug:2"})
	require.NoError(t, err)
	require.Len(t, bySlug, 1)
	assert.Equal(t, "2", bySlug[0].Slug)
}

// TestSelectDetectedGames_UnambiguousSelectorsAreUnaffected pins that the
// refusal above is narrow: an index no row claims as a slug still resolves,
// a slug no index can be still resolves, and the explicit forms work on a
// listing with no collision at all.
func TestSelectDetectedGames_UnambiguousSelectorsAreUnaffected(t *testing.T) {
	scan := []domain.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition", Known: true},
		{SteamAppID: "1133870", Slug: "space-engineers-2", Name: "Space Engineers 2", InstallPath: "/games/se2",
			Sources: map[string]string{"steamworkshop": "1133870"}, WorkshopItems: 30},
	}

	for _, tc := range []struct{ sel, want string }{
		{"1", "skyrim-se"},
		{"#1", "skyrim-se"},
		{"space-engineers-2", "space-engineers-2"},
		{"slug:space-engineers-2", "space-engineers-2"},
		{"SPACE-ENGINEERS-2", "space-engineers-2"},
		{"slug:SPACE-ENGINEERS-2", "space-engineers-2"},
	} {
		got, err := core.SelectDetectedGames(scan, []string{tc.sel})
		require.NoError(t, err, "selector %q", tc.sel)
		require.Len(t, got, 1)
		assert.Equal(t, tc.want, got[0].Slug, "selector %q", tc.sel)
	}

	_, err := core.SelectDetectedGames(scan, []string{"#9"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid selection")

	_, err = core.SelectDetectedGames(scan, []string{"slug:nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid selection")
}

// --- #406 review F1: a curation wave must not orphan the game the user
// already added under the slug detection used to derive ---

// preCuratedGame writes the games.yaml entry a user got by running `lmm
// game detect` BEFORE the game was curated: the id steam.deriveSlug
// produced from the Steam title, pointing at the real install path.
func preCuratedGame(t *testing.T, svc *core.Service, id, name, install string) {
	t.Helper()
	_, err := svc.AddGame(context.Background(), core.GameSpec{
		ID: id, Name: name, InstallPath: install,
		SourceID: "nexusmods", Identifier: id,
	})
	require.NoError(t, err)
}

// TestGameDetectListing_ExistingGameAtTheSameInstallPathIsConfigured pins
// #406 review F1: the curated slug for six of #406's games differs from the
// one detection derived from the Steam title before they were curated, so a
// user who had already added one of them was offered it again as a fresh
// add - and taking it wrote a SECOND games.yaml game at the same install
// path, with the old game's profiles, mods and deployed links still on the
// old id. Two of the six stand for the set.
func TestGameDetectListing_ExistingGameAtTheSameInstallPathIsConfigured(t *testing.T) {
	tests := []struct {
		name        string
		derivedID   string
		curatedSlug string
		modPath     string
	}{
		{name: "Cyberpunk 2077", derivedID: "cyberpunk-2077", curatedSlug: "cyberpunk2077"},
		{name: "The Planet Crafter", derivedID: "the-planet-crafter", curatedSlug: "planet-crafter", modPath: "BepInEx/plugins"},
	}
	for _, tc := range tests {
		t.Run(tc.curatedSlug, func(t *testing.T) {
			svc := newGameAddService(t)
			install := t.TempDir()
			preCuratedGame(t, svc, tc.derivedID, tc.name, install)

			listing, err := svc.GameDetectListing(context.Background(), []domain.DetectedGame{{
				SteamAppID: "1091500", Slug: tc.curatedSlug, Name: tc.name,
				InstallPath: install, ModPath: filepath.Join(install, tc.modPath),
				NexusID: tc.curatedSlug, Known: true,
			}}, nil, core.GameDetectListingOptions{})
			require.NoError(t, err)

			require.Len(t, listing.Games, 1)
			assert.True(t, listing.Games[0].AlreadyConfigured,
				"games.yaml already holds %q for this install path; the curated slug %q is the same game",
				tc.derivedID, tc.curatedSlug)
		})
	}
}

// TestApplyGameDetect_RepairsTheGameAlreadyConfiguredAtTheSameInstallPath is
// F1's other half: recognising the row is only useful if selecting it
// REPAIRS the game the user has (its existing id, its profiles) instead of
// writing a duplicate beside it.
func TestApplyGameDetect_RepairsTheGameAlreadyConfiguredAtTheSameInstallPath(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()
	preCuratedGame(t, svc, "cyberpunk-2077", "Cyberpunk 2077", install)

	result, err := svc.ApplyGameDetect(context.Background(), []domain.DetectedGame{{
		SteamAppID: "1091500", Slug: "cyberpunk2077", Name: "Cyberpunk 2077",
		InstallPath: install, ModPath: install, NexusID: "cyberpunk2077", Known: true,
	}})
	require.NoError(t, err)
	assert.Equal(t, []string{"cyberpunk-2077"}, result.Saved, "the repair keeps the id the user's profiles hang off")
	assert.Equal(t, []string{"cyberpunk-2077/default"}, result.Profiles)

	saved, err := svc.LoadGamesFromDisk()
	require.NoError(t, err)
	assert.NotContains(t, saved, "cyberpunk2077",
		"a curation wave must never leave two games pointing at one install path")
	require.Contains(t, saved, "cyberpunk-2077")
	assert.Equal(t, map[string]string{"nexusmods": "cyberpunk2077"}, saved["cyberpunk-2077"].SourceIDs,
		"the repair still applies the curated prefill")
	assert.Equal(t, install, saved["cyberpunk-2077"].ModPath)
}

// TestPrefillGameSpecFromDetected_KeepsTheConfiguredGamesID is the
// `lmm game add --from-detected` half of F1: the prefill resolves to the id
// games.yaml already uses for that install path, so AddGame refuses the
// duplicate (ErrGameExists) instead of creating one. An explicit --game-id
// still wins - a manual add naming its own id is left alone.
func TestPrefillGameSpecFromDetected_KeepsTheConfiguredGamesID(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()
	preCuratedGame(t, svc, "no-man-s-sky", "No Man's Sky", install)
	detected := domain.DetectedGame{
		SteamAppID: "275850", Slug: "no-mans-sky", Name: "No Man's Sky",
		InstallPath: install, ModPath: filepath.Join(install, "GAMEDATA", "MODS"),
		NexusID: "nomanssky", Known: true,
	}

	spec, err := svc.PrefillGameSpecFromDetected(detected, core.GameSpec{})
	require.NoError(t, err)
	assert.Equal(t, "no-man-s-sky", spec.ID)

	_, err = svc.AddGame(context.Background(), spec)
	require.ErrorIs(t, err, core.ErrGameExists)
	assert.Contains(t, err.Error(), "no-man-s-sky")

	explicit, err := svc.PrefillGameSpecFromDetected(detected, core.GameSpec{ID: "my-nms"})
	require.NoError(t, err)
	assert.Equal(t, "my-nms", explicit.ID, "an explicitly named id is the user's, not detection's")
}

// TestApplyGameDetect_RepairKeepsTheFieldsDetectionDoesNotOwn is the
// re-review's M1. `config.SaveGame` does `games[game.ID] = game` - a full
// replacement - and `GameFromDetected` builds a fresh domain.Game carrying
// only the curated fields, so repairing a game silently dropped its
// link_method, cache_path, deploy_mode/convert_paks and both hooks blocks.
// Pre-existing for an id match; F1 widened it to path matches, which is why
// it is worth pinning here rather than leaving to the day someone notices
// their hooks stopped running.
//
// The repair now edits the game the user has instead of replacing it: it
// changes the paths detection found and adds the curated sources, and every
// other field is theirs.
func TestApplyGameDetect_RepairKeepsTheFieldsDetectionDoesNotOwn(t *testing.T) {
	configDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	install := t.TempDir()
	customised := &domain.Game{
		ID:          "cyberpunk-2077",
		Name:        "Cyberpunk (my copy)",
		InstallPath: install,
		ModPath:     filepath.Join(install, "mods"),
		SourceIDs:   map[string]string{"nexusmods": "cyberpunk2077", "curseforge": "7777"},
		// Everything below is the user's, and none of it is anything
		// detection knows or could know.
		LinkMethod:          domain.LinkHardlink,
		LinkMethodExplicit:  true,
		CachePath:           filepath.Join(t.TempDir(), "cyberpunk-cache"),
		DeployMode:          domain.DeployCopy,
		ConvertPaks:         false,
		ConvertPaksExplicit: true,
		Hooks: domain.GameHooks{
			Install:   domain.HookConfig{BeforeAll: "/bin/true", BeforeEach: "/bin/true", AfterEach: "/bin/true", AfterAll: "/bin/true"},
			Uninstall: domain.HookConfig{BeforeAll: "/bin/true", BeforeEach: "/bin/true", AfterEach: "/bin/true", AfterAll: "/bin/true"},
		},
	}
	require.NoError(t, config.SaveGame(configDir, customised))

	result, err := svc.ApplyGameDetect(context.Background(), []domain.DetectedGame{{
		SteamAppID: "1091500", Slug: "cyberpunk2077", Name: "Cyberpunk 2077",
		InstallPath: install, ModPath: install, NexusID: "cyberpunk2077", Known: true,
	}})
	require.NoError(t, err)
	require.Equal(t, []string{"cyberpunk-2077"}, result.Saved)

	saved, err := svc.LoadGamesFromDisk()
	require.NoError(t, err)
	require.Contains(t, saved, "cyberpunk-2077")
	got := saved["cyberpunk-2077"]

	// What detection owns.
	assert.Equal(t, install, got.InstallPath)
	assert.Equal(t, install, got.ModPath, "the curated mod path is the point of the repair")
	assert.Equal(t, "cyberpunk2077", got.SourceIDs["nexusmods"], "the curated source mapping is applied")

	// What it does not.
	assert.Equal(t, "7777", got.SourceIDs["curseforge"], "a source the user added is not detection's to drop")
	assert.Equal(t, "Cyberpunk (my copy)", got.Name)
	assert.Equal(t, domain.LinkHardlink, got.LinkMethod)
	assert.True(t, got.LinkMethodExplicit)
	assert.Equal(t, customised.CachePath, got.CachePath)
	assert.Equal(t, domain.DeployCopy, got.DeployMode)
	assert.False(t, got.ConvertPaks)
	assert.True(t, got.ConvertPaksExplicit)
	assert.Equal(t, customised.Hooks, got.Hooks)
}

// TestGameDetectListing_ExistingGameUnderASymlinkedLibraryIsConfigured is
// the re-review's M2. F1's path match used filepath.Clean, which is purely
// lexical, so a games.yaml entry recorded through a symlinked Steam library
// root (~/Games -> /mnt/ssd/Games, an ordinary second-drive setup) did not
// match the resolved path detection reports, and the duplicate F1 exists to
// prevent came straight back for exactly the users most likely to hit it.
func TestGameDetectListing_ExistingGameUnderASymlinkedLibraryIsConfigured(t *testing.T) {
	svc := newGameAddService(t)

	realLibrary := t.TempDir()
	install := filepath.Join(realLibrary, "Cyberpunk 2077")
	require.NoError(t, os.MkdirAll(install, 0o755))
	// The user's second drive, reached through a symlink in $HOME.
	linkedLibrary := filepath.Join(t.TempDir(), "Games")
	require.NoError(t, os.Symlink(realLibrary, linkedLibrary))

	// games.yaml holds the path as the user typed it, through the symlink.
	preCuratedGame(t, svc, "cyberpunk-2077", "Cyberpunk 2077", filepath.Join(linkedLibrary, "Cyberpunk 2077"))

	// Detection reports the resolved path Steam's own scan walked to.
	listing, err := svc.GameDetectListing(context.Background(), []domain.DetectedGame{{
		SteamAppID: "1091500", Slug: "cyberpunk2077", Name: "Cyberpunk 2077",
		InstallPath: install, ModPath: install, NexusID: "cyberpunk2077", Known: true,
	}}, nil, core.GameDetectListingOptions{})
	require.NoError(t, err)
	require.Len(t, listing.Games, 1)
	assert.True(t, listing.Games[0].AlreadyConfigured,
		"one installed directory is one game however the path spells it")
}

// TestConfiguredGameFor_FallsBackToCleanWhenAPathIsGone: EvalSymlinks
// errors on a path that no longer exists, and a game whose install
// directory the user has since deleted or moved must still be recognised
// as configured - otherwise a detect run offers a duplicate the moment the
// drive is unplugged.
func TestConfiguredGameFor_FallsBackToCleanWhenAPathIsGone(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "unplugged", "Cyberpunk 2077")
	existing := map[string]*domain.Game{
		"cyberpunk-2077": {ID: "cyberpunk-2077", InstallPath: gone + string(filepath.Separator)},
	}

	match := core.ConfiguredGameFor(existing, domain.DetectedGame{
		Slug: "cyberpunk2077", InstallPath: gone,
	})
	require.NotNil(t, match, "a path neither side can resolve still compares lexically")
	assert.Equal(t, "cyberpunk-2077", match.ID)
}

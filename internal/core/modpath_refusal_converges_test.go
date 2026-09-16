package core_test

// The mod_path refusal (#427) is only as good as the commands it names: a
// user who runs exactly those, in its order, must end with the move
// allowed. The #445 audit found three states where they never cleared it
// - each purge kept a record the next one could not clear - so each state
// here runs the refusal's own commands, parsed from its text, for at most
// three rounds, then the move and the deploy it names after the move, and
// checks that every file the active profile lists is live under the new
// mod_path.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var refusalCommand = regexp.MustCompile("`(lmm [^`]+)`")

// runRefusalCommand runs one command the refusal names through core, the
// way the CLI runs it.
func runRefusalCommand(t *testing.T, svc *core.Service, gameID, command string) {
	t.Helper()
	ctx := context.Background()
	game, err := svc.GetGame(gameID)
	require.NoError(t, err)
	fields := strings.Fields(command)
	flag := func(name string) string {
		i := slices.Index(fields, name)
		require.GreaterOrEqual(t, i, 0, "%q has no %s", command, name)
		return fields[i+1]
	}
	require.Equal(t, gameID, flag("--game"), command)
	switch {
	case strings.HasPrefix(command, "lmm purge "):
		plan, err := svc.PlanPurge(ctx, game, flag("--profile"), core.PurgeOptions{})
		require.NoError(t, err, command)
		_, err = svc.ApplyPurge(ctx, game, plan, core.PurgeOptions{}, nil)
		require.NoError(t, err, command)
	case strings.HasPrefix(command, "lmm profile apply "):
		plan, err := svc.PlanProfileApply(ctx, game, fields[3])
		require.NoError(t, err, command)
		result, err := svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
		require.NoError(t, err, command)
		require.Empty(t, result.Failed, command)
	case command == "lmm deploy --game "+gameID:
		active, err := svc.NewProfileManager().GetDefault(ctx, gameID)
		require.NoError(t, err)
		result, err := svc.DeployProfile(ctx, game, active.Name, core.DeployOptions{}, nil)
		require.NoError(t, err, command)
		require.Empty(t, result.Skipped, command)
	default:
		t.Fatalf("the refusal names a command this test does not run: %q", command)
	}
}

// followModPathRefusal moves gameID's mod_path to to, running what each
// refusal names first, for at most three rounds; then it runs what the last
// refusal names for after the move. It returns every refusal's text.
func followModPathRefusal(t *testing.T, svc *core.Service, gameID, to string) []string {
	t.Helper()
	ctx := context.Background()
	var refusals []string
	var after []string
	for range 3 {
		_, err := svc.SetGameModPath(ctx, gameID, to)
		var inUse *core.GameModPathInUseError
		if !errors.As(err, &inUse) {
			require.NoError(t, err, "after %d refusal(s): %v", len(refusals), refusals)
			for _, command := range after {
				runRefusalCommand(t, svc, gameID, command)
			}
			return refusals
		}
		text := err.Error()
		refusals = append(refusals, text)
		before, rest, found := strings.Cut(text, "then change the mod_path")
		require.True(t, found, text)
		for _, m := range refusalCommand.FindAllStringSubmatch(before, -1) {
			runRefusalCommand(t, svc, gameID, m[1])
		}
		// After the move: the deploy, not the "when you next switch" hints.
		after = after[:0]
		afterMove, _, _ := strings.Cut(rest, "; profile ")
		for _, m := range refusalCommand.FindAllStringSubmatch(afterMove, -1) {
			after = append(after, m[1])
		}
	}
	t.Fatalf("the mod_path move is still refused after 3 rounds of the commands it names:\n%s", strings.Join(refusals, "\n"))
	return nil
}

// requireActiveListedLive checks that every mod gameID's active profile
// lists and enables is deployed under its (new) mod_path: each cached file
// is there, and recorded under the active profile.
func requireActiveListedLive(t *testing.T, svc *core.Service, gameID string) {
	t.Helper()
	ctx := context.Background()
	game, err := svc.GetGame(gameID)
	require.NoError(t, err)
	active, err := svc.NewProfileManager().GetDefault(ctx, gameID)
	require.NoError(t, err)
	for _, ref := range active.Mods {
		if ref.Disabled {
			continue
		}
		row, err := svc.GetInstalledMod(ctx, ref.SourceID, ref.ModID, gameID, active.Name)
		require.NoError(t, err, "the active profile %s has a row for %s", active.Name, ref.ModID)
		files, err := svc.GetGameCache(game).ListFiles(gameID, row.SourceID, row.ID, row.Version)
		require.NoError(t, err)
		require.NotEmpty(t, files)
		recorded, err := svc.GetDeployedFilesForMod(ctx, gameID, active.Name, row.SourceID, row.ID)
		require.NoError(t, err)
		for _, file := range files {
			_, err := os.Lstat(filepath.Join(game.ModPath, file))
			assert.NoError(t, err, "%s of %s is live under %s", file, ref.ModID, game.ModPath)
			assert.Contains(t, recorded, filepath.ToSlash(file))
		}
	}
}

func TestModPathRefusal_ItsOwnCommandsClearIt(t *testing.T) {
	ctx := context.Background()
	plainGame := func(t *testing.T) *domain.Game {
		root := t.TempDir()
		return &domain.Game{ID: "sky", Name: "Sky", InstallPath: root, ModPath: filepath.Join(root, "Data"), LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}
	}
	recordToo := func(t *testing.T, f *legacyFixture, profile, rel, modID string) {
		t.Helper()
		require.NoError(t, f.svc.ExecForTest(ctx,
			`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES (?, ?, ?, 'local', ?)`,
			f.game.ID, profile, rel, modID))
	}

	t.Run("a legacy BepInEx config under a non-active profile", func(t *testing.T) {
		f := newLegacyFixture(t, bepinexGame(t, domain.LinkCopy))
		f.profile(t, "default", true, "d")
		f.profile(t, "alt", false, "m")
		f.deployed(t, "default", "d", domain.LinkCopy, map[string]string{"BepInEx/plugins/d.dll": "d"}, nil)
		f.deployed(t, "alt", "m", domain.LinkCopy, bepinexMod, map[string]string{"BepInEx/config/m.cfg": "setting=USER-TUNED"})
		cfg := filepath.Join(f.game.ModPath, "BepInEx", "config", "m.cfg")

		refusals := followModPathRefusal(t, f.svc, "val", filepath.Join(f.game.InstallPath, "mods"))

		require.Len(t, refusals, 1)
		assert.Contains(t, refusals[0], "(2 by profile alt, 1 by profile default)", "the legacy config row is counted, and alt's purge clears it")
		requireActiveListedLive(t, f.svc, "val")
		assert.Equal(t, "setting=USER-TUNED", readLive(t, cfg), "the user's config is untouched")
		assert.NoFileExists(t, filepath.Join(f.game.InstallPath, "BepInEx", "plugins", "m.dll"))
	})

	t.Run("a file the active profile shares", func(t *testing.T) {
		f := newLegacyFixture(t, plainGame(t))
		f.profile(t, "default", true, "s")
		f.profile(t, "alt", false, "s", "a")
		f.deployed(t, "default", "s", domain.LinkSymlink, map[string]string{"s.esp": "s"}, nil)
		recordToo(t, f, "alt", "s.esp", "s")
		f.deployed(t, "alt", "a", domain.LinkSymlink, map[string]string{"a.esp": "a"}, nil)

		refusals := followModPathRefusal(t, f.svc, "sky", filepath.Join(f.game.InstallPath, "Mods"))

		require.Len(t, refusals, 1)
		requireActiveListedLive(t, f.svc, "sky")
		assert.NoFileExists(t, filepath.Join(f.game.InstallPath, "Mods", "a.esp"))
	})

	t.Run("a file two non-active profiles share", func(t *testing.T) {
		f := sharedFixture(t, "d")
		f.game.InstallPath = filepath.Dir(f.game.ModPath)
		require.NoError(t, f.svc.SaveGame(ctx, f.game))
		f.deployed(t, "default", "d", domain.LinkCopy, map[string]string{"d.esp": "d"}, nil)

		refusals := followModPathRefusal(t, f.svc, "sky", filepath.Join(f.game.InstallPath, "Mods"))

		require.Len(t, refusals, 1)
		requireActiveListedLive(t, f.svc, "sky")
		assert.NoFileExists(t, filepath.Join(f.game.InstallPath, "Mods", "x.esp"))
	})

	t.Run("the state a v1.30.1 switch leaves", func(t *testing.T) {
		f := l1Fixture(t)
		f.game.InstallPath = filepath.Dir(f.game.ModPath)
		require.NoError(t, f.svc.SaveGame(ctx, f.game))

		refusals := followModPathRefusal(t, f.svc, "sky", filepath.Join(f.game.InstallPath, "Mods"))

		require.Len(t, refusals, 1)
		assert.Contains(t, refusals[0], "run `lmm profile apply alt --game sky`")
		assert.Contains(t, refusals[0], "`lmm purge --game sky --profile alt`", "the active profile's purge is named, though it has no rows yet")
		requireActiveListedLive(t, f.svc, "sky")
	})

	t.Run("the same, with the active profile's row already enabled", func(t *testing.T) {
		f := l1Fixture(t)
		f.game.InstallPath = filepath.Dir(f.game.ModPath)
		require.NoError(t, f.svc.SaveGame(ctx, f.game))
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: "a", SourceID: "local", Name: "Mod a", Version: "unknown", GameID: "sky"},
			ProfileName: "alt", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true, LinkMethod: domain.LinkSymlink,
		}))

		refusals := followModPathRefusal(t, f.svc, "sky", filepath.Join(f.game.InstallPath, "Mods"))

		require.Len(t, refusals, 1)
		assert.NotContains(t, refusals[0], "lmm profile apply", "an apply has nothing to do for an enabled row")
		requireActiveListedLive(t, f.svc, "sky")
	})
}

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
// way the CLI runs it. The refusal for gameID names commands for gameID,
// and - to let go of a file another game records (#445 gate 2, G2-1) -
// purges of another game's profiles.
func runRefusalCommand(t *testing.T, svc *core.Service, gameID, command string) {
	t.Helper()
	ctx := context.Background()
	fields := strings.Fields(command)
	flag := func(name string) string {
		i := slices.Index(fields, name)
		require.GreaterOrEqual(t, i, 0, "%q has no %s", command, name)
		return fields[i+1]
	}
	named := flag("--game")
	if !strings.HasPrefix(command, "lmm purge ") {
		require.Equal(t, gameID, named, command)
	}
	game, err := svc.GetGame(named)
	require.NoError(t, err)
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
	case strings.HasPrefix(command, "lmm verify --fix "):
		// #469: drops the active profile's records no installed mod owns.
		report, err := svc.VerifyReport(ctx, game, flag("--profile"), core.VerifyOptions{Fix: true, Force: true}, nil)
		require.NoError(t, err, command)
		for _, f := range report.Result.Findings {
			require.NotEqual(t, "orphaned_record", f.Status, "%s left %+v", command, f)
		}
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

// requireActiveListedLive checks every mod gameID's active profile lists
// and does not mark off, each decided by its first reference: the active
// profile has a row for it, enabled and deployed, and each of its cached
// files is live under the (new) mod_path, byte for byte, and recorded under
// the active profile. A listed mod with no row fails rather than being
// skipped - the #445 final gate's F-A left exactly that, a listed mod with
// neither a row nor its file, behind a move the refusal allowed.
func requireActiveListedLive(t *testing.T, svc *core.Service, gameID string) {
	t.Helper()
	ctx := context.Background()
	game, err := svc.GetGame(gameID)
	require.NoError(t, err)
	active, err := svc.NewProfileManager().GetDefault(ctx, gameID)
	require.NoError(t, err)
	gameCache := svc.GetGameCache(game)
	seen := make(map[string]bool, len(active.Mods))
	for _, ref := range active.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if seen[key] {
			continue
		}
		seen[key] = true
		if ref.Disabled {
			continue
		}
		row, err := svc.GetInstalledMod(ctx, ref.SourceID, ref.ModID, gameID, active.Name)
		require.NoError(t, err, "the active profile %s lists %s, so it has a row for it", active.Name, ref.ModID)
		assert.True(t, row.Enabled, "%s is enabled under %s", ref.ModID, active.Name)
		assert.True(t, row.Deployed, "%s is deployed under %s", ref.ModID, active.Name)
		files, err := gameCache.ListFiles(gameID, row.SourceID, row.ID, row.Version)
		require.NoError(t, err)
		require.NotEmpty(t, files)
		recorded, err := svc.GetDeployedFilesForMod(ctx, gameID, active.Name, row.SourceID, row.ID)
		require.NoError(t, err)
		for _, file := range files {
			want, err := os.ReadFile(gameCache.GetFilePath(gameID, row.SourceID, row.ID, row.Version, file))
			require.NoError(t, err)
			got, err := os.ReadFile(filepath.Join(game.ModPath, file))
			if assert.NoError(t, err, "%s of %s is live under %s", file, ref.ModID, game.ModPath) {
				assert.Equal(t, string(want), string(got), "%s of %s is its cached copy", file, ref.ModID)
			}
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

// TestModPathRefusal_AListedVersionNothingCanSupplyIsNamed is the #445
// final gate's F-C (R7): the active profile lists a mod at a version no
// cache holds and its source cannot supply - a local mod pinned to 2.0 while
// only "unknown" is cached. A purge keeps the mod's live file for the active
// profile, and `lmm profile apply` cannot record it, so a refusal naming
// the apply sent the user round a loop that never ended. The refusal now
// says what is listed, what is cached, and the edit that ends the loop; after
// either edit, its own commands clear it.
func TestModPathRefusal_AListedVersionNothingCanSupplyIsNamed(t *testing.T) {
	ctx := context.Background()
	altPinned := "name: alt\ngame_id: sky\nmods:\n    - source_id: local\n      mod_id: a\n      version: \"2.0\"\n"
	pinned := func(t *testing.T, doc string) (*legacyFixture, string) {
		t.Helper()
		f := l1Fixture(t)
		f.game.InstallPath = filepath.Dir(f.game.ModPath)
		require.NoError(t, f.svc.SaveGame(ctx, f.game))
		path := filepath.Join(f.svc.ConfigDir(), "games", "sky", "profiles", "alt.yaml")
		require.NoError(t, os.WriteFile(path, []byte(doc+"is_default: true\n"), 0o644))
		return f, path
	}
	to := func(f *legacyFixture) string { return filepath.Join(f.game.InstallPath, "Mods") }

	t.Run("the refusal says what is listed and what is cached", func(t *testing.T) {
		f, path := pinned(t, altPinned)

		_, err := f.svc.SetGameModPath(ctx, "sky", to(f))

		var inUse *core.GameModPathInUseError
		require.ErrorAs(t, err, &inUse)
		assert.Equal(t, 1, inUse.ListedUnrecorded)
		assert.False(t, inUse.NeedsApply, "no apply can record it")
		assert.False(t, inUse.NeedsDeploy)
		assert.Equal(t, []core.ListedVersionUnavailable{{
			SourceID: "local", ModID: "a", Version: "2.0", Cached: []string{"unknown"}, ProfileFile: path,
		}}, inUse.ListedUnavailable)
		text := err.Error()
		assert.NotContains(t, text, "lmm profile apply")
		for _, want := range []string{"local:a at version 2.0", "the cache holds it only at unknown", "source local", path, "`disabled: true`"} {
			assert.Contains(t, text, want)
		}
	})

	t.Run("its own commands alone leave it refused, saying the same", func(t *testing.T) {
		f, _ := pinned(t, altPinned)
		_, err := f.svc.SetGameModPath(ctx, "sky", to(f))
		require.Error(t, err)
		first := err.Error()
		before, _, found := strings.Cut(first, "then change the mod_path")
		require.True(t, found)
		for _, m := range refusalCommand.FindAllStringSubmatch(before, -1) {
			runRefusalCommand(t, f.svc, "sky", m[1])
		}

		_, err = f.svc.SetGameModPath(ctx, "sky", to(f))

		require.Error(t, err)
		assert.Equal(t, first, err.Error())
		assert.FileExists(t, filepath.Join(f.game.ModPath, "Data", "a.esp"), "the listed file is kept meanwhile")
	})

	t.Run("listing the cached version clears it", func(t *testing.T) {
		f, _ := pinned(t, strings.Replace(altPinned, `"2.0"`, "unknown", 1))

		refusals := followModPathRefusal(t, f.svc, "sky", to(f))

		require.Len(t, refusals, 1)
		assert.Contains(t, refusals[0], "`lmm profile apply alt --game sky`")
		requireActiveListedLive(t, f.svc, "sky")
	})

	t.Run("marking it off clears it, and its file goes", func(t *testing.T) {
		f, _ := pinned(t, altPinned+"      disabled: true\n")
		old := f.game.ModPath

		refusals := followModPathRefusal(t, f.svc, "sky", to(f))

		require.Len(t, refusals, 1)
		assert.NotContains(t, refusals[0], "lmm profile apply")
		assert.NoFileExists(t, filepath.Join(old, "Data", "a.esp"))
		assert.NoFileExists(t, filepath.Join(to(f), "Data", "a.esp"))
	})

	t.Run("a listed mod an apply can record is still named beside it", func(t *testing.T) {
		f, path := pinned(t, altPinned+"    - source_id: local\n      mod_id: c\n      version: unknown\n")
		f.deployed(t, "default", "c", domain.LinkSymlink, map[string]string{"Data/c.esp": "mod c"}, nil)

		_, err := f.svc.SetGameModPath(ctx, "sky", to(f))

		var inUse *core.GameModPathInUseError
		require.ErrorAs(t, err, &inUse)
		assert.Equal(t, 2, inUse.ListedUnrecorded)
		assert.True(t, inUse.NeedsApply)
		require.Len(t, inUse.ListedUnavailable, 1)
		assert.Equal(t, "a", inUse.ListedUnavailable[0].ModID)
		assert.Contains(t, err.Error(), "`lmm profile apply alt --game sky`")
		assert.Contains(t, err.Error(), "local:a at version 2.0")
		assert.Contains(t, err.Error(), path)
	})
}

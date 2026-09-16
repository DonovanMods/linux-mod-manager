package core_test

// #445 review F2: which profile is active was GetDefault's answer, and
// GetDefault skips a profile file it cannot read and falls back to the first
// readable profile. A typo in the active profile's file therefore made
// another profile "active", and every decision built on that answer ran
// against the wrong profile: `lmm purge -p <other>` became a full purge and
// deleted a file the real active profile still had live. Every decision that
// deploys into the game directory or removes from it now fails closed when
// the answer is a guess - a profile file that cannot be read, several marked
// `is_default: true`, or none marked among several - and names
// `lmm profile list`. A game with exactly one profile file has no guess to
// make (coordinator ruling A), and a game with none deploys "default".

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// breakProfile replaces profile's file with text the decoder rejects - the
// indentation typo the review used.
func (f *backfillFixture) breakProfile(t *testing.T, profile string) {
	t.Helper()
	path := f.profilePath(profile)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	broken := strings.Replace(string(data), "game_id:", "  game_id:", 1)
	require.NotEqual(t, string(data), broken)
	require.NoError(t, os.WriteFile(path, []byte(broken), 0o644))
	_, err = f.svc.NewProfileManager().Get(context.Background(), f.game.ID, profile)
	require.Error(t, err, "the broken file must not load")
}

// setFlag rewrites profile's `is_default:` by hand, the way a hand edit or a
// hand copy of a profile file does.
func (f *backfillFixture) setFlag(t *testing.T, profile string, flagged bool) {
	t.Helper()
	path := f.profilePath(profile)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var lines []string
	for line := range strings.Lines(string(data)) {
		if !strings.HasPrefix(line, "is_default:") {
			lines = append(lines, line)
		}
	}
	if flagged {
		lines = append(lines, "is_default: true\n")
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "")), 0o644))
}

// requireActiveUnknown checks err is the fail-closed refusal and says how
// to find out more.
func requireActiveUnknown(t *testing.T, err error, mentions ...string) {
	t.Helper()
	require.ErrorIs(t, err, core.ErrActiveProfileUnknown)
	assert.NotErrorIs(t, err, core.ErrProfileNotActive, "an unknown active profile is not a known other one")
	assert.Contains(t, err.Error(), "lmm profile list")
	for _, m := range mentions {
		assert.Contains(t, err.Error(), m)
	}
}

func TestActiveProfile_AnUnreadableProfileFileRefusesEveryRemovalAndDeploy(t *testing.T) {
	ctx := context.Background()

	t.Run("purge of the other profile", func(t *testing.T) {
		f := mixedDirFixture(t)
		f.breakProfile(t, "a")
		before := treeOf(t, f.gameDir)

		_, err := f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{})
		requireActiveUnknown(t, err, f.profilePath("a"))
		rows, err := f.svc.GetInstalledMods(ctx, f.game.ID, "b")
		require.NoError(t, err)
		_, err = f.svc.PurgeProfile(ctx, f.game, "b", rows, core.PurgeOptions{}, nil)
		requireActiveUnknown(t, err, f.profilePath("a"))
		assert.Equal(t, before, treeOf(t, f.gameDir), "the active profile's live file is still there")
		assert.FileExists(t, filepath.Join(f.gameDir, "shared.esp"))
	})

	t.Run("a purge planned before the file broke", func(t *testing.T) {
		f := mixedDirFixture(t)
		plan, err := f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{})
		require.NoError(t, err)
		require.True(t, plan.RecordedOnly)
		f.breakProfile(t, "a")
		before := treeOf(t, f.gameDir)
		_, err = f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
		requireActiveUnknown(t, err)
		assert.Equal(t, before, treeOf(t, f.gameDir))
	})

	t.Run("purge of the readable profile the fallback would pick", func(t *testing.T) {
		f := mixedDirFixture(t)
		f.breakProfile(t, "a")
		before := treeOf(t, f.gameDir)
		plan := &core.PurgePlan{Profile: "b"}
		_, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
		requireActiveUnknown(t, err)
		assert.Equal(t, before, treeOf(t, f.gameDir))
	})

	t.Run("deploy", func(t *testing.T) {
		f := mixedDirFixture(t)
		f.breakProfile(t, "a")
		before := treeOf(t, f.gameDir)
		_, err := f.svc.PlanDeploy(ctx, f.game, "b", core.DeployOptions{})
		requireActiveUnknown(t, err)
		_, err = f.svc.DeployProfile(ctx, f.game, "b", core.DeployOptions{All: true}, nil)
		requireActiveUnknown(t, err)
		assert.Equal(t, before, treeOf(t, f.gameDir))
	})

	t.Run("profile delete", func(t *testing.T) {
		f := mixedDirFixture(t)
		f.breakProfile(t, "a")
		err := f.svc.NewProfileManager().Delete(ctx, f.game.ID, "b")
		requireActiveUnknown(t, err)
		assert.FileExists(t, f.profilePath("b"))
	})

	t.Run("profile list says what the refusals saw", func(t *testing.T) {
		f := mixedDirFixture(t)
		f.breakProfile(t, "a")
		listing, err := f.svc.ListProfiles(ctx, f.game.ID)
		require.NoError(t, err)
		require.Len(t, listing.Profiles, 1)
		require.Len(t, listing.Warnings, 1, "one warning: the unreadable file, not also a missing marker")
		assert.Contains(t, listing.Warnings[0], f.profilePath("a"))
		assert.Contains(t, listing.Warnings[0], "cannot be read")
		assert.Contains(t, listing.Warnings[0], "will not deploy, purge or switch")
	})

	t.Run("switch names the file", func(t *testing.T) {
		f := mixedDirFixture(t)
		f.breakProfile(t, "a")
		before := treeOf(t, f.gameDir)
		_, err := f.svc.PlanProfileSwitch(ctx, f.game, "b")
		requireActiveUnknown(t, err, f.profilePath("a"))
		assert.Equal(t, before, treeOf(t, f.gameDir))
	})
}

func TestActiveProfile_SeveralFlaggedProfilesRefuse(t *testing.T) {
	ctx := context.Background()
	f := mixedDirFixture(t)
	f.setFlag(t, "b", true) // a hand copy of a's file
	before := treeOf(t, f.gameDir)

	_, err := f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{})
	requireActiveUnknown(t, err, "a, b", "lmm profile switch")
	_, err = f.svc.PlanDeploy(ctx, f.game, "a", core.DeployOptions{})
	requireActiveUnknown(t, err, "a, b")
	_, err = f.svc.PlanPurge(ctx, f.game, "a", core.PurgeOptions{})
	requireActiveUnknown(t, err)
	assert.Equal(t, before, treeOf(t, f.gameDir))
}

func TestActiveProfile_NoFlaggedProfileAmongSeveralRefuses(t *testing.T) {
	ctx := context.Background()
	f := mixedDirFixture(t)
	f.setFlag(t, "a", false)
	before := treeOf(t, f.gameDir)

	_, err := f.svc.PlanDeploy(ctx, f.game, "a", core.DeployOptions{})
	requireActiveUnknown(t, err, "none of", "lmm profile switch")
	_, err = f.svc.PlanPurge(ctx, f.game, "b", core.PurgeOptions{})
	requireActiveUnknown(t, err)
	assert.Equal(t, before, treeOf(t, f.gameDir))
}

// TestActiveProfile_ASoleUnflaggedProfileIsActive is ruling A: one profile
// file is one answer, flagged or not. `lmm install` on a game set up by hand
// creates exactly that, and its deploys must keep working.
func TestActiveProfile_ASoleUnflaggedProfileIsActive(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "solo", Name: "Solo", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	ctx := context.Background()
	dir := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mine.yaml"), []byte("name: mine\ngame_id: solo\nmods: []\n"), 0o644))

	_, err := svc.PlanDeploy(ctx, game, "mine", core.DeployOptions{})
	require.NoError(t, err)
	_, err = svc.PlanDeploy(ctx, game, "other", core.DeployOptions{})
	require.ErrorIs(t, err, core.ErrProfileNotActive)
	plan, err := svc.PlanPurge(ctx, game, "other", core.PurgeOptions{})
	require.NoError(t, err)
	assert.True(t, plan.RecordedOnly)
	assert.Equal(t, "mine", plan.ActiveProfile)
}

// TestCreate_TheFirstProfileOfAGameIsActive is ruling B: a game's first
// profile file is written marked active, so a new game starts with exactly
// one - whether the profile was asked for or made on the way by an install.
func TestCreate_TheFirstProfileOfAGameIsActive(t *testing.T) {
	ctx := context.Background()

	t.Run("create", func(t *testing.T) {
		svc := newFlowsTestService(t)
		pm := svc.NewProfileManager()
		first, err := pm.Create(ctx, "g", "zeta")
		require.NoError(t, err)
		assert.True(t, first.IsDefault)
		second, err := pm.Create(ctx, "g", "alpha")
		require.NoError(t, err)
		assert.False(t, second.IsDefault)

		for name, want := range map[string]bool{"zeta": true, "alpha": false} {
			p, err := pm.Get(ctx, "g", name)
			require.NoError(t, err)
			assert.Equal(t, want, p.IsDefault, name)
		}
		active, err := pm.GetDefault(ctx, "g")
		require.NoError(t, err)
		assert.Equal(t, "zeta", active.Name)
	})

	t.Run("import", func(t *testing.T) {
		svc := newFlowsTestService(t)
		pm := svc.NewProfileManager()
		doc := []byte("name: imported\ngame_id: g\nmods: []\n")
		p, err := pm.ImportWithOptions(ctx, doc, false)
		require.NoError(t, err)
		assert.True(t, p.IsDefault)
		other, err := pm.ImportWithOptions(ctx, []byte("name: second\ngame_id: g\nmods: []\n"), false)
		require.NoError(t, err)
		assert.False(t, other.IsDefault)
	})
}

// flagOnlyMessage checks msg is ruling C's notice: what happened, that the
// directory may still hold another profile's files, and the two commands -
// for the game, naming each other profile - that make it target's.
func flagOnlyMessage(t *testing.T, msg, target string, others ...string) {
	t.Helper()
	assert.Contains(t, msg, target+" is now the active profile of g1, but nothing was deployed or removed")
	assert.Contains(t, msg, "may still hold files another profile deployed")
	assert.Contains(t, msg, "Run `lmm profile apply "+target+" --game g1` to deploy its mods")
	for _, other := range others {
		assert.Contains(t, msg, "`lmm purge -p "+other+" --game g1`")
	}
	assert.Contains(t, msg, "keeps what "+target+" uses")
	assert.NotContains(t, msg, "lmm deploy")
	assert.NotContains(t, msg, "lmm verify")
}

// TestSwitch_WithNoSingleActiveProfileOnlyMarksTheTarget is ruling C: with
// no single profile marked active, a switch has no From to diff against, so
// it deploys and removes nothing and only records the target as active -
// the way out the refusals above name.
func TestSwitch_WithNoSingleActiveProfileOnlyMarksTheTarget(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(t *testing.T, f *backfillFixture){
		"none marked":    func(t *testing.T, f *backfillFixture) { f.setFlag(t, "a", false) },
		"several marked": func(t *testing.T, f *backfillFixture) { f.setFlag(t, "b", true) },
	} {
		t.Run(name, func(t *testing.T) {
			f := mixedDirFixture(t)
			setup(t, f)
			before := treeOf(t, f.gameDir)

			plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "b")
			require.NoError(t, err)
			assert.True(t, plan.FlagOnly)
			assert.Empty(t, plan.From)
			assert.Empty(t, plan.ToDisable)
			assert.Empty(t, plan.ToEnable)
			assert.Empty(t, plan.ToInstall)
			assert.False(t, plan.AlreadyActive)
			require.Len(t, plan.Warnings, 1)
			assert.Contains(t, plan.Warnings[0], "lmm cannot tell whose mods the game directory holds")
			assert.Contains(t, plan.Warnings[0], "only marks b as the active profile")
			assert.Contains(t, plan.Warnings[0], "nothing is deployed or removed")

			var events []core.Event
			result, err := f.svc.ApplyProfileSwitch(ctx, f.game, plan, func(e core.Event) { events = append(events, e) })
			require.NoError(t, err)
			require.Len(t, result.Warnings, 1)
			flagOnlyMessage(t, result.Warnings[0], "b", "a")
			assert.Contains(t, result.Warnings[0], "to clear the files a recorded")
			assert.Zero(t, result.Disabled+result.Enabled+result.Installed)
			var warned bool
			for _, e := range events {
				if w, ok := e.(core.WarningEvent); ok && w.Message == result.Warnings[0] && w.Phase == core.SwitchFlagOnly {
					warned = true
				}
			}
			assert.True(t, warned, "the notice is an event too, so a job stream shows it")

			assert.Equal(t, before, treeOf(t, f.gameDir), "nothing was deployed or removed")
			a, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "a")
			require.NoError(t, err)
			b, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "b")
			require.NoError(t, err)
			assert.False(t, a.IsDefault)
			assert.True(t, b.IsDefault)
			for _, profile := range []string{"a", "b"} {
				rows, err := f.svc.GetInstalledMods(ctx, f.game.ID, profile)
				require.NoError(t, err)
				for _, row := range rows {
					assert.True(t, row.Enabled, "%s's %s is untouched", profile, row.ID)
				}
			}

			// Now there is one answer again, and the refusals lift.
			_, err = f.svc.PlanDeploy(ctx, f.game, "b", core.DeployOptions{})
			require.NoError(t, err)
		})
	}

	t.Run("a flag-only plan is stale once one profile is marked", func(t *testing.T) {
		f := mixedDirFixture(t)
		f.setFlag(t, "a", false)
		plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "b")
		require.NoError(t, err)
		require.True(t, plan.FlagOnly)
		f.setFlag(t, "a", true)
		_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
		require.ErrorIs(t, err, core.ErrStalePlan)
		a, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "a")
		require.NoError(t, err)
		assert.True(t, a.IsDefault)
	})

	t.Run("a sole unflagged profile is already active", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "solo", Name: "Solo", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
		dir := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "mine.yaml"), []byte("name: mine\ngame_id: solo\nmods: []\n"), 0o644))
		plan, err := svc.PlanProfileSwitch(ctx, game, "mine")
		require.NoError(t, err)
		assert.True(t, plan.AlreadyActive)
		assert.False(t, plan.FlagOnly)
	})
}

// TestSwitch_TheFlagOnlyRecoveryLeavesExactlyTheTargetsFiles pins ruling C's
// notice as a procedure: from a game with no profile marked, the flag-only
// switch, then `lmm profile apply <target>`, then a purge of each other
// profile, leave the game directory holding exactly the target's files - a
// path the target shares with another profile included.
func TestSwitch_TheFlagOnlyRecoveryLeavesExactlyTheTargetsFiles(t *testing.T) {
	ctx := context.Background()
	f := newBackfillFixture(t)
	pm := f.svc.NewProfileManager()
	_, err := pm.Create(ctx, f.game.ID, "c")
	require.NoError(t, err)
	// a was active: shared and aonly are live. c was active for a while and
	// left conly behind. b - the profile the user wants - lists bonly and
	// shared, both switched away from (enabled 0), as a switch leaves them.
	f.row(t, "a", "shared", true, false)
	f.row(t, "a", "aonly", true, false)
	f.row(t, "c", "conly", true, false)
	require.NoError(t, pm.SetDefault(ctx, f.game.ID, "c"))
	_, err = f.svc.DeployProfile(ctx, f.game, "c", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, f.game.ID, "a"))
	_, err = f.svc.DeployProfile(ctx, f.game, "a", core.DeployOptions{}, nil)
	require.NoError(t, err)
	f.row(t, "b", "bonly", false, false)
	f.row(t, "b", "shared", false, false)
	f.setFlag(t, "a", false)
	require.ElementsMatch(t, []string{"aonly.esp", "conly.esp", "shared.esp"}, keysOf(treeOf(t, f.gameDir)))

	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "b")
	require.NoError(t, err)
	require.True(t, plan.FlagOnly)
	result, err := f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	flagOnlyMessage(t, result.Warnings[0], "b", "a", "c")
	assert.Contains(t, result.Warnings[0], "`lmm purge -p a --game g1` and `lmm purge -p c --game g1` to clear the files each of those profiles recorded")

	// lmm profile apply b
	applyPlan, err := f.svc.PlanProfileApply(ctx, f.game, "b")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileApply(ctx, f.game, applyPlan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	// lmm purge -p a, lmm purge -p c
	for _, other := range []string{"a", "c"} {
		purge, err := f.svc.PlanPurge(ctx, f.game, other, core.PurgeOptions{})
		require.NoError(t, err)
		require.True(t, purge.RecordedOnly, other)
		_, err = f.svc.ApplyPurge(ctx, f.game, purge, core.PurgeOptions{}, nil)
		require.NoError(t, err)
	}

	tree := treeOf(t, f.gameDir)
	assert.ElementsMatch(t, []string{"bonly.esp", "shared.esp"}, keysOf(tree), "exactly b's files")
	link, err := os.Readlink(filepath.Join(f.gameDir, "shared.esp"))
	require.NoError(t, err)
	assert.FileExists(t, link, "the shared path still resolves")
	for _, mod := range []string{"bonly", "shared"} {
		row, err := f.svc.GetInstalledMod(ctx, "src", mod, f.game.ID, "b")
		require.NoError(t, err)
		assert.True(t, row.Enabled, "b's %s is on", mod)
	}
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

package core_test

// The one-time profile-document backfill (#431, fix rounds F2 and 2). Every
// mod a user disabled BEFORE the `disabled:` marker existed recorded that
// intent in one place only - the installed_mods row - because the profile
// document had no key for it. A converge run reads the document, so on the
// first switch or apply after the upgrade such a mod would be switched back
// on. The backfill turns the row into an explicit marker, once.
//
// The row is weak evidence, though, and fix round 2's audit is what these
// tests pin: enabled = 0 is written by every profile switch (for the
// profile switched AWAY from), purge and a failed re-link clear deployed on
// rows nobody disabled, and the #430 switch bug itself leaves the active
// profile's row at (0, 1) for a mod that is live. A false marker empties the
// game directory the next time the user switches into that profile; a missed
// one only reproduces the pre-upgrade behaviour. So only the least
// ambiguous rows are marked: enabled = 0 AND deployed = 0 under the game's
// single explicitly-default profile.

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// backfillFixture is a Service over a game with two profiles, "a" (the
// explicitly-default one) and "b", its warnings captured.
type backfillFixture struct {
	svc      *core.Service
	game     *domain.Game
	gameDir  string
	warnings *bytes.Buffer
	lockPath string
}

func newBackfillFixture(t *testing.T) *backfillFixture {
	t.Helper()
	f := &backfillFixture{gameDir: t.TempDir(), warnings: &bytes.Buffer{}}
	f.lockPath = filepath.Join(t.TempDir(), ".oplock")
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		WarnWriter: f.warnings, OpLockPath: f.lockPath,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	f.svc = svc
	f.game = &domain.Game{ID: "g1", Name: "Game", ModPath: f.gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	for _, name := range []string{"a", "b"} {
		_, err := pm.Create(context.Background(), f.game.ID, name)
		require.NoError(t, err)
	}
	require.NoError(t, pm.SetDefault(context.Background(), f.game.ID, "a"))
	return f
}

// row seeds an installed row under profile with exactly the two flags given,
// its bytes in the cache and a reference in the profile's document - the
// state an older lmm leaves behind.
func (f *backfillFixture) row(t *testing.T, profile, modID string, enabled, deployed bool) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, f.svc.GetGameCache(f.game).Store(f.game.ID, "src", modID, "1.0", modID+".esp", []byte(modID)))
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "src", Name: "Mod " + modID, Version: "1.0", GameID: f.game.ID},
		ProfileName:  profile,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
		Deployed:     deployed,
	}))
	require.NoError(t, f.svc.NewProfileManager().AddMod(ctx, f.game.ID, profile,
		domain.ModReference{SourceID: "src", ModID: modID, Version: "1.0"}))
}

func (f *backfillFixture) owe(t *testing.T) {
	t.Helper()
	require.NoError(t, f.svc.OweProfileDisabledBackfillForTest(context.Background()))
}

func (f *backfillFixture) disabledRefs(t *testing.T, profile string) []string {
	t.Helper()
	p, err := f.svc.NewProfileManager().Get(context.Background(), f.game.ID, profile)
	require.NoError(t, err)
	var out []string
	for _, ref := range p.Mods {
		if ref.Disabled {
			out = append(out, ref.ModID)
		}
	}
	return out
}

func (f *backfillFixture) profilePath(profile string) string {
	return filepath.Join(f.svc.ConfigDir(), "games", f.game.ID, "profiles", profile+".yaml")
}

// switchTo plans and applies a profile switch.
func (f *backfillFixture) switchTo(t *testing.T, profile string) {
	t.Helper()
	ctx := context.Background()
	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, profile)
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
}

// holdOpLock takes the cross-process mutation lock the way another lmm
// process would, until the returned func is called.
func holdOpLock(t *testing.T, path string) func() {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(file.Fd()), syscall.LOCK_EX))
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}
	t.Cleanup(release)
	return release
}

// TestBackfillProfileDisabledMarkers_MarksADisabledModUnderTheActiveProfile
// is the case the backfill exists for: `lmm mod disable` (since #183 it
// writes enabled = 0 AND deployed = 0) under the profile that is still the
// active one. The notice names the mod, its profile and its file, and ends
// with the command that undoes it.
func TestBackfillProfileDisabledMarkers_MarksADisabledModUnderTheActiveProfile(t *testing.T) {
	f := newBackfillFixture(t)
	f.row(t, "a", "off", false, false)
	f.row(t, "a", "on", true, true)
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(context.Background())
	require.NoError(t, err)
	require.NotNil(t, report)
	require.Len(t, report.Marked, 1)
	assert.Equal(t, core.ProfileBackfillMark{
		GameID: "g1", Profile: "a", File: f.profilePath("a"), SourceID: "src", ModID: "off", Name: "Mod off",
	}, report.Marked[0])
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))

	notice := f.warnings.String()
	assert.Contains(t, notice, "Mod off")
	assert.Contains(t, notice, "profile a")
	assert.Contains(t, notice, f.profilePath("a"))
	assert.True(t, strings.HasSuffix(strings.TrimSpace(notice), "lmm mod enable off --game g1 --source src --profile a"),
		"the notice ends with the one command that undoes a wrong marker, got:\n%s", notice)

	owed, err := f.svc.ProfileDisabledBackfillOwedForTest(context.Background())
	require.NoError(t, err)
	assert.Empty(t, owed, "a completed backfill is discharged")
}

// TestRR_BackfillMarksASwitchedAwayMod is fix round 2's R1 blocker, kept as
// a permanent regression: every pre-upgrade `lmm profile switch` leaves the
// OUTGOING profile's rows at enabled = 0 for mods the user wants on there.
// The backfill marked them, and the next switch into that profile emptied
// the game directory.
func TestRR_BackfillMarksASwitchedAwayMod(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.NewProfileManager().SetDefault(ctx, f.game.ID, "b")) // the user is on b now
	f.row(t, "a", "x", false, true)                                               // switched away from, never disabled
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
	assert.Empty(t, f.disabledRefs(t, "a"))

	f.switchTo(t, "a")
	assert.FileExists(t, filepath.Join(f.gameDir, "x.esp"), "switching back must deploy what the user switched away from")
}

// TestBackfillProfileDisabledMarkers_PurgeThenSwitchIsNotADisable is the
// audit's S1: `lmm purge` clears deployed on every row of the profile and
// leaves enabled alone, so a switch afterwards writes enabled = 0 onto rows
// that are now (0, 0) - exactly what `lmm mod disable` writes. Nothing tells
// the two apart, so a non-active profile is never marked.
func TestBackfillProfileDisabledMarkers_PurgeThenSwitchIsNotADisable(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "x", true, false)
	_, err := f.svc.DeployProfile(ctx, f.game, "a", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(f.gameDir, "x.esp"))

	purgePlan, err := f.svc.PlanPurge(ctx, f.game, "a", core.PurgeOptions{})
	require.NoError(t, err)
	_, err = f.svc.ApplyPurge(ctx, f.game, purgePlan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	f.switchTo(t, "b")

	row, err := f.svc.GetInstalledMod(ctx, "src", "x", f.game.ID, "a")
	require.NoError(t, err)
	require.False(t, row.Enabled)
	require.False(t, row.Deployed, "the precondition: the row now reads exactly like a disable")

	f.owe(t)
	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
	assert.Empty(t, f.disabledRefs(t, "a"))

	f.switchTo(t, "a")
	assert.FileExists(t, filepath.Join(f.gameDir, "x.esp"))
}

// TestBackfillProfileDisabledMarkers_Issue430LeftTheActiveRowOff is the
// audit's T2: the #430 plan bug skipped a mod both profiles list, so the
// profile switched INTO keeps the (0, 1) row an earlier switch away from it
// wrote - while the mod is live and the document lists it. Marking it would
// take the mod down at the next converge run.
func TestBackfillProfileDisabledMarkers_Issue430LeftTheActiveRowOff(t *testing.T) {
	f := newBackfillFixture(t)
	f.row(t, "a", "x", false, true)
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(context.Background())
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
	assert.Empty(t, f.disabledRefs(t, "a"))
}

// TestBackfillProfileDisabledMarkers_PreV128DisableStaysUnrecorded pins the
// accepted residual of the same rule: a disable made before #183 (v1.28.0)
// also reads (0, 1), so it is not recorded either. Its cost is the
// pre-upgrade behaviour - the next converge run switches it back on, and
// one `lmm mod disable` writes the marker.
func TestBackfillProfileDisabledMarkers_PreV128DisableStaysUnrecorded(t *testing.T) {
	f := newBackfillFixture(t)
	f.row(t, "a", "old", false, true)
	f.row(t, "b", "elsewhere", false, false) // a real disable, but not under the active profile
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(context.Background())
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
	assert.Empty(t, f.disabledRefs(t, "a"))
	assert.Empty(t, f.disabledRefs(t, "b"))
}

// TestBackfillProfileDisabledMarkers_OnlyAnExplicitDefaultIsActive: the
// active profile is the one file that says `is_default: true`. With none
// (a deleted default, or `profile import --force` over the active name)
// GetDefault falls back to a guess, and with two it is ambiguous - either
// way nothing is marked.
func TestBackfillProfileDisabledMarkers_OnlyAnExplicitDefaultIsActive(t *testing.T) {
	for name, defaults := range map[string][]string{"none": nil, "two": {"a", "b"}} {
		t.Run(name, func(t *testing.T) {
			f := newBackfillFixture(t)
			f.row(t, "a", "x", false, false)
			f.row(t, "b", "y", false, false)
			for _, profile := range []string{"a", "b"} {
				p, err := f.svc.NewProfileManager().Get(context.Background(), f.game.ID, profile)
				require.NoError(t, err)
				p.IsDefault = false
				for _, d := range defaults {
					if d == profile {
						p.IsDefault = true
					}
				}
				data, err := os.ReadFile(f.profilePath(profile))
				require.NoError(t, err)
				text := strings.ReplaceAll(string(data), "is_default: true\n", "")
				if p.IsDefault {
					text = "is_default: true\n" + text
				}
				require.NoError(t, os.WriteFile(f.profilePath(profile), []byte(text), 0o644))
			}
			f.owe(t)

			report, err := f.svc.BackfillProfileDisabledMarkers(context.Background())
			require.NoError(t, err)
			assert.Empty(t, report.Marked)
			assert.Empty(t, f.disabledRefs(t, "a"))
			assert.Empty(t, f.disabledRefs(t, "b"))
		})
	}
}

// TestBackfillProfileDisabledMarkers_ConvergeRunsThenLeaveTheModOff is the
// symptom the backfill exists to stop, end to end.
func TestBackfillProfileDisabledMarkers_ConvergeRunsThenLeaveTheModOff(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	require.NoError(t, f.svc.NewProfileManager().AddMod(ctx, f.game.ID, "b", domain.ModReference{SourceID: "src", ModID: "off", Version: "1.0"}))
	f.owe(t)

	_, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)

	applyPlan, err := f.svc.PlanProfileApply(ctx, f.game, "a")
	require.NoError(t, err)
	assert.Empty(t, applyPlan.ToEnable, "`lmm profile apply` must not switch a pre-marker disabled mod back on")
	assert.Empty(t, applyPlan.ToInstall)
	assert.True(t, applyPlan.NoChanges)

	syncPlan, err := f.svc.PlanProfileSync(ctx, f.game, "a")
	require.NoError(t, err)
	assert.Empty(t, syncPlan.ToRemove,
		"`lmm profile sync` must not prune the ref, its load-order position and its pinned version")
	assert.True(t, syncPlan.NoChanges)

	assert.NoFileExists(t, filepath.Join(f.gameDir, "off.esp"))
}

// TestBackfillProfileDisabledMarkers_RunsOnceAndOnlyOnce pins the durable
// obligation: the backfill is a migration, not a rule. A marker the user
// has since removed by hand is not written back.
func TestBackfillProfileDisabledMarkers_RunsOnceAndOnlyOnce(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Marked, 1)

	require.NoError(t, f.svc.NewProfileManager().SetModDisabled(ctx, f.game.ID, "a", "src", "off", false))

	report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Nil(t, report, "the obligation is discharged: a second run does nothing")
	assert.Empty(t, f.disabledRefs(t, "a"), "and the hand edit stands")
}

// TestBackfillProfileDisabledMarkers_NotOwedMeansNothingRuns is the
// backward-compatibility half: a database that does not owe the backfill -
// every fresh one - is never examined, however its rows read, and no
// profile file moves a byte.
func TestBackfillProfileDisabledMarkers_NotOwedMeansNothingRuns(t *testing.T) {
	f := newBackfillFixture(t)
	f.row(t, "a", "off", false, false)
	before, err := os.ReadFile(f.profilePath("a"))
	require.NoError(t, err)

	report, err := f.svc.BackfillProfileDisabledMarkers(context.Background())
	require.NoError(t, err)
	assert.Nil(t, report)

	after, err := os.ReadFile(f.profilePath("a"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
	assert.Empty(t, f.warnings.String())
}

// TestBackfillProfileDisabledMarkers_OwedWithNothingToMarkWritesNothing: an
// owed backfill that finds nothing it may mark discharges itself and writes
// no profile file.
func TestBackfillProfileDisabledMarkers_OwedWithNothingToMarkWritesNothing(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "on", true, true)
	f.row(t, "b", "away", false, true)
	// A row the active profile never listed: no desired-state entry to mark.
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "unlisted", SourceID: "src", Name: "Unlisted", Version: "1.0", GameID: f.game.ID},
		ProfileName: "a", UpdatePolicy: domain.UpdateNotify,
	}))
	f.owe(t)
	before := map[string]string{}
	for _, p := range []string{"a", "b"} {
		data, err := os.ReadFile(f.profilePath(p))
		require.NoError(t, err)
		before[p] = string(data)
	}

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
	assert.Empty(t, report.Skipped)
	for p, text := range before {
		data, err := os.ReadFile(f.profilePath(p))
		require.NoError(t, err)
		assert.Equal(t, text, string(data), "profile %s must not move a byte", p)
	}
	assert.Empty(t, f.warnings.String())
	owed, err := f.svc.ProfileDisabledBackfillOwedForTest(ctx)
	require.NoError(t, err)
	assert.Empty(t, owed)
}

// TestBackfillProfileDisabledMarkers_SkipsExternalRows pins #269's rule
// through the migration: lmm cannot switch a Steam Workshop item off.
func TestBackfillProfileDisabledMarkers_SkipsExternalRows(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: "ws", SourceID: "steamworkshop", Name: "Workshop Item", Version: "12345", GameID: f.game.ID},
		ProfileName:  "a",
		UpdatePolicy: domain.UpdateNotify,
		External:     true,
		ExternalPath: "/steam/workshop/content/1/12345",
	}))
	require.NoError(t, f.svc.NewProfileManager().AddMod(ctx, f.game.ID, "a", domain.ModReference{SourceID: "steamworkshop", ModID: "ws"}))
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
	assert.Empty(t, f.disabledRefs(t, "a"))
}

// TestBackfillProfileDisabledMarkers_MarkerSurvivesTheToggleRoundTrip: once
// backfilled, the mod behaves exactly like one disabled today, including
// being recoverable by the command the notice prints.
func TestBackfillProfileDisabledMarkers_MarkerSurvivesTheToggleRoundTrip(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	f.owe(t)
	_, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)

	result, err := f.svc.EnableMod(ctx, f.game, "a", "src", "off")
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Empty(t, f.disabledRefs(t, "a"), "`lmm mod enable` clears a backfilled marker like any other")
	assert.FileExists(t, filepath.Join(f.gameDir, "off.esp"))

	plan, err := f.svc.PlanProfileApply(ctx, f.game, "a")
	require.NoError(t, err)
	assert.Empty(t, plan.ToDisable)
}

// TestBackfillProfileDisabledMarkers_WritesOnlyTheMarkerWhereTheFileWasRead
// is R3: the backfill runs from a read-only command, on files the owner
// keeps in dotfiles. It must not drop comments, expand `~/`, or add null
// hook keys - and it writes the file the profile was READ from: a
// hand-copied profile whose `name:` still says "default" is not written
// over default.yaml.
func TestBackfillProfileDisabledMarkers_WritesOnlyTheMarkerWhereTheFileWasRead(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "b", "alpha", false, false)
	f.row(t, "b", "beta", true, true)

	// "b" is a copy of the old default, name: never changed, and it is the
	// active profile now.
	copied := `# My carefully curated profile
name: a
game_id: g1
is_default: true
hooks:
  install:
    after_all: ~/bin/after.sh   # run my script
mods:
  # Alpha first
  - {source_id: src, mod_id: alpha, version: "1.0"}
  - source_id: src
    mod_id: beta
    version: "1.0"
`
	require.NoError(t, os.WriteFile(f.profilePath("b"), []byte(copied), 0o644))
	aText := strings.ReplaceAll(mustRead(t, f.profilePath("a")), "is_default: true\n", "")
	require.NoError(t, os.WriteFile(f.profilePath("a"), []byte(aText), 0o644))
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Marked, 1)
	assert.Equal(t, f.profilePath("b"), report.Marked[0].File)

	want := strings.Replace(copied, `version: "1.0"}`, `version: "1.0", disabled: true}`, 1)
	assert.Equal(t, want, mustRead(t, f.profilePath("b")))
	assert.Equal(t, aText, mustRead(t, f.profilePath("a")), "the profile the copy is still NAMED after is not touched")
	assert.Contains(t, f.warnings.String(), f.profilePath("b"))
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// TestBackfillProfileDisabledMarkers_AnUnwritableProfileIsRetriedWhenItChanges
// is R2: a profile the backfill cannot write (a read-only dotfile target)
// is reported by file, every other game is still backfilled, and the
// obligation for THAT profile is kept - without an ordinary open taking the
// cross-process lock again until the file (or its directory) changes.
func TestBackfillProfileDisabledMarkers_AnUnwritableProfileIsRetriedWhenItChanges(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)

	// A second game whose active profile is writable.
	other := &domain.Game{ID: "g2", Name: "Other", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	pm := f.svc.NewProfileManager()
	_, err := pm.Create(ctx, other.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, other.ID, "default"))
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "z", SourceID: "src", Name: "Mod z", Version: "1.0", GameID: other.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.AddMod(ctx, other.ID, "default", domain.ModReference{SourceID: "src", ModID: "z"}))

	// g1's active profile lives, read-only, in a "store" it is linked from.
	store := filepath.Join(t.TempDir(), "store")
	require.NoError(t, os.Mkdir(store, 0o755))
	target := filepath.Join(store, "a.yaml")
	require.NoError(t, os.Rename(f.profilePath("a"), target))
	require.NoError(t, os.Symlink(target, f.profilePath("a")))
	require.NoError(t, os.Chmod(store, 0o555))
	t.Cleanup(func() { _ = os.Chmod(store, 0o755) })
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err, "one unwritable profile is a diagnostic, not a failure")
	require.Len(t, report.Marked, 1, "the other game is still backfilled")
	assert.Equal(t, "g2", report.Marked[0].GameID)
	require.Len(t, report.Skipped, 1)
	assert.Equal(t, f.profilePath("a"), report.Skipped[0].File)
	assert.Equal(t, []string{"Mod off"}, report.Skipped[0].Mods)
	assert.Contains(t, f.warnings.String(), f.profilePath("a"))

	// Unchanged, the file is not retried - and the retry check takes no
	// lock, so a held one costs an ordinary open nothing.
	f.warnings.Reset()
	release := holdOpLock(t, f.lockPath)
	started := time.Now()
	report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Nil(t, report)
	assert.Less(t, time.Since(started), time.Second, "an unchanged pending profile must not wait on the lock")
	assert.Empty(t, f.warnings.String(), "and it is not reported again")
	release()

	// Repaired, it is.
	require.NoError(t, os.Chmod(store, 0o755))
	report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Marked, 1)
	assert.Equal(t, "off", report.Marked[0].ModID)
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
	owed, err := f.svc.ProfileDisabledBackfillOwedForTest(ctx)
	require.NoError(t, err)
	assert.Empty(t, owed)
}

// TestBackfillProfileDisabledMarkers_AReadOnlyProfileIsSkippedUntilItChanges
// is fix round 3's F4 end to end: a profile file the user made read-only is
// skipped and named, not rewritten, and marked once they make it writable.
func TestBackfillProfileDisabledMarkers_AReadOnlyProfileIsSkippedUntilItChanges(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write a read-only file")
	}
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	require.NoError(t, os.Chmod(f.profilePath("a"), 0o444))
	original := mustRead(t, f.profilePath("a"))
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
	require.Len(t, report.Skipped, 1)
	assert.Equal(t, f.profilePath("a"), report.Skipped[0].File)
	require.ErrorIs(t, report.Skipped[0].Err, os.ErrPermission)
	assert.Equal(t, original, mustRead(t, f.profilePath("a")))

	require.NoError(t, os.Chmod(f.profilePath("a"), 0o644))
	report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Marked, 1)
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
}

// TestBackfillProfileDisabledMarkers_ABrokenProfileDoesNotStopTheOthers: a
// profile file that will not parse is skipped - it cannot be the active
// profile as far as lmm is concerned (GetDefault skips it too) - and every
// other profile of the game is still examined.
func TestBackfillProfileDisabledMarkers_ABrokenProfileDoesNotStopTheOthers(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "q", SourceID: "src", Name: "Mod q", Version: "1.0", GameID: f.game.ID},
		ProfileName: "0broken", UpdatePolicy: domain.UpdateNotify,
	}))
	broken := filepath.Join(filepath.Dir(f.profilePath("a")), "0broken.yaml")
	require.NoError(t, os.WriteFile(broken, []byte("name: 0broken\nmods: [this is: : not yaml\n"), 0o644))
	// One the YAML decoder itself panics on (fix round 3's fuzzing found it).
	panics := filepath.Join(filepath.Dir(f.profilePath("a")), "1panics.yaml")
	require.NoError(t, os.WriteFile(panics, []byte("<<:\n? 0:"), 0o644))
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Marked, 1)
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
}

// TestBackfillProfileDisabledMarkers_ABrokenCandidateIsRecordedOnceRepaired:
// when no readable profile is the explicit default, an unreadable one might
// be - so its disabled rows are kept, and marked once the file is repaired
// and turns out to be the game's one default.
func TestBackfillProfileDisabledMarkers_ABrokenCandidateIsRecordedOnceRepaired(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	good := mustRead(t, f.profilePath("a"))
	require.NoError(t, os.WriteFile(f.profilePath("a"), []byte(good+"mods: [this is: : not yaml\n"), 0o644))
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
	require.Len(t, report.Skipped, 1)
	assert.Equal(t, f.profilePath("a"), report.Skipped[0].File)

	require.NoError(t, os.WriteFile(f.profilePath("a"), []byte(good), 0o644))
	report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Marked, 1)
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
}

// TestBackfillProfileDisabledMarkers_AnEnableSupersedesAPendingMarker: the
// rows a pending profile was captured with are frozen evidence, and a
// later enable of one of them is newer intent - so it leaves the pending
// set, and a retry never marks a mod the user has switched on since.
func TestBackfillProfileDisabledMarkers_AnEnableSupersedesAPendingMarker(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	f.row(t, "a", "also", false, false)
	require.NoError(t, os.Chmod(filepath.Dir(f.profilePath("a")), 0o555))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(f.profilePath("a")), 0o755) })
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Skipped, 1)

	// Enabled in the meantime (the directory is read-only, so there is no
	// marker to clear and none is written), then left at (0, 0) again by a
	// flow that is not a disable - a purge-then-switch, say.
	_, err = f.svc.EnableMod(ctx, f.game, "a", "src", "off")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetModEnabledForTest(ctx, "src", "off", f.game.ID, "a", false))
	require.NoError(t, f.svc.SetModDeployed(ctx, "src", "off", f.game.ID, "a", false))

	require.NoError(t, os.Chmod(filepath.Dir(f.profilePath("a")), 0o755))
	report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"also"}, f.disabledRefs(t, "a"), "only the mod nobody switched on since")
	require.Len(t, report.Marked, 1)
}

// TestBackfillProfileDisabledMarkers_AMutationDischargesItFirst closes the
// ordering hole: the new binary's own flows write the flag values the
// backfill reads as evidence - `lmm purge` clears deployed on every row - so
// an owed backfill runs inside the first mutation's slot, BEFORE it, not
// only when an installation is opened. Here the active profile holds a
// (0, 1) row (the #430 leftover); a purge first would turn it into (0, 0)
// and a later backfill would mark a mod nobody disabled.
func TestBackfillProfileDisabledMarkers_AMutationDischargesItFirst(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "x", false, true)
	f.row(t, "a", "off", false, false)
	f.owe(t)

	plan, err := f.svc.PlanPurge(ctx, f.game, "a", core.PurgeOptions{})
	require.NoError(t, err)
	_, err = f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	assert.Contains(t, f.warnings.String(), "Mod off", "the mutation printed the notice")

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Nil(t, report, "already discharged")
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"), "x was never disabled and must stay unmarked")
}

// TestBackfillProfileDisabledMarkers_ReadsItsRowsUnderTheSlot is R4: the
// rows are read only once the mutation slot is held, so a change another
// process committed while this one waited for the lock is the one acted on.
func TestBackfillProfileDisabledMarkers_ReadsItsRowsUnderTheSlot(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	f.owe(t)

	f.svc.SetBeforeProfileBackfillScanForTest(func() {
		// An older lmm, say a `lmm serve` still running across the upgrade,
		// enabled the mod and released the lock this process was waiting on.
		conn, err := sql.Open("sqlite", filepath.Join(f.svc.DataDirForTest(), "lmm.db"))
		require.NoError(t, err)
		defer func() { require.NoError(t, conn.Close()) }()
		_, err = conn.Exec(`UPDATE installed_mods SET enabled = 1, deployed = 1 WHERE mod_id = 'off'`)
		require.NoError(t, err)
	})

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Marked)
	assert.Empty(t, f.disabledRefs(t, "a"))
}

// TestBackfillProfileDisabledMarkers_ASecondProcessFindsItDone: two
// processes over one installation, both constructed while the backfill was
// owed. The one that gets there second re-reads the obligation - on its
// next open, or under the lock inside its next mutation - and does nothing:
// one notice, not two.
func TestBackfillProfileDisabledMarkers_ASecondProcessFindsItDone(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	f.owe(t)

	newSecond := func() (*core.Service, *bytes.Buffer) {
		var warnings bytes.Buffer
		second, err := core.NewService(core.ServiceConfig{
			ConfigDir: f.svc.ConfigDir(), DataDir: f.svc.DataDirForTest(), CacheDir: t.TempDir(),
			WarnWriter: &warnings, OpLockPath: f.lockPath,
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, second.Close()) })
		return second, &warnings
	}
	opener, openerWarnings := newSecond()
	mutator, mutatorWarnings := newSecond()

	first, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, first.Marked, 1)
	require.NoError(t, f.svc.NewProfileManager().SetModDisabled(ctx, f.game.ID, "a", "src", "off", false))

	again, err := opener.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Nil(t, again)
	require.NoError(t, mutator.SetModDeployed(ctx, "src", "off", f.game.ID, "a", false))

	assert.Empty(t, openerWarnings.String())
	assert.Empty(t, mutatorWarnings.String())
	assert.Empty(t, f.disabledRefs(t, "a"), "neither wrote the marker the user has since removed")
}

// exec runs statement against the fixture's database file directly, the way
// an older lmm - or a hand edit - would have written it.
func (f *backfillFixture) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(f.svc.DataDirForTest(), "lmm.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	_, err = conn.Exec(statement, args...)
	require.NoError(t, err)
}

// reopen constructs a second Service over the fixture's installation, as
// the next lmm process would, with its own warnings.
func (f *backfillFixture) reopen(t *testing.T) (*core.Service, *bytes.Buffer) {
	t.Helper()
	var warnings bytes.Buffer
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: f.svc.ConfigDir(), DataDir: f.svc.DataDirForTest(), CacheDir: t.TempDir(),
		WarnWriter: &warnings, OpLockPath: f.lockPath,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc, &warnings
}

// TestBackfillProfileDisabledMarkers_AnEditorPanicSkipsTheProfile is fix
// round 3's F1 boundary: the backfill runs from app.Open, before any command
// does its work, so a panic in the profile editor - F1 was one - used to
// crash every lmm command, the recovery one included, on every run. A bug
// nobody has found yet now costs that one profile: kept, reported by file,
// and not tried again until the file changes.
func TestBackfillProfileDisabledMarkers_AnEditorPanicSkipsTheProfile(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	f.row(t, "b", "x", true, true)
	f.owe(t)
	original := mustRead(t, f.profilePath("a"))

	calls := 0
	f.svc.SetProfileMarkerForTest(func(path string, mods []domain.ModReference) ([]domain.ModReference, error) {
		calls++
		var lines []int
		return nil, fmt.Errorf("unreachable: %d", lines[len(mods)]) // index out of range, as F1 was
	})

	var report *core.ProfileBackfillReport
	var err error
	require.NotPanics(t, func() { report, err = f.svc.BackfillProfileDisabledMarkers(ctx) })
	require.NoError(t, err, "an editor bug is a skipped profile, not a failure")
	require.Len(t, report.Skipped, 1)
	assert.Equal(t, f.profilePath("a"), report.Skipped[0].File)
	require.ErrorContains(t, report.Skipped[0].Err, "index out of range")
	assert.Contains(t, f.warnings.String(), f.profilePath("a"))
	assert.Equal(t, original, mustRead(t, f.profilePath("a")))
	assert.Equal(t, 1, calls)

	// Unchanged, the file is not tried again: not by the next open, and not
	// by a mutation elsewhere - which is not refused either.
	report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Nil(t, report)
	_, err = f.svc.DisableMod(ctx, f.game, "b", "src", "x")
	require.NoError(t, err)
	assert.Equal(t, 1, calls)

	// With the editor fixed and the file changed, it is marked.
	f.svc.SetProfileMarkerForTest(nil)
	later := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(f.profilePath("a"), later, later))
	report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Marked, 1)
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
}

// TestBackfillProfileDisabledMarkers_APlanMadeBeforeTheMarkersIsStale is
// fix round 3's F2. A plan computed while the backfill was still owed (the
// open could not take the lock) was applied unchecked after beginOp
// discharged it: `profile apply` re-enabled, and `profile sync` deleted the
// reference of, the very mod the notice had just recorded as disabled. The
// marker is part of what a plan was computed from, so a changed one makes
// the plan stale.
func TestBackfillProfileDisabledMarkers_APlanMadeBeforeTheMarkersIsStale(t *testing.T) {
	ctx := context.Background()

	// A plan the markers decide first settles an owed backfill itself, as
	// its Apply would, so the ordinary case never reaches the refusal.
	t.Run("an apply or sync plan settles the backfill first", func(t *testing.T) {
		for _, flow := range []string{"apply", "sync"} {
			t.Run(flow, func(t *testing.T) {
				f := newBackfillFixture(t)
				f.row(t, "a", "off", false, false)
				f.owe(t)

				switch flow {
				case "apply":
					plan, err := f.svc.PlanProfileApply(ctx, f.game, "a")
					require.NoError(t, err)
					assert.Empty(t, plan.ToEnable)
				case "sync":
					plan, err := f.svc.PlanProfileSync(ctx, f.game, "a")
					require.NoError(t, err)
					assert.Empty(t, plan.Warnings, "the marker settles what the row says")
				}
				assert.Contains(t, f.warnings.String(), "Mod off", "the plan discharged it and said so")
				assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
			})
		}
	})

	// Another lmm holds the lock while the plan is made, so the plan goes
	// ahead without waiting long; its Apply, which does wait, discharges
	// the backfill and refuses the plan.
	t.Run("profile apply", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		f.owe(t)

		release := holdOpLock(t, f.lockPath)
		started := time.Now()
		plan, err := f.svc.PlanProfileApply(ctx, f.game, "a")
		require.NoError(t, err)
		assert.Less(t, time.Since(started), 1500*time.Millisecond, "a plan waits less than a mutation does")
		require.Len(t, plan.ToEnable, 1, "planned before the marker existed")
		release()

		_, err = f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
		require.ErrorIs(t, err, core.ErrStalePlan)
		assert.Contains(t, f.warnings.String(), "Mod off", "the mutation's slot discharged the backfill")
		assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
		row, err := f.svc.GetInstalledMod(ctx, "src", "off", f.game.ID, "a")
		require.NoError(t, err)
		assert.False(t, row.Enabled, "the stale plan did not switch it back on")

		plan, err = f.svc.PlanProfileApply(ctx, f.game, "a")
		require.NoError(t, err)
		assert.Empty(t, plan.ToEnable, "the re-plan reads the marker")
	})

	t.Run("profile sync", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		f.owe(t)

		release := holdOpLock(t, f.lockPath)
		plan, err := f.svc.PlanProfileSync(ctx, f.game, "a")
		require.NoError(t, err)
		// #444: the sync keeps an unmarked disabled mod and warns - it used
		// to plan its removal - so the warning is what was decided from the
		// unmarked read.
		require.Len(t, plan.Warnings, 1, "planned before the marker existed")
		release()

		_, err = f.svc.ApplyProfileSync(ctx, f.game, plan, nil)
		require.ErrorIs(t, err, core.ErrStalePlan)
		assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"), "the reference and its marker survive")

		plan, err = f.svc.PlanProfileSync(ctx, f.game, "a")
		require.NoError(t, err)
		assert.Empty(t, plan.Warnings, "the re-plan reads the marker")
	})

	// Another lmm's discharge can write the marker between the document
	// read a plan decides from and the snapshot it records. The snapshot
	// has to describe what the plan read, or the plan passes the check
	// with a decision the marker has already overruled.
	t.Run("a marker written while the plan is being made", func(t *testing.T) {
		for _, flow := range []string{"apply", "sync"} {
			t.Run(flow, func(t *testing.T) {
				f := newBackfillFixture(t)
				f.row(t, "a", "off", false, false)
				other, _ := f.reopen(t) // another lmm, which owes nothing itself

				var apply func() error
				core.AfterProfileLoadForTest(func(loads int) {
					if loads == 1 {
						require.NoError(t, other.NewProfileManager().SetModDisabled(ctx, f.game.ID, "a", "src", "off", true))
					}
				}, func() {
					switch flow {
					case "apply":
						plan, err := f.svc.PlanProfileApply(ctx, f.game, "a")
						require.NoError(t, err)
						require.Len(t, plan.ToEnable, 1, "decided from the unmarked read")
						apply = func() error {
							_, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
							return err
						}
					case "sync":
						plan, err := f.svc.PlanProfileSync(ctx, f.game, "a")
						require.NoError(t, err)
						require.Len(t, plan.Warnings, 1, "decided from the unmarked read")
						apply = func() error {
							_, err := f.svc.ApplyProfileSync(ctx, f.game, plan, nil)
							return err
						}
					}
				})
				require.ErrorIs(t, apply(), core.ErrStalePlan)
				assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
			})
		}
	})

	// Only a plan the marker can change is refused. A deploy never enables a
	// disabled row, and the backfill marks nothing else, so a deploy (or a
	// purge) planned across the discharge still applies. Refusing it made a
	// `lmm deploy` started beside another lmm's first open fail once in a
	// while with "plan is stale" for nothing.
	t.Run("a plan the marker cannot change stays fresh", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		f.row(t, "a", "on", true, false)
		f.owe(t)

		deploy, err := f.svc.PlanDeploy(ctx, f.game, "a", core.DeployOptions{})
		require.NoError(t, err)
		purge, err := f.svc.PlanPurge(ctx, f.game, "a", core.PurgeOptions{})
		require.NoError(t, err)

		_, err = f.svc.ApplyDeploy(ctx, f.game, deploy, core.DeployOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"), "the deploy's slot discharged the backfill")
		assert.FileExists(t, filepath.Join(f.gameDir, "on.esp"))
		assert.NoFileExists(t, filepath.Join(f.gameDir, "off.esp"))
		_, err = f.svc.ApplyPurge(ctx, f.game, purge, core.PurgeOptions{}, nil)
		require.NoError(t, err)
	})

	t.Run("a marker in another profile leaves the plan fresh", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		// A reference with no row at all: something for b's sync to do.
		require.NoError(t, f.svc.NewProfileManager().AddMod(ctx, f.game.ID, "b",
			domain.ModReference{SourceID: "src", ModID: "y", Version: "1.0"}))
		f.owe(t)

		plan, err := f.svc.PlanProfileSync(ctx, f.game, "b")
		require.NoError(t, err)
		require.Len(t, plan.ToRemove, 1)

		_, err = f.svc.ApplyProfileSync(ctx, f.game, plan, nil)
		require.NoError(t, err, "only profile a was marked")
		assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
	})
}

// TestBackfillProfileDisabledMarkers_ABadRowIsSkippedNotFatal is fix round
// 3's F3. One disabled row whose game or profile could never name a file (a
// pre-validation build, or a hand-edited games.yaml key) failed the
// backfill on every open and, because a mutation refuses to run over an
// owed backfill it could not discharge, every mutation of every game with
// it. The row is skipped with a diagnostic; the rest are marked and the
// obligation is discharged.
func TestBackfillProfileDisabledMarkers_ABadRowIsSkippedNotFatal(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	f.exec(t, `INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, enabled, deployed)
		VALUES ('src', 'm', 'weird..game', 'default', 'Weird', '1', 0, 0),
		       ('src', 'n', 'g1', '../escape', 'Escape', '1', 0, 0)`)
	// Flags an older lmm never wrote, but a hand edit can: they are read
	// as "deployed" and "not external", never as a failure.
	f.row(t, "a", "nulls", false, false)
	f.exec(t, `UPDATE installed_mods SET deployed = NULL, external = NULL WHERE mod_id = 'nulls'`)
	f.owe(t)

	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Marked, 1, "the good row is still marked")
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
	require.Len(t, report.Skipped, 2)
	for _, skip := range report.Skipped {
		assert.Empty(t, skip.File, "no file can be named for %s/%s", skip.GameID, skip.Profile)
		require.Error(t, skip.Err)
	}
	assert.Contains(t, f.warnings.String(), "weird..game")
	assert.Contains(t, f.warnings.String(), "../escape")
	owed, err := f.svc.ProfileDisabledBackfillOwedForTest(ctx)
	require.NoError(t, err)
	assert.Empty(t, owed, "discharged, bad rows and all")

	// Nothing is left to refuse a mutation over.
	f.warnings.Reset()
	f.row(t, "b", "x", true, true)
	_, err = f.svc.DisableMod(ctx, f.game, "b", "src", "x")
	require.NoError(t, err)
	assert.Empty(t, f.warnings.String())
}

// TestBackfillProfileDisabledMarkers_AnUnreadablePendingRowIsDropped: a
// profile kept for later froze its rows; when the file is repaired, each is
// read again. One that can no longer be read back is dropped - losing a
// marker is the safe direction - and the rest are still marked (F3).
func TestBackfillProfileDisabledMarkers_AnUnreadablePendingRowIsDropped(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	f.row(t, "a", "odd", false, false)
	dir := filepath.Dir(f.profilePath("a"))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	f.owe(t)
	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Skipped, 1)
	require.Equal(t, []string{"Mod odd", "Mod off"}, report.Skipped[0].Mods)

	f.exec(t, `UPDATE installed_mods SET previous_file_ids = 'not a file list' WHERE mod_id = 'odd'`)
	require.NoError(t, os.Chmod(dir, 0o755))
	report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err, "one row that cannot be read back is not a failure")
	require.Len(t, report.Marked, 1)
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
	owed, err := f.svc.ProfileDisabledBackfillOwedForTest(ctx)
	require.NoError(t, err)
	assert.Empty(t, owed)
}

// TestBackfillProfileDisabledMarkers_OneGamesFailureDoesNotStopTheOthers:
// when the database refuses a write for one game, every other game is still
// marked. The obligation stays owed - that game's share has nowhere to be
// kept - and the next run, once the write succeeds, finishes it (F3).
func TestBackfillProfileDisabledMarkers_OneGamesFailureDoesNotStopTheOthers(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)

	// g0 sorts first. Its active profile cannot be written, so its share
	// has to be kept - and the database refuses that one write.
	pm := f.svc.NewProfileManager()
	_, err := pm.Create(ctx, "g0", "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, "g0", "default"))
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "z", SourceID: "src", Name: "Mod z", Version: "1.0", GameID: "g0"},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.AddMod(ctx, "g0", "default", domain.ModReference{SourceID: "src", ModID: "z"}))
	g0Dir := filepath.Join(f.svc.ConfigDir(), "games", "g0", "profiles")
	require.NoError(t, os.Chmod(g0Dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(g0Dir, 0o755) })
	f.exec(t, `CREATE TRIGGER refuse_g0 BEFORE INSERT ON db_meta
		WHEN NEW.key = 'profile_disabled_backfill:g0/default'
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	f.owe(t)

	_, err = f.svc.BackfillProfileDisabledMarkers(ctx)
	require.ErrorContains(t, err, "injected failure")
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"), "g1 is marked all the same")
	owed, err := f.svc.ProfileDisabledBackfillOwedForTest(ctx)
	require.NoError(t, err)
	assert.Contains(t, owed, "profile_disabled_backfill", "still owed: g0's share was not kept")

	f.exec(t, `DROP TRIGGER refuse_g0`)
	f.warnings.Reset()
	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Marked, "g1's marker is already there")
	require.Len(t, report.Skipped, 1)
	assert.Equal(t, "g0", report.Skipped[0].GameID)
	owed, err = f.svc.ProfileDisabledBackfillOwedForTest(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"profile_disabled_backfill:g0/default"}, slices.Collect(maps.Keys(owed)))
}

// TestBackfillProfileDisabledMarkers_AnOpenNeverWaitsForTheLock is fix
// round 3's F5. The backfill at app.Open took the cross-process lock with a
// mutation's bounded wait, so while another lmm held it, every read-only
// command stalled two seconds and printed a warning - on every run, for as
// long as a changed pending profile, or the whole obligation, stayed owed.
// An open only ever tries the lock; a mutation's own slot, or the next
// uncontended open, does the work.
func TestBackfillProfileDisabledMarkers_AnOpenNeverWaitsForTheLock(t *testing.T) {
	ctx := context.Background()

	t.Run("the whole obligation", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		f.owe(t)

		release := holdOpLock(t, f.lockPath)
		started := time.Now()
		report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
		require.NoError(t, err)
		assert.Nil(t, report)
		assert.Less(t, time.Since(started), 500*time.Millisecond)
		assert.Empty(t, f.warnings.String())
		assert.Empty(t, f.disabledRefs(t, "a"))
		release()

		report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
		require.NoError(t, err)
		require.Len(t, report.Marked, 1)
	})

	t.Run("a changed pending profile", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		dir := filepath.Dir(f.profilePath("a"))
		require.NoError(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		f.owe(t)
		report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
		require.NoError(t, err)
		require.Len(t, report.Skipped, 1)

		require.NoError(t, os.Chmod(dir, 0o755)) // repaired...
		f.warnings.Reset()
		release := holdOpLock(t, f.lockPath) // ...while another lmm is busy
		for range 2 {
			started := time.Now()
			report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
			require.NoError(t, err)
			assert.Nil(t, report)
			assert.Less(t, time.Since(started), 500*time.Millisecond)
		}
		assert.Empty(t, f.warnings.String())
		release()

		report, err = f.svc.BackfillProfileDisabledMarkers(ctx)
		require.NoError(t, err)
		require.Len(t, report.Marked, 1)
		assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
	})
}

// TestBackfillProfileDisabledMarkers_RoundOnesKeyIsNotAnObligation is fix
// round 3's F6. Round 1's development builds recorded the backfill as DONE
// under `profile_disabled_backfill_done`, a key that shares the
// obligation's prefix - so on a database one of them touched the backfill
// looked owed forever. It is not an obligation, and the first mutation
// deletes it.
func TestBackfillProfileDisabledMarkers_RoundOnesKeyIsNotAnObligation(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	f.exec(t, `INSERT INTO db_meta (key, value) VALUES ('profile_disabled_backfill_done', '2026-09-14T00:00:00Z')`)
	svc, warnings := f.reopen(t)

	report, err := svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	assert.Nil(t, report)
	assert.Empty(t, f.disabledRefs(t, "a"), "nothing is owed, so nothing is marked")

	f.row(t, "b", "x", true, true)
	_, err = svc.DisableMod(ctx, f.game, "b", "src", "x")
	require.NoError(t, err)
	assert.Empty(t, warnings.String())
	owed, err := svc.ProfileDisabledBackfillOwedForTest(ctx)
	require.NoError(t, err)
	assert.Empty(t, owed, "the stray key is gone")
	assert.Empty(t, f.disabledRefs(t, "a"))
}

// declinedWrite is what the profile editor seam returns to model a profile
// file the backfill cannot write now - an unwritable file, or an editor
// panic, both of which keep the profile's share for later. (A layout the
// editor cannot edit used to as well; it is rewritten whole now, #441
// review F11 - backfill_layout_test.go.)
var declinedWrite = &fs.PathError{Op: "open", Path: "a.yaml", Err: fs.ErrPermission}

// pendingSwitchBack leaves profile a's share of the backfill kept for later
// - declined by the editor - with b the active profile. The switch to b
// rewrote a's file (SetDefault), so the next mutation retries a, whichever
// profile is active by then.
func pendingSwitchBack(t *testing.T, how string) *backfillFixture {
	t.Helper()
	f := newBackfillFixture(t)
	f.row(t, "a", "off", false, false)
	f.row(t, "b", "x", true, false)
	require.Equal(t, "declined", how)
	f.svc.SetProfileMarkerForTest(func(string, []domain.ModReference) ([]domain.ModReference, error) {
		return nil, declinedWrite
	})
	f.owe(t)
	report, err := f.svc.BackfillProfileDisabledMarkers(context.Background())
	require.NoError(t, err)
	require.Len(t, report.Skipped, 1, "a's share is kept for later")
	require.Empty(t, report.Marked)

	f.switchTo(t, "b")
	f.svc.SetProfileMarkerForTest(nil) // the editor takes the changed file
	require.Empty(t, f.disabledRefs(t, "a"))
	f.warnings.Reset()
	return f
}

// assertOffAndUndeployed checks that profile a's mod "off" is still marked,
// disabled and not in the game directory.
func (f *backfillFixture) assertOffAndUndeployed(t *testing.T) {
	t.Helper()
	assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
	row, err := f.svc.GetInstalledMod(context.Background(), "src", "off", f.game.ID, "a")
	require.NoError(t, err)
	assert.False(t, row.Enabled, "switched back on")
	assert.False(t, row.Deployed, "deployed")
	assert.NoFileExists(t, filepath.Join(f.gameDir, "off.esp"))
}

// TestBackfillProfileDisabledMarkers_ASwitchIntoAKeptProfile is the merge
// gate's G1. A profile whose share of the backfill was kept for later is
// retried by the first mutation after its file changes, and a switch away
// from it changes it. The switch back was planned from the unmarked
// document; its own slot wrote the marker; and the same Apply, whose
// snapshot of the target left markers out, enabled and deployed the mod the
// notice had just recorded as disabled - #431's own failure, in `lmm serve`
// with no contention at all.
func TestBackfillProfileDisabledMarkers_ASwitchIntoAKeptProfile(t *testing.T) {
	ctx := context.Background()
	for _, how := range []string{"declined"} {
		t.Run(how, func(t *testing.T) {
			// The plan settles the retry before it reads the document, so
			// the ordinary case plans the switch right the first time.
			t.Run("the plan settles it first", func(t *testing.T) {
				f := pendingSwitchBack(t, how)

				plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "a")
				require.NoError(t, err)
				assert.Empty(t, plan.ToEnable)
				assert.Contains(t, f.warnings.String(), "Mod off", "the plan recorded it and said so")

				_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
				require.NoError(t, err)
				f.assertOffAndUndeployed(t)
			})

			// Another lmm holds the lock while the plan is made, so the plan
			// goes ahead unsettled; its Apply writes the marker and refuses.
			t.Run("a plan made under another lmm's lock is stale", func(t *testing.T) {
				f := pendingSwitchBack(t, how)

				release := holdOpLock(t, f.lockPath)
				plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "a")
				require.NoError(t, err)
				require.Len(t, plan.ToEnable, 1, "planned before the marker existed")
				release()

				_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
				require.ErrorIs(t, err, core.ErrStalePlan)
				assert.Contains(t, err.Error(), "g1/a")
				assert.Contains(t, err.Error(), "disabled markers", "a marker is what changed")
				assert.NotContains(t, err.Error(), "installed mods", "no installed mod changed")
				assert.Contains(t, f.warnings.String(), "Mod off")
				f.assertOffAndUndeployed(t)

				plan, err = f.svc.PlanProfileSwitch(ctx, f.game, "a")
				require.NoError(t, err)
				assert.Empty(t, plan.ToEnable, "the re-plan reads the marker")
				_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
				require.NoError(t, err)
				f.assertOffAndUndeployed(t)
			})
		})
	}

	// Another lmm writes the marker between the document read the switch
	// decides from and the snapshot it records.
	t.Run("a marker written while the plan is being made", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		f.switchTo(t, "b")
		other, _ := f.reopen(t)

		var plan *core.SwitchPlan
		core.AfterProfileLoadForTest(func(loads int) {
			if loads == 1 {
				require.NoError(t, other.NewProfileManager().SetModDisabled(ctx, f.game.ID, "a", "src", "off", true))
			}
		}, func() {
			var err error
			plan, err = f.svc.PlanProfileSwitch(ctx, f.game, "a")
			require.NoError(t, err)
			require.Len(t, plan.ToEnable, 1, "decided from the unmarked read")
		})
		_, err := f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
		require.ErrorIs(t, err, core.ErrStalePlan)
		f.assertOffAndUndeployed(t)
	})
}

// TestCheckPlanFresh_SaysWhatChanged: a plan refused because an installed
// mod moved says so, as it always has; one refused because only a
// `disabled:` marker moved names the marker instead (the merge gate's Q1).
func TestCheckPlanFresh_SaysWhatChanged(t *testing.T) {
	ctx := context.Background()

	t.Run("an installed mod", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		plan, err := f.svc.PlanProfileApply(ctx, f.game, "a")
		require.NoError(t, err)
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: "off", SourceID: "src", Name: "Mod off", Version: "2.0", GameID: f.game.ID},
			ProfileName: "a", UpdatePolicy: domain.UpdateNotify,
		}))

		_, err = f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
		require.ErrorIs(t, err, core.ErrStalePlan)
		assert.Equal(t, "plan is stale: installed mods changed since it was computed: g1/a", err.Error())
	})

	t.Run("a marker", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		f.row(t, "a", "on", false, false)
		pm := f.svc.NewProfileManager()
		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "a", "src", "on", true))
		plan, err := f.svc.PlanProfileSync(ctx, f.game, "a")
		require.NoError(t, err)
		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "a", "src", "off", true))
		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "a", "src", "on", false))

		_, err = f.svc.ApplyProfileSync(ctx, f.game, plan, nil)
		require.ErrorIs(t, err, core.ErrStalePlan)
		assert.Equal(t, "plan is stale: the disabled markers in profile g1/a changed since it was computed "+
			"(now marked: src:off; no longer marked: src:on)", err.Error())
	})
}

// TestBackfillProfileDisabledMarkers_AnUnchangedKeptProfileCostsAPlanNothing:
// a plan settles a kept profile only once its file has changed. Until then
// the retry would do nothing, so the plan does not wait on another lmm's
// lock for it.
func TestBackfillProfileDisabledMarkers_AnUnchangedKeptProfileCostsAPlanNothing(t *testing.T) {
	ctx := context.Background()
	f := newBackfillFixture(t)
	f.row(t, "a", "off", false, false)
	f.row(t, "b", "x", false, false)
	f.svc.SetProfileMarkerForTest(func(string, []domain.ModReference) ([]domain.ModReference, error) {
		return nil, declinedWrite
	})
	f.owe(t)
	report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
	require.NoError(t, err)
	require.Len(t, report.Skipped, 1)
	f.svc.SetProfileMarkerForTest(nil)

	release := holdOpLock(t, f.lockPath)
	defer release()
	started := time.Now()
	_, err = f.svc.PlanProfileApply(ctx, f.game, "a")
	require.NoError(t, err)
	_, err = f.svc.PlanProfileSync(ctx, f.game, "a")
	require.NoError(t, err)
	_, err = f.svc.PlanProfileSwitch(ctx, f.game, "b")
	require.NoError(t, err)
	assert.Less(t, time.Since(started), 250*time.Millisecond, "three plans, none of them waiting")
	assert.Empty(t, f.disabledRefs(t, "a"))
}

// writeDuplicated writes profile's document by hand, listing mod "off" once
// per entry of marks - marked disabled where the entry is true.
func (f *backfillFixture) writeDuplicated(t *testing.T, profile string, isDefault bool, marks ...bool) {
	t.Helper()
	var doc strings.Builder
	fmt.Fprintf(&doc, "name: %s\ngame_id: %s\nmods:\n", profile, f.game.ID)
	for _, off := range marks {
		doc.WriteString("  - source_id: src\n    mod_id: \"off\"\n    version: \"1.0\"\n")
		if off {
			doc.WriteString("    disabled: true\n")
		}
	}
	if isDefault {
		doc.WriteString("is_default: true\n")
	}
	require.NoError(t, os.WriteFile(f.profilePath(profile), []byte(doc.String()), 0o644))
}

// refCount is how many references profile's document holds.
func (f *backfillFixture) refCount(t *testing.T, profile string) int {
	t.Helper()
	p, err := f.svc.NewProfileManager().Get(context.Background(), f.game.ID, profile)
	require.NoError(t, err)
	return len(p.Mods)
}

// TestBackfillProfileDisabledMarkers_ADuplicatedReference: a hand edit can
// list one mod twice. The backfill marked only the first copy, `profile
// apply` read the LAST copy and switched the mod back on, and `profile
// sync` listed the unmarked copy for removal - through RemoveMod, which
// removes every copy, the marked one and its marker with it. Every flow now
// decides a listed-twice mod by its first reference, the backfill marks
// every copy, and a sync never removes any copy of a mod that still has a
// row (#444).
func TestBackfillProfileDisabledMarkers_ADuplicatedReference(t *testing.T) {
	ctx := context.Background()

	t.Run("the backfill marks every copy", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		f.writeDuplicated(t, "a", true, false, false)
		f.owe(t)

		report, err := f.svc.BackfillProfileDisabledMarkers(ctx)
		require.NoError(t, err)
		require.Len(t, report.Marked, 1, "one mod, reported once")
		assert.Equal(t, []string{"off", "off"}, f.disabledRefs(t, "a"))

		apply, err := f.svc.PlanProfileApply(ctx, f.game, "a")
		require.NoError(t, err)
		assert.True(t, apply.NoChanges, "apply leaves it off")
		sync, err := f.svc.PlanProfileSync(ctx, f.game, "a")
		require.NoError(t, err)
		assert.True(t, sync.NoChanges, "sync keeps both copies")
	})

	// A hand edit that marks one copy and not the other: the first copy
	// decides, in every flow.
	for _, tc := range []struct {
		name  string
		marks []bool
		off   bool
	}{
		{name: "first copy marked", marks: []bool{true, false}, off: true},
		{name: "first copy unmarked", marks: []bool{false, true}, off: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackfillFixture(t)
			f.row(t, "a", "off", false, false)
			f.row(t, "b", "off", true, false) // live under b once b is active
			f.switchTo(t, "b")
			f.writeDuplicated(t, "a", false, tc.marks...)

			apply, err := f.svc.PlanProfileApply(ctx, f.game, "a")
			require.NoError(t, err)
			switchPlan, err := f.svc.PlanProfileSwitch(ctx, f.game, "a")
			require.NoError(t, err)
			sync, err := f.svc.PlanProfileSync(ctx, f.game, "a")
			require.NoError(t, err)
			if tc.off {
				assert.Empty(t, apply.ToEnable, "apply")
				assert.Empty(t, switchPlan.ToEnable, "switch")
				assert.Len(t, switchPlan.ToDisable, 1, "switch takes b's live copy down")
				assert.True(t, sync.NoChanges, "sync keeps a marked mod")
			} else {
				assert.Len(t, apply.ToEnable, 1, "apply")
				assert.Len(t, switchPlan.ToEnable, 1, "switch")
				assert.Empty(t, switchPlan.ToDisable, "switch keeps b's live copy")
				// #444: a is not active, so its row's enabled = 0 is not a
				// choice the sync may act on - the first copy says "on",
				// and that is the profile's intent.
				assert.True(t, sync.NoChanges, "sync keeps a mod a non-active profile lists")
			}

			// Either way the sync leaves both copies, and the marked one
			// keeps its marker.
			_, err = f.svc.ApplyProfileSync(ctx, f.game, sync, nil)
			require.NoError(t, err)
			assert.Equal(t, []string{"off"}, f.disabledRefs(t, "a"))
			assert.Equal(t, 2, f.refCount(t, "a"), "no copy was removed")
		})
	}

	// deploy reads the same copy the converge flows do.
	for _, tc := range []struct {
		name     string
		marks    []bool
		deployed bool
	}{
		{name: "deploy, first copy marked", marks: []bool{true, false}, deployed: false},
		{name: "deploy, first copy unmarked", marks: []bool{false, true}, deployed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackfillFixture(t)
			f.row(t, "a", "off", true, false)
			f.writeDuplicated(t, "a", true, tc.marks...)

			plan, err := f.svc.PlanDeploy(ctx, f.game, "a", core.DeployOptions{})
			require.NoError(t, err)
			selected := slices.ContainsFunc(plan.Mods, func(m core.DeployPlanMod) bool {
				return m.Ref.ModID == "off" && m.Skipped == ""
			})
			assert.Equal(t, tc.deployed, selected)
			apply, err := f.svc.PlanProfileApply(ctx, f.game, "a")
			require.NoError(t, err)
			assert.Equal(t, !tc.deployed, len(apply.ToDisable) == 1, "apply agrees with deploy")
		})
	}

	t.Run("import, first copy marked", func(t *testing.T) {
		f := newBackfillFixture(t)
		doc := "name: c\ngame_id: g1\nmods:\n" +
			"  - {source_id: src, mod_id: new, disabled: true}\n" +
			"  - {source_id: src, mod_id: new}\n"
		plan, err := f.svc.PlanImport(ctx, f.game, []byte(doc))
		require.NoError(t, err)
		assert.Empty(t, plan.Missing, "nothing to fetch for a mod the document switches off")
	})

	// `lmm mod enable` and `lmm mod disable` write every copy, so lmm never
	// leaves a document whose copies disagree.
	t.Run("the toggle writes every copy", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.row(t, "a", "off", false, false)
		f.writeDuplicated(t, "a", true, true, true)
		pm := f.svc.NewProfileManager()

		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "a", "src", "off", false))
		assert.Empty(t, f.disabledRefs(t, "a"))
		require.NoError(t, pm.SetModDisabled(ctx, f.game.ID, "a", "src", "off", true))
		assert.Equal(t, []string{"off", "off"}, f.disabledRefs(t, "a"))
		assert.Equal(t, 2, f.refCount(t, "a"))
	})
}

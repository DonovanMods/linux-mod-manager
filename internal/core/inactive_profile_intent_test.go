package core_test

// #444: every `lmm profile switch a -> b` writes enabled = 0 onto a's rows
// for the mods a lists and b does not - mods the user wants ON in a, and
// off only because a is not the active profile. On a profile that is not
// active, that bit says nothing about what the user chose, so a flow acting
// on such a profile has to take intent from the document (#431's
// `disabled:` marker) instead. Each test below starts from exactly the
// state a switch made before the upgrade leaves behind: a's rows at
// enabled = 0 - deployed still 1 when the mod had been live, since that
// switch never cleared it - and a's document with no marker at all.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// switchedAway is a backfill fixture whose profile "a" was switched away
// from before the upgrade: its two mods' rows sit at enabled = 0 (deployed
// as given), its document lists both unmarked, and "b" is the active
// profile.
func switchedAway(t *testing.T, deployed bool) *backfillFixture {
	t.Helper()
	f := newBackfillFixture(t)
	f.row(t, "a", "m1", false, deployed)
	f.row(t, "a", "m2", false, deployed)
	require.NoError(t, f.svc.NewProfileManager().SetDefault(context.Background(), f.game.ID, "b"))
	return f
}

func TestProfileSync_ANonActiveProfileKeepsItsSwitchedAwayMods(t *testing.T) {
	for _, deployed := range []bool{true, false} {
		t.Run(fmt.Sprintf("deployed=%v", deployed), func(t *testing.T) {
			f := switchedAway(t, deployed)
			ctx := context.Background()
			before := mustRead(t, f.profilePath("a"))

			plan, err := f.svc.PlanProfileSync(ctx, f.game, "a")
			require.NoError(t, err)
			assert.Empty(t, plan.ToRemove, "a switched-away mod is not a leftover")
			assert.True(t, plan.NoChanges)
			assert.Empty(t, plan.Warnings, "nothing is in doubt: the document is the only intent a non-active profile has")

			_, err = f.svc.ApplyProfileSync(ctx, f.game, plan, nil)
			require.NoError(t, err)
			assert.Equal(t, before, mustRead(t, f.profilePath("a")),
				"the references, their load order and their pinned versions all survive")
		})
	}
}

// TestProfileSync_ARefWithNoRowIsStillRemoved is the limit of the rule
// above: a reference with no installed row at all, under any profile, is a
// genuine leftover.
func TestProfileSync_ARefWithNoRowIsStillRemoved(t *testing.T) {
	f := switchedAway(t, true)
	ctx := context.Background()
	require.NoError(t, f.svc.NewProfileManager().AddMod(ctx, f.game.ID, "a",
		domain.ModReference{SourceID: "src", ModID: "gone", Version: "1.0"}))

	plan, err := f.svc.PlanProfileSync(ctx, f.game, "a")
	require.NoError(t, err)
	require.Len(t, plan.ToRemove, 1)
	assert.Equal(t, "gone", plan.ToRemove[0].ModID)

	_, err = f.svc.ApplyProfileSync(ctx, f.game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, f.refCount(t, "a"), "only the unbacked reference went")
}

// TestProfileSync_TheActiveProfileKeepsAnUnmarkedDisabledModAndSaysSo: on
// the active profile the row's flag IS a choice, but the document still
// says the mod is on - a disagreement, not a leftover. Removing the
// reference would resolve it by throwing away the mod's load-order slot and
// pinned version, so the sync keeps it and says what to do.
func TestProfileSync_TheActiveProfileKeepsAnUnmarkedDisabledModAndSaysSo(t *testing.T) {
	f := newBackfillFixture(t) // "a" is the active profile
	ctx := context.Background()
	f.row(t, "a", "off", false, false)
	before := mustRead(t, f.profilePath("a"))

	plan, err := f.svc.PlanProfileSync(ctx, f.game, "a")
	require.NoError(t, err)
	assert.Empty(t, plan.ToRemove)
	assert.True(t, plan.NoChanges)
	require.Len(t, plan.Warnings, 1)
	assert.Contains(t, plan.Warnings[0], "Mod off")
	assert.Contains(t, plan.Warnings[0], "lmm mod disable")

	result, err := f.svc.ApplyProfileSync(ctx, f.game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, plan.Warnings, result.Warnings, "the result carries what the plan found")
	assert.Equal(t, before, mustRead(t, f.profilePath("a")))
}

// siblingSwitchedAway is newVersionRepairFixGame's version mismatch with a
// sibling profile "second" that holds the same mod at the same stale
// version, left by a pre-upgrade switch away from it: enabled = 0 but still
// claiming a symlink deployment. active names the profile made active.
func siblingSwitchedAway(t *testing.T, active string, primaryDeployed bool) (*core.Service, *domain.Game) {
	t.Helper()
	ctx := context.Background()
	svc, game := newVersionRepairFixGame(t, primaryDeployed)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})
	require.NoError(t, svc.SetModEnabledForTest(ctx, "test-src", "mod1", game.ID, "second", false))
	require.NoError(t, svc.SetModDeployed(ctx, "test-src", "mod1", game.ID, "second", true))
	require.NoError(t, svc.SetModLinkMethod(ctx, "test-src", "mod1", game.ID, "second", domain.LinkSymlink))
	require.NoError(t, svc.NewProfileManager().SetDefault(ctx, game.ID, active))
	return svc, game
}

// TestVerifyFix_ANonActiveSiblingIsNotRelinkedIntoTheGameDirectory: the
// sibling pass corrects every other profile's record of a renamed cache
// entry, and re-linked any that claimed a symlink deployment - which put a
// non-active profile's mod into the one game directory the active profile
// owns.
func TestVerifyFix_ANonActiveSiblingIsNotRelinkedIntoTheGameDirectory(t *testing.T) {
	svc, game := siblingSwitchedAway(t, "default", false)
	ctx := context.Background()

	result, _ := runVersionRepairFix(t, svc, game)

	assert.NoFileExists(t, filepath.Join(game.ModPath, "2"),
		"a profile that is not active has nothing in the game directory")
	sibling, err := svc.GetInstalledMod(ctx, "test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.0", sibling.Version, "its record is still corrected")
	f := mismatchFinding(t, result.Findings)
	assert.Contains(t, f.Note, "second", "the note says what was left alone")
	assert.Contains(t, f.Note, "not active")
	assert.Equal(t, 0, result.Warnings, "leaving it alone is the right outcome, not a failure")
}

// TestVerifyFix_TheActiveSiblingIsStillRelinked: when the profile verified
// is the non-active one, the active profile is the sibling, its files ARE
// the live ones, and a renamed cache entry leaves them dangling - so it is
// re-linked. The primary row, not active, is not.
func TestVerifyFix_TheActiveSiblingIsStillRelinked(t *testing.T) {
	svc, game := siblingSwitchedAway(t, "second", true)
	ctx := context.Background()
	require.NoError(t, svc.SetModEnabledForTest(ctx, "test-src", "mod1", game.ID, "second", true))
	deployed := filepath.Join(game.ModPath, "2")
	require.NoError(t, os.Remove(deployed))

	runVersionRepairFix(t, svc, game)

	content, err := os.ReadFile(deployed)
	require.NoError(t, err, "the active sibling's deployment is re-linked, and resolves")
	assert.Equal(t, "plugin content", string(content))
	link, err := os.Readlink(deployed)
	require.NoError(t, err)
	assert.Contains(t, link, filepath.Join("test-src-mod1", "1.0"))

	owned, err := svc.GetDeployedFilesForMod(ctx, game.ID, "second", "test-src", "mod1")
	require.NoError(t, err)
	assert.Equal(t, []string{"2"}, owned, "the active profile owns it")
}

// TestVerifyFix_ANonActivePrimaryIsNotRelinked is the same rule for the
// profile `verify --fix -p` names itself.
func TestVerifyFix_ANonActivePrimaryIsNotRelinked(t *testing.T) {
	svc, game := siblingSwitchedAway(t, "second", true)
	deployed := filepath.Join(game.ModPath, "2")
	require.NoError(t, os.Remove(deployed))

	result, _ := runVersionRepairFix(t, svc, game)

	assert.NoFileExists(t, deployed, "neither profile's row is live: default is not active, and second's is switched away")
	f := mismatchFinding(t, result.Findings)
	assert.Contains(t, f.Note, "not active")
}

// switchedAwaySnapshot builds a restore fixture with a second profile,
// "alt", switched away from before the upgrade (its mod's row at enabled = 0,
// its document unmarked), snapshots alt while "default" is active, and
// returns the snapshot's path.
func switchedAwaySnapshot(t *testing.T, deployed bool) (*core.Service, *domain.Game, string) {
	t.Helper()
	ctx := context.Background()
	svc, game, dataDir := newRestoreFixture(t)
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))

	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "alpha", "1.0", "Data/alpha.esp", []byte("alpha v1")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: "alpha", SourceID: "src", Name: "Alpha", Version: "1.0", GameID: game.ID},
		ProfileName:  "alt",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      false,
		Deployed:     deployed,
	}))
	seedProfileWithMod(t, svc, game.ID, "alt", "src", "alpha", "1.0")

	_, err := svc.CreateSnapshot(ctx, game, "alt", "alt-then")
	require.NoError(t, err)
	return svc, game, filepath.Join(dataDir, "snapshots", game.ID, "alt-then.json")
}

func restoreSnapshot(t *testing.T, svc *core.Service, game *domain.Game, name string) *core.SnapshotRestoreResult {
	t.Helper()
	ctx := context.Background()
	plan, err := svc.PlanSnapshotRestore(ctx, game, name)
	require.NoError(t, err)
	result, err := svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{NoSafetySnapshot: true}, nil)
	require.NoError(t, err)
	return result
}

// assertAlphaLive checks the restored profile has its mod on, deployed, and
// unmarked.
func assertAlphaLive(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	ctx := context.Background()
	row, err := svc.GetInstalledMod(ctx, "src", "alpha", game.ID, "alt")
	require.NoError(t, err)
	assert.True(t, row.Enabled, "a mod that was off only because alt was not active comes back on")
	assert.True(t, row.Deployed)
	assert.FileExists(t, filepath.Join(game.ModPath, "Data", "alpha.esp"))
	profile, err := svc.NewProfileManager().Get(ctx, game.ID, "alt")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.False(t, profile.Mods[0].Disabled, "and no marker is invented for it")
}

func TestSnapshotRestore_ANonActiveProfilesSwitchedAwayModsComeBackOn(t *testing.T) {
	for _, deployed := range []bool{true, false} {
		t.Run(fmt.Sprintf("deployed=%v", deployed), func(t *testing.T) {
			svc, game, _ := switchedAwaySnapshot(t, deployed)

			result := restoreSnapshot(t, svc, game, "alt-then")

			assertAlphaLive(t, svc, game)
			assert.Zero(t, result.Disabled)
			for _, note := range result.Notes {
				assert.NotContains(t, note, "Alpha", "a snapshot that says which profile was active leaves nothing in doubt")
			}
		})
	}
}

// TestSnapshotRestore_ASnapshotThatDoesNotSayWhichProfileWasActive: one
// recorded before the snapshot kept that fact cannot tell a switched-away
// mod from a disabled one. The row's flag is not trusted, and the restore
// says which mods it left on.
func TestSnapshotRestore_ASnapshotThatDoesNotSayWhichProfileWasActive(t *testing.T) {
	svc, game, path := switchedAwaySnapshot(t, true)
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(mustRead(t, path)), &doc))
	require.Equal(t, "default", doc["active_profile"], "a new snapshot records the active profile")
	delete(doc, "active_profile")
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	result := restoreSnapshot(t, svc, game, "alt-then")

	assertAlphaLive(t, svc, game)
	var noted bool
	for _, note := range result.Notes {
		noted = noted || (strings.Contains(note, "Alpha") && strings.Contains(note, "lmm mod disable"))
	}
	assert.True(t, noted, "the restore names the mod it left on: %q", result.Notes)
}

// TestSnapshotRestore_TheActiveProfilesUnmarkedDisabledModStaysOff is the
// other side: under the profile that was active when the snapshot was
// taken, a row at enabled = 0 and deployed = 0 is what `lmm mod disable`
// wrote before the marker existed, and the restore still honours it.
func TestSnapshotRestore_TheActiveProfilesUnmarkedDisabledModStaysOff(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()
	require.NoError(t, svc.NewProfileManager().SetDefault(ctx, game.ID, "default"))
	seedNamedInstalledMod(t, svc, game, "src", "sleeper", "Sleeper", "1.0", false,
		map[string][]byte{"Data/sleeper.esp": []byte("sleeper v1")})
	seedProfileWithMod(t, svc, game.ID, "default", "src", "sleeper", "1.0")
	_, err := svc.CreateSnapshot(ctx, game, "default", "sleeper-off")
	require.NoError(t, err)

	restoreSnapshot(t, svc, game, "sleeper-off")

	row, err := svc.GetInstalledMod(ctx, "src", "sleeper", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, row.Enabled)
	assert.NoFileExists(t, filepath.Join(game.ModPath, "Data", "sleeper.esp"))
}

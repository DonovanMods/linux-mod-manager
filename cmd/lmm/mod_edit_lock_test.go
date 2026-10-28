package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// setupDoModEditTest reuses setupDoModLockTest's service/game/source fixture
// (the same seedLockableMod seeding applies) and additionally resets mod
// edit's package-level flag globals, mirroring how setupDoModLockTest resets
// lock/unlock's own.
func setupDoModEditTest(t *testing.T) (*core.Service, *domain.Game, *fakeInstallSource) {
	t.Helper()

	svc, game, src := setupDoModLockTest(t)

	oldName, oldVersion, oldAuthor := editName, editVersion, editAuthor
	oldSource, oldID, oldProfile := editToSource, editToID, editProfile
	editName, editVersion, editAuthor, editToSource, editToID, editProfile = "", "", "", "", "", ""
	t.Cleanup(func() {
		editName, editVersion, editAuthor = oldName, oldVersion, oldAuthor
		editToSource, editToID, editProfile = oldSource, oldID, oldProfile
	})

	return svc, game, src
}

// TestDoModEdit_ReLink_LockedRef_Refuses guards #146 shape 1: re-linking a
// LOCKED ref (--to-source/--to-source-id) must refuse up front - the pre-fix code
// did RemoveMod (deleting the locked ref) then UpsertMod of a fresh ref with
// zero-value Locked, silently dropping the lock. Lock-wins (#97/#143): only
// explicit unlock releases the ref, so the edit must fail with ErrModLocked,
// name the unlock remedy, and leave BOTH records (DB row and profile ref)
// exactly as they were.
func TestDoModEdit_ReLink_LockedRef_Refuses(t *testing.T) {
	svc, game, src := setupDoModEditTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "src", "a", ""))
	src.AddMod(&domain.Mod{ID: "b", SourceID: "src", Name: "Mod B", Version: "2.0", GameID: game.ID}, nil)

	editToID = "b"
	err := doModEdit(context.Background(), svc, game, "a")

	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModLocked)
	// #294 (Ruling 5): the hand-worded re-link refusal is gone - a re-link
	// refusal is lockedRefUnlockOnlyMessage's canonical wording,
	// byte-for-byte, like every other unlock-only lock refusal (re-linking
	// ignores the locked version, unlike an install or `mod edit --version`).
	// internal/core/mod_edit_test.go pins that the old sentence is absent.
	assert.Equal(t, "mod is locked: Mod A is locked at v1.0 in profile default - unlock with 'lmm mod unlock -s src -p default a' first", err.Error())

	profile, loadErr := config.LoadProfile(configDir, game.ID, "default")
	require.NoError(t, loadErr)
	require.Len(t, profile.Mods, 1, "the locked ref must survive; no re-linked ref may be appended")
	assert.Equal(t, "a", profile.Mods[0].ModID)
	assert.True(t, profile.Mods[0].Locked, "the Locked marker must not be dropped")
	assert.Equal(t, "1.0", profile.Mods[0].Version)

	dbMod, dbErr := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, dbErr)
	require.NotNil(t, dbMod, "the DB row must keep its original source:id")
	assert.Equal(t, "1.0", dbMod.Version)
}

// TestDoModEdit_Version_LockedRef_RefusesBeforeDBWrite guards #146 shape 2:
// `mod edit --version` on a LOCKED ref must refuse BEFORE any state moves.
// The pre-fix code saved the DB row first and only then hit UpsertMod's
// ErrModLocked guard - demoted to a verbose-only warning - so default
// verbosity got success output plus silent DB-vs-profile divergence. The
// edit must instead fail with the standard lock refusal (both remedies) and
// leave the DB row untouched.
func TestDoModEdit_Version_LockedRef_RefusesBeforeDBWrite(t *testing.T) {
	svc, game, _ := setupDoModEditTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "src", "a", ""))

	editVersion = "2.0"
	err := doModEdit(context.Background(), svc, game, "a")

	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModLocked)
	assert.Contains(t, err.Error(), "lmm mod lock -s src -p default a <version>")
	assert.Contains(t, err.Error(), "lmm mod unlock -s src -p default a")

	dbMod, dbErr := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, dbErr)
	require.NotNil(t, dbMod)
	assert.Equal(t, "1.0", dbMod.Version, "the DB row must not move before the lock refusal")

	profile, loadErr := config.LoadProfile(configDir, game.ID, "default")
	require.NoError(t, loadErr)
	require.Len(t, profile.Mods, 1)
	assert.True(t, profile.Mods[0].Locked)
	assert.Equal(t, "1.0", profile.Mods[0].Version)
}

// TestDoModEdit_Version_MatchingLock_Allowed: a --version equal to the
// locked version is not a version MOVE - it realigns a diverged DB row with
// the lock target (the same allowance UpsertMod itself grants a same-version
// upsert). Constructed via SetModLock's explicit-version form so the ref is
// locked at 1.0 while the DB row still says 2.0 - the "lock pending
// convergence" state lmm verify reports.
func TestDoModEdit_Version_MatchingLock_Allowed(t *testing.T) {
	svc, game, _ := setupDoModEditTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "2.0")
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "src", "a", "1.0"))

	editVersion = "1.0"
	var err error
	_ = captureStdout(t, func() error {
		err = doModEdit(context.Background(), svc, game, "a")
		return err
	})

	require.NoError(t, err)

	dbMod, dbErr := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, dbErr)
	require.NotNil(t, dbMod)
	assert.Equal(t, "1.0", dbMod.Version)

	profile, loadErr := config.LoadProfile(configDir, game.ID, "default")
	require.NoError(t, loadErr)
	assert.True(t, profile.Mods[0].Locked, "realigning to the locked version must preserve the marker")
	assert.Equal(t, "1.0", profile.Mods[0].Version)
}

// TestDoModEdit_MetadataOnly_LockedRef_Allowed: --name/--author touch
// neither the ref's Version nor its identity, so a lock must not block them
// (a lock holds a version, it does not freeze display metadata).
func TestDoModEdit_MetadataOnly_LockedRef_Allowed(t *testing.T) {
	svc, game, _ := setupDoModEditTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "src", "a", ""))

	editName = "Renamed Mod A"
	var err error
	out := captureStdout(t, func() error {
		err = doModEdit(context.Background(), svc, game, "a")
		return err
	})

	require.NoError(t, err)
	assert.Contains(t, out, "name -> Renamed Mod A")

	dbMod, dbErr := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, dbErr)
	require.NotNil(t, dbMod)
	assert.Equal(t, "Renamed Mod A", dbMod.Name)

	profile, loadErr := config.LoadProfile(configDir, game.ID, "default")
	require.NoError(t, loadErr)
	assert.True(t, profile.Mods[0].Locked)
	assert.Equal(t, "1.0", profile.Mods[0].Version)
}

// TestModEditCmd_DoesNotShadowTheGroupsSourceShorthand is #396: `lmm mod`'s
// persistent -s/--source means "which source this mod is in", but
// `mod edit` declared a LOCAL --source meaning "re-link it to this source".
// The local flag replaced the persistent one outright, so
// `lmm mod edit alpha -s repo` failed with "unknown shorthand flag: 's'"
// and there was no way at all to say which of two same-ID mods to edit.
// The re-link target is --to-source/--to-source-id; -s belongs to the group.
func TestModEditCmd_DoesNotShadowTheGroupsSourceShorthand(t *testing.T) {
	// LocalFlags, not Flags: cobra merges the inherited persistent flags
	// into Flags() once anything has parsed, so Flags() legitimately holds
	// a "source" - the GROUP's. What must not exist is a local one.
	assert.Nil(t, modEditCmd.LocalFlags().Lookup("source"),
		"a local --source shadows the mod group's persistent -s/--source")
	assert.Nil(t, modEditCmd.LocalFlags().Lookup("source-id"),
		"renamed alongside --source so the pair stays consistent")
	require.NotNil(t, modEditCmd.LocalFlags().Lookup("to-source"))
	require.NotNil(t, modEditCmd.LocalFlags().Lookup("to-source-id"))

	// And the group's own flag, shorthand intact, is what `mod edit` now
	// resolves -s against.
	resolved := modEditCmd.Flags().Lookup("source")
	require.NotNil(t, resolved, "the mod group's --source must still reach mod edit")
	assert.Equal(t, "s", resolved.Shorthand)
}

// TestDoModEdit_AmbiguousModIDNamesTheSourceFlag is #396's other half
// (final review finding 3): two profiles entries can share a mod id across
// sources, and mod edit took whichever the scan hit first. It now says so
// and names the flag that decides it.
func TestDoModEdit_AmbiguousModIDNamesTheSourceFlag(t *testing.T) {
	svc, game, _ := setupDoModEditTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	seedSameIDModFromOtherSource(t, svc, game, "a", "Other Mod A", "9.9")
	modSource = "" // no -s: the ambiguity is the point

	editName = "Renamed"
	err := doModEdit(context.Background(), svc, game, "a")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "-s/--source")
	assert.Contains(t, err.Error(), "src")
	assert.Contains(t, err.Error(), "other")

	// #373: and it is the SHARED refusal `uninstall` and `update` give, not
	// a message worded a third time here - which is what puts the candidate
	// list in the --json envelope's details (see
	// TestReportError_JSON_AmbiguousModError below).
	var ambiguous *core.AmbiguousModError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, []string{"other", "src"}, ambiguous.Sources, "sorted, so the message is install-order independent")
	assert.Equal(t, "-s/--source", ambiguous.Flag)
}

// TestReportError_JSON_AmbiguousModError pins core.AmbiguousModError's wire
// shape in the --json error envelope (details_coverage_test.go's entry for
// it): a scripting caller gets the candidate sources and the flag that
// resolves them as data, not by parsing the sentence.
func TestReportError_JSON_AmbiguousModError(t *testing.T) {
	withJSONOutput(t)

	err := &core.AmbiguousModError{
		ModID: "alpha", Profile: "default",
		Sources: []string{"localmods", "repo"}, Flag: "-s/--source",
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"mod alpha is in profile default under multiple sources (localmods, repo); retry with -s/--source to choose\",\n"+
		"  \"details\": {\n"+
		"    \"mod_id\": \"alpha\",\n"+
		"    \"profile\": \"default\",\n"+
		"    \"sources\": [\n"+
		"      \"localmods\",\n"+
		"      \"repo\"\n"+
		"    ],\n"+
		"    \"flag\": \"-s/--source\"\n"+
		"  }\n"+
		"}\n", out)
}

// TestDoModEdit_SourceFlagPicksAmongSameIDMods is the resolution: with -s
// given, the right one is edited and the other is untouched.
func TestDoModEdit_SourceFlagPicksAmongSameIDMods(t *testing.T) {
	svc, game, _ := setupDoModEditTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	seedSameIDModFromOtherSource(t, svc, game, "a", "Other Mod A", "9.9")
	modSource = "other"

	editName = "Renamed"
	require.NoError(t, doModEdit(context.Background(), svc, game, "a"))

	edited, err := svc.GetInstalledMod(context.Background(), "other", "a", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "Renamed", edited.Name)

	untouched, err := svc.GetInstalledMod(context.Background(), "src", "a", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "Mod A", untouched.Name)
}

// seedSameIDModFromOtherSource is seedLockableMod for a SECOND registered
// source mapped to the same game, so a profile can hold two entries with
// the same mod id - the shape #396's disambiguation is about. (Distinct
// from uninstall_purge_dry_run_golden_test.go's seedSecondSourceMod, which
// seeds a cache entry and registers nothing.)
func seedSameIDModFromOtherSource(t *testing.T, svc *core.Service, game *domain.Game, modID, name, version string) {
	t.Helper()
	other := newFakeInstallSource("other")
	t.Cleanup(other.Close)
	svc.RegisterSource(other)
	game.SourceIDs["other"] = game.ID

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "other", Name: name, Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default",
		domain.ModReference{SourceID: "other", ModID: modID, Version: version}))
}

// TestREADMEDoesNotNameTheRenamedRelinkFlags is P1b review F3: #396 renamed
// `mod edit`'s re-link pair to --to-source/--to-source-id, and the wave
// updated the man page and the README's import-scan mentions but missed the
// lock caveat, which still told readers to re-link with --source/--source-id.
// The README is prose nobody executes, so nothing else notices it going
// stale (the shortcuts ratchet's own reasoning) - and --source-id in
// particular now names no flag of any lmm command at all.
func TestREADMEDoesNotNameTheRenamedRelinkFlags(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	require.NoError(t, err)
	text := string(readme)

	assert.NotContains(t, text, "--source-id",
		"no lmm command has a --source-id flag since #396; the re-link flag is --to-source-id")
	assert.NotContains(t, text, "`--source`/",
		"the re-link pair is --to-source/--to-source-id; the mod group's -s/--source means the opposite thing")
	assert.Contains(t, text, "`--to-source`/`--to-source-id` re-linking",
		"the lock caveat must name the flags that actually re-link")
}

// TestDoModEdit_ZeroMatchesForASourceFilterNamesTheRelinkFlag is P1b review
// F4. Of the two flags #396 removed, --source-id fails loudly ("unknown
// flag"); --source does not - it resolves to the mod group's persistent
// -s/--source, which means the opposite thing. So the old
// `lmm mod edit alpha --source curseforge` is silently reinterpreted as a
// filter and reports "not found ... for source curseforge", a message that
// says nothing about the rename. There is deliberately no alias (the
// shadowing IS #396's defect), so the 0-match branch carries the pointer.
func TestDoModEdit_ZeroMatchesForASourceFilterNamesTheRelinkFlag(t *testing.T) {
	svc, game, _ := setupDoModEditTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.0")
	modSource = "curseforge" // the mod is in "src", so this filters it out

	editName = "Renamed"
	err := doModEdit(context.Background(), svc, game, "a")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "curseforge")
	assert.Contains(t, err.Error(), "--to-source",
		"a user who typed the pre-#396 --source must be told which flag re-links")
}

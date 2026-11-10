package core_test

// The --fix version-record repair (repairModVersion and its sibling pass)
// beyond the headline scenarios in verify_test.go: the failure orderings
// that keep a retry convergent, checksum preservation, the sibling pass's
// failure accounting, and the profile-effective link method on a re-link.
// These were CLI tests (cmd/lmm/verify_test.go) until #600 moved them here,
// beside the engine they exercise; they share verify_test.go's
// newVersionRepairFixGame fixture: "default" records test-src/mod1 at 1.5
// while the source says its file "2" is 1.0.

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

// skipIfRoot skips a permission-based test, which root would pass for the
// wrong reason.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}
}

// chmodForTest changes path's mode and restores it before t.TempDir's own
// cleanup has to remove it.
func chmodForTest(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.Chmod(path, mode))
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

// versionRepairProfilePath is a profile's YAML document under svc's config
// dir.
func versionRepairProfilePath(svc *core.Service, game *domain.Game, profileName string) string {
	return filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles", profileName+".yaml")
}

// installedVersion reads a profile's recorded version of test-src/mod1.
func installedVersion(t *testing.T, svc *core.Service, game *domain.Game, profileName string) string {
	t.Helper()
	mod, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, profileName)
	require.NoError(t, err)
	return mod.Version
}

// storedFileChecksum is profileName's stored checksum for mod1's file "2".
func storedFileChecksum(t *testing.T, svc *core.Service, game *domain.Game, profileName string) string {
	t.Helper()
	files, err := svc.GetFilesWithChecksums(context.Background(), game.ID, profileName)
	require.NoError(t, err)
	for _, f := range files {
		if f.ModID == "mod1" && f.FileID == "2" {
			return f.Checksum
		}
	}
	t.Fatalf("mod1's file 2 is no longer tracked in %s: %+v", profileName, files)
	return ""
}

// deploySiblingRow marks profileName's row deployed by symlink through the
// targeted setters - a full SaveInstalledMod upsert would wipe the row's
// checksum - and makes that profile the active one, since only the active
// profile's rows are re-linked.
func deploySiblingRow(t *testing.T, svc *core.Service, game *domain.Game, profileName string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.NewProfileManager().SetDefault(ctx, game.ID, profileName))
	require.NoError(t, svc.SetModDeployed(ctx, "test-src", "mod1", game.ID, profileName, true))
	require.NoError(t, svc.SetModLinkMethod(ctx, "test-src", "mod1", game.ID, profileName, domain.LinkSymlink))
}

// detailsContaining is the repair details whose text contains sub.
func detailsContaining(events []core.VerifyEvent, sub string) []string {
	var out []string
	for _, d := range repairDetails(events) {
		if strings.Contains(d.Detail, sub) {
			out = append(out, d.Detail)
		}
	}
	return out
}

// TestVerify_Fix_VersionMismatch_Deployed_RelinksIntoTheRenamedEntry: once
// the cache entry is renamed to the effective version, a symlinked
// deployment pointing into the old entry would dangle, so --fix re-links it
// into the renamed one.
func TestVerify_Fix_VersionMismatch_Deployed_RelinksIntoTheRenamedEntry(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, true)
	deployed := filepath.Join(game.ModPath, "2")
	content, err := os.ReadFile(deployed)
	require.NoError(t, err, "pre-state: the symlink resolves before the rename")
	require.Equal(t, "plugin content", string(content))

	result, _ := runVersionRepairFix(t, svc, game)

	assert.Equal(t, 0, result.Issues)
	info, err := os.Lstat(deployed)
	require.NoError(t, err)
	assert.True(t, info.Mode()&os.ModeSymlink != 0, "the deployment is still a symlink")
	content, err = os.ReadFile(deployed)
	require.NoError(t, err, "the symlink resolves, not dangles")
	assert.Equal(t, "plugin content", string(content))
	target, err := os.Readlink(deployed)
	require.NoError(t, err)
	assert.Contains(t, target, filepath.Join("test-src-mod1", "1.0"), "it points into the renamed entry")
}

// TestVerify_Fix_VersionMismatch_RelinkFailure_SecondRunIsClean: after a
// failed re-link has cleared Deployed, the record is already at the
// effective version, so once the game directory is writable again a second
// --fix run has nothing to report - no re-detected mismatch, and no NO
// CHECKSUM from a repair that wiped the stored checksum (audit Finding 8,
// the net for Finding 1). The orphaned symlink the failed run left behind
// is swept by convergence.
func TestVerify_Fix_VersionMismatch_RelinkFailure_SecondRunIsClean(t *testing.T) {
	skipIfRoot(t)
	svc, game := newVersionRepairFixGame(t, true)
	chmodForTest(t, game.ModPath, 0o555)

	first, _ := runVersionRepairFix(t, svc, game)
	require.Equal(t, 1, first.Issues, "sanity: the re-link failed")

	require.NoError(t, os.Chmod(game.ModPath, 0o755))
	second, _ := runVersionRepairFix(t, svc, game)

	assert.Equal(t, 0, second.Issues, "the version is already corrected; nothing is re-reported")
	assert.Equal(t, 0, second.Warnings, "the second run is clean, with no NO CHECKSUM or other new finding")
	assert.Equal(t, "deadbeef", storedFileChecksum(t, svc, game, "default"))
	mod, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Deployed, "nothing re-marks the row deployed on its own")
}

// TestVerify_Fix_VersionMismatch_RenameFails_LeavesRecordUnchanged: a
// genuine os.Rename failure (the entry's parent made read-only) must stop
// the repair before any DB write, or the DB and the cache would disagree
// in a new way. The row stays version_mismatch with the reason in its note.
func TestVerify_Fix_VersionMismatch_RenameFails_LeavesRecordUnchanged(t *testing.T) {
	skipIfRoot(t)
	svc, game := newVersionRepairFixGame(t, false)
	gameCache := svc.GetGameCache(game)
	chmodForTest(t, filepath.Dir(gameCache.ModPath(game.ID, "test-src", "mod1", "1.5")), 0o500)

	result, _ := runVersionRepairFix(t, svc, game)

	assert.Equal(t, 1, result.Issues, "a failed rename repairs nothing")
	f := mismatchFinding(t, result.Findings)
	assert.Equal(t, "version_mismatch", f.Status)
	assert.NotEmpty(t, f.Note, "the failure reason reaches the finding (audit Finding 7)")
	assert.Equal(t, "1.5", installedVersion(t, svc, game, "default"), "no DB write when the rename itself fails")
	assert.True(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.5"))
	assert.False(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.0"))
}

// TestVerify_Fix_VersionMismatch_PreservesFileChecksum guards audit
// Finding 1: a repair that saved the row with a full upsert replaced
// installed_mod_files and wiped every stored checksum, so the next verify
// reported NO CHECKSUM and the next --fix redownloaded. The checksum
// survives, and a second run is clean.
func TestVerify_Fix_VersionMismatch_PreservesFileChecksum(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	require.Equal(t, "deadbeef", storedFileChecksum(t, svc, game, "default"), "sanity: the fixture seeds a checksum")

	first, _ := runVersionRepairFix(t, svc, game)
	require.Equal(t, "ok", mismatchFinding(t, first.Findings).Status, "sanity: the repair ran")

	assert.Equal(t, "deadbeef", storedFileChecksum(t, svc, game, "default"), "a repair must not wipe the stored checksum")
	second, _ := runVersionRepairFix(t, svc, game)
	assert.Equal(t, 0, second.Issues)
	assert.Equal(t, 0, second.Warnings, "the second run is clean")
}

// TestVerify_Fix_VersionMismatch_RetryAfterPartialFailure_StillRepairsSiblings
// guards audit Finding 2: the sibling pass was gated on a rename made THIS
// run, so a retry after a run whose rename succeeded but whose later steps
// failed skipped the siblings, though the shared entry had already moved.
// The cache already lives at the effective version while both rows still
// record the old one.
func TestVerify_Fix_VersionMismatch_RetryAfterPartialFailure_StillRepairsSiblings(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.Rename(
		gameCache.ModPath(game.ID, "test-src", "mod1", "1.5"),
		gameCache.ModPath(game.ID, "test-src", "mod1", "1.0"),
	), "a prior run's completed rename")

	runVersionRepairFix(t, svc, game)

	assert.Equal(t, "1.0", installedVersion(t, svc, game, "second"),
		"the sibling is repaired though no rename happened this run")
}

// TestVerify_Fix_VersionMismatch_ProfileUpsertFails_LeavesDBForRetry guards
// audit Finding 3: the profile upsert runs before the DB write, so the DB
// write is the "done" signal. When the upsert fails the DB still records
// the old version, and the next run still sees the mismatch instead of a
// falsely converged record over a stale profile document.
func TestVerify_Fix_VersionMismatch_ProfileUpsertFails_LeavesDBForRetry(t *testing.T) {
	skipIfRoot(t)
	svc, game := newVersionRepairFixGame(t, false)
	path := versionRepairProfilePath(svc, game, "default")
	require.NoError(t, os.Chmod(path, 0o444))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	_, events := runVersionRepairFix(t, svc, game)

	assert.NotEmpty(t, detailsContaining(events, "Repair failed"), "the failure is visible, not silent")
	assert.Equal(t, "1.5", installedVersion(t, svc, game, "default"), "no DB write when the profile upsert fails")

	second, _ := runVersionRepairFix(t, svc, game)
	assert.Equal(t, "version_mismatch", mismatchFinding(t, second.Findings).Status,
		"the retry still detects the mismatch")
}

// TestVerify_Fix_VersionMismatch_OldPathStatErrorBlocksRepair guards audit
// Finding 4: a stat error on the old entry that is not "does not exist"
// (here the parent is 0o000, so stat cannot traverse it) must block the
// repair like a rename failure, not read as nothing-to-rename.
func TestVerify_Fix_VersionMismatch_OldPathStatErrorBlocksRepair(t *testing.T) {
	skipIfRoot(t)
	svc, game := newVersionRepairFixGame(t, false)
	gameCache := svc.GetGameCache(game)
	chmodForTest(t, filepath.Dir(gameCache.ModPath(game.ID, "test-src", "mod1", "1.5")), 0o000)

	runVersionRepairFix(t, svc, game)

	assert.Equal(t, "1.5", installedVersion(t, svc, game, "default"), "a stat failure blocks the repair entirely")
}

// TestVerify_Fix_VersionMismatch_SiblingNotInstalled_SkippedSilently: a
// profile that never had the mod (GetInstalledMod's ErrModNotFound) is not
// a sibling candidate - a silent skip, not a warning or a failed sibling.
func TestVerify_Fix_VersionMismatch_SiblingNotInstalled_SkippedSilently(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "fourth")
	require.NoError(t, err)

	result, events := runVersionRepairFix(t, svc, game)

	assert.Equal(t, 0, result.Warnings)
	assert.Empty(t, detailsContaining(events, "fourth"), "a profile without the mod is never mentioned")
	assert.NotContains(t, mismatchFinding(t, result.Findings).Note, "fourth")
	_, err = svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, "fourth")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "sanity: this is the not-installed path")
}

// TestVerify_Fix_VersionMismatch_RenameBlocked_SiblingsUntouched: when the
// rename is blocked, nothing moved, so a sibling at the same stale version
// is not orphaned and must be left as it was.
func TestVerify_Fix_VersionMismatch_RenameBlocked_SiblingsUntouched(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "test-src", "mod1", "1.0", "2", []byte("pre-existing 1.0 content")))

	runVersionRepairFix(t, svc, game)

	assert.Equal(t, "1.5", installedVersion(t, svc, game, "second"))
}

// TestVerify_Fix_VersionMismatch_SiblingLockedAndDeployed_WarnsDeploymentMayBeBroken:
// a locked sibling that is also deployed is still declined, and since the
// primary repair renamed the shared entry from under it, its decline
// warning says the deployment may be broken - in the same line.
func TestVerify_Fix_VersionMismatch_SiblingLockedAndDeployed_WarnsDeploymentMayBeBroken(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})
	ctx := context.Background()
	require.NoError(t, svc.NewProfileManager().SetModLock(ctx, game.ID, "second", "test-src", "mod1", ""))
	require.NoError(t, svc.SetModDeployed(ctx, "test-src", "mod1", game.ID, "second", true))
	require.NoError(t, svc.SetModLinkMethod(ctx, "test-src", "mod1", game.ID, "second", domain.LinkSymlink))

	_, events := runVersionRepairFix(t, svc, game)

	warnings := detailsContaining(events, "Mod One is locked at v1.5 in profile second")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "its deployment may be broken until the lock is moved or cleared")
	assert.Equal(t, "1.5", installedVersion(t, svc, game, "second"), "still not repaired")
}

// TestVerify_Fix_VersionMismatch_SiblingDeployed_RelinkKeepsDeployedAndChecksum:
// a deployed sibling (the active profile) is re-linked like the primary
// would be; it stays Deployed and keeps its stored checksum.
func TestVerify_Fix_VersionMismatch_SiblingDeployed_RelinkKeepsDeployedAndChecksum(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "test-src", "mod1", game.ID, "second", "2", "deadbeef"))
	deploySiblingRow(t, svc, game, "second")

	result, _ := runVersionRepairFix(t, svc, game)

	assert.Equal(t, 0, result.Issues)
	content, err := os.ReadFile(filepath.Join(game.ModPath, "2"))
	require.NoError(t, err, "the sibling's symlink resolves")
	assert.Equal(t, "plugin content", string(content))
	assert.Equal(t, "deadbeef", storedFileChecksum(t, svc, game, "second"), "the sibling repair keeps the checksum")
	mod, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.True(t, mod.Deployed)
}

// TestVerify_Fix_VersionMismatch_SiblingRelinkFails_ClearsDeployedAndWarns:
// a sibling whose own re-link fails keeps its version correction but
// clears Deployed, and the failure is a warning and a FAILED note - never
// folded into a "Repaired (profile second)" success (PR #128 Claim 2).
func TestVerify_Fix_VersionMismatch_SiblingRelinkFails_ClearsDeployedAndWarns(t *testing.T) {
	skipIfRoot(t)
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "test-src", "mod1", game.ID, "second", "2", "deadbeef"))
	deploySiblingRow(t, svc, game, "second")
	chmodForTest(t, game.ModPath, 0o555)

	result, events := runVersionRepairFix(t, svc, game)

	f := mismatchFinding(t, result.Findings)
	assert.Contains(t, f.Note, "second")
	assert.Contains(t, f.Note, "FAILED")
	assert.Empty(t, detailsContaining(events, "Repaired (profile second)"), "a failed sibling is not a success")
	var warned bool
	for _, d := range detailsContaining(events, "Warning") {
		warned = warned || strings.Contains(d, "second")
	}
	assert.True(t, warned, "a warning names the sibling: %+v", repairDetails(events))

	mod, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, "1.0", mod.Version, "the sibling's version correction stands")
	assert.False(t, mod.Deployed, "Deployed is cleared when the re-link fails")
	assert.Equal(t, "1.0", installedVersion(t, svc, game, "default"), "the primary is still repaired")
	assert.Equal(t, "deadbeef", storedFileChecksum(t, svc, game, "second"))
}

// TestVerify_Fix_VersionMismatch_SiblingUpsertFails_CountsAWarning: a
// sibling whose profile document cannot be written is surfaced (a warning
// detail naming it, a FAILED note) and counted in Warnings (PR #128 round
// 4), while the best-effort loop leaves every other profile alone.
func TestVerify_Fix_VersionMismatch_SiblingUpsertFails_CountsAWarning(t *testing.T) {
	skipIfRoot(t)
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})
	addVersionRepairSibling(t, svc, game, "third", "2.0", []string{"2"})
	path := versionRepairProfilePath(svc, game, "second")
	require.NoError(t, os.Chmod(path, 0o444))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	result, events := runVersionRepairFix(t, svc, game)

	assert.Equal(t, 0, result.Issues, "the primary repair succeeds")
	assert.Equal(t, 1, result.Warnings, "the failed sibling counts as a warning")
	f := mismatchFinding(t, result.Findings)
	assert.Contains(t, f.Note, "second")
	assert.Contains(t, f.Note, "FAILED")
	var warned bool
	for _, d := range detailsContaining(events, "Warning") {
		warned = warned || strings.Contains(d, "second")
	}
	assert.True(t, warned, "a warning names the sibling: %+v", repairDetails(events))
	assert.Equal(t, "2.0", installedVersion(t, svc, game, "third"), "a non-candidate is untouched")
}

// TestVerify_Fix_VersionMismatch_SiblingListFails_WarnsOnce: when the
// profiles directory cannot be listed (0o311: no read bit), sibling
// discovery fails once - one warning, not one per sibling - while the
// primary repair, which opens its own document by path, still goes
// through. The second warning is convergence, which removes nothing while
// it cannot list the profiles to tell which is active (#462).
func TestVerify_Fix_VersionMismatch_SiblingListFails_WarnsOnce(t *testing.T) {
	skipIfRoot(t)
	svc, game := newVersionRepairFixGame(t, false)
	chmodForTest(t, filepath.Dir(versionRepairProfilePath(svc, game, "default")), 0o311)

	result, events := runVersionRepairFix(t, svc, game)

	assert.Len(t, detailsContaining(events, "Warning"), 1, "the list failure surfaces exactly once: %+v", repairDetails(events))
	assert.Equal(t, 2, result.Warnings, "the list failure and the convergence refusal")
	assert.Equal(t, "1.0", installedVersion(t, svc, game, "default"), "the primary is still repaired")
}

// TestVerify_Fix_VersionMismatch_Deployed_RelinksWithProfileLinkMethod is
// #152: the re-link resolves the profile-effective link method (profile >
// game > global), so a profile's explicit copy beats the game's explicit
// symlink, and the row records the method it used, as DeployProfile does.
func TestVerify_Fix_VersionMismatch_Deployed_RelinksWithProfileLinkMethod(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, true)
	game.LinkMethod = domain.LinkSymlink
	game.LinkMethodExplicit = true
	setProfileLinkMethod(t, svc, game.ID, "default", domain.LinkCopy)
	deployed := filepath.Join(game.ModPath, "2")
	info, err := os.Lstat(deployed)
	require.NoError(t, err)
	require.True(t, info.Mode()&os.ModeSymlink != 0, "pre-state: a symlink")

	result, _ := runVersionRepairFix(t, svc, game)
	require.Equal(t, "ok", mismatchFinding(t, result.Findings).Status, "sanity: the repair ran")

	info, err = os.Lstat(deployed)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "a real copy: the profile's override wins")
	content, err := os.ReadFile(deployed)
	require.NoError(t, err)
	assert.Equal(t, "plugin content", string(content))
	mod, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, domain.LinkCopy, mod.LinkMethod)
	assert.True(t, mod.Deployed)
}

// TestVerify_Fix_VersionMismatch_SiblingDeployed_RelinksWithSiblingProfileLinkMethod
// is #152's second re-link site: a deployed sibling re-links with ITS OWN
// profile's effective method - not the game's, and not the primary's
// ("default" has no override, so only resolving against the sibling's name
// yields copy).
func TestVerify_Fix_VersionMismatch_SiblingDeployed_RelinksWithSiblingProfileLinkMethod(t *testing.T) {
	svc, game := newVersionRepairFixGame(t, false)
	addVersionRepairSibling(t, svc, game, "second", "1.5", []string{"2"})
	game.LinkMethod = domain.LinkSymlink
	game.LinkMethodExplicit = true
	setProfileLinkMethod(t, svc, game.ID, "second", domain.LinkCopy)
	deploySiblingRow(t, svc, game, "second")

	_, events := runVersionRepairFix(t, svc, game)
	require.NotEmpty(t, detailsContaining(events, "Repaired (profile second)"), "sanity: the sibling repair ran")

	deployed := filepath.Join(game.ModPath, "2")
	info, err := os.Lstat(deployed)
	require.NoError(t, err, "the sibling's deployment was (re-)created")
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "a real copy: the sibling's own override wins")
	content, err := os.ReadFile(deployed)
	require.NoError(t, err)
	assert.Equal(t, "plugin content", string(content))
	mod, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, "second")
	require.NoError(t, err)
	assert.Equal(t, domain.LinkCopy, mod.LinkMethod)
	assert.True(t, mod.Deployed)
}

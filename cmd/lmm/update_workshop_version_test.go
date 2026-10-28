package main

// #428 (finding F4): `lmm update`'s own human-facing lines - the dry-run
// auto-update preview, the changelog header, the batch "applied" line,
// "pinned at"/"up to date", the locked-refusal/updating/updated trio, and
// `lmm update rollback`'s three - kept printing a Steam Workshop item's
// raw 19-digit content id after ad87828c (#428) fixed `lmm install`,
// `list`, `mod show` and the update TABLE. This file drives those exact
// lines for a Tier-3 item (lmm downloaded it itself: not External, but its
// source is workshop-capable) and asserts no line ever prints the content
// id, only the revision date (or, where there is none to show, the
// version_display.go words for "no date to give").

import (
	"context"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWorkshopContentID is a SECOND Workshop revision - workshopContentID/
// workshopRevision (workshop_version_display_test.go) is the "old" one
// these tests update FROM.
const newWorkshopContentID = "8123456789012345678"

// workshopUpdatedAt is workshopRevision's underlying instant - the same
// 1764767935 the install fixture in install_workshop_test.go stamps
// time_updated with.
var workshopUpdatedAt = time.Unix(1764767935, 0)

// fakeWorkshopUpdateSource wraps fakeUpdateSource (update_test.go) with a
// WorkshopScanner implementation, so core.sourceIsWorkshop - and therefore
// version_display.go's workshopVersioned - reports it as workshop-capable.
// ScanWorkshopItems is never called by anything these tests drive; it
// exists purely so the capability type-assertion succeeds, giving a Tier-3
// mod (External: false) the same "print the revision date" rule an
// External one gets.
type fakeWorkshopUpdateSource struct {
	*fakeUpdateSource
}

func (fakeWorkshopUpdateSource) ScanWorkshopItems(ctx context.Context, sourceGameID string) (source.WorkshopScan, error) {
	return source.WorkshopScan{}, nil
}

// setupDoUpdateWorkshopTest is setupDoUpdateTest's (update_test.go)
// Workshop twin: the identical fakeUpdateSource - a real CheckUpdates/
// ApplyUpdate path - registered so it also answers as workshop-capable.
func setupDoUpdateWorkshopTest(t *testing.T) (*core.Service, *domain.Game, *fakeUpdateSource) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := newFakeUpdateSource("steamworkshop")
	t.Cleanup(src.Close)
	svc.RegisterSource(fakeWorkshopUpdateSource{src})

	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"steamworkshop": "g1"},
	}

	oldSource, oldProfile, oldAll, oldDryRun, oldForce := updateSource, updateProfile, updateAll, updateDryRun, updateForce
	oldVerbose, oldNoColor, oldNoHooks := verbose, noColor, noHooks
	updateSource = "steamworkshop"
	updateProfile = ""
	updateAll = false
	updateDryRun = false
	updateForce = false
	verbose = false
	noColor = true
	noHooks = false
	t.Cleanup(func() {
		updateSource, updateProfile, updateAll, updateDryRun, updateForce = oldSource, oldProfile, oldAll, oldDryRun, oldForce
		verbose, noColor, noHooks = oldVerbose, oldNoColor, oldNoHooks
	})

	return svc, game, src
}

// seedWorkshopInstalledMod seeds a Tier-3 (not External) installed mod
// whose Version is the Workshop content id, stamped with a real UpdatedAt -
// seedInstalledForUpdate (update_test.go) leaves UpdatedAt at the zero
// value, which would still pass these tests (workshopRevisionDate returns
// "" for it, and displayRevision's "revision of unknown" is still not the
// raw id) but would never exercise the actual date substitution.
func seedWorkshopInstalledMod(t *testing.T, svc *core.Service, game *domain.Game) *domain.InstalledMod {
	t.Helper()
	mod := seedInstalledForUpdate(t, svc, game, "steamworkshop", "3000000001", "Workshop Item", workshopContentID,
		[]string{"old-1"}, map[string][]byte{"item-old.pak": []byte("old-content")})
	mod.UpdatedAt = workshopUpdatedAt
	require.NoError(t, svc.SaveInstalledMod(context.Background(), mod))
	mod, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3000000001", "g1", "default")
	require.NoError(t, err)
	return mod
}

// TestApplySingleUpdate_Workshop_PinnedAndUpToDate_ShowRevisionDate covers
// #428's ~742 (up to date) and ~730 (pinned) lines.
func TestApplySingleUpdate_Workshop_PinnedAndUpToDate_ShowRevisionDate(t *testing.T) {
	t.Run("up to date", func(t *testing.T) {
		svc, game, _ := setupDoUpdateWorkshopTest(t)
		mod := seedWorkshopInstalledMod(t, svc, game)
		// No AddMod: fakeUpdateSource's CheckUpdates finds no registered mod,
		// so PlanUpdate reports the mod as current.

		out := captureStdout(t, func() error {
			return applySingleUpdate(context.Background(), svc, game, mod, "default")
		})

		assert.NotContains(t, out, workshopContentID, "no line prints the content id as a version:\n%s", out)
		assert.Equal(t, "Workshop Item is already up to date (revision of "+workshopRevision+").\n", out)
	})

	t.Run("pinned", func(t *testing.T) {
		svc, game, _ := setupDoUpdateWorkshopTest(t)
		seedWorkshopInstalledMod(t, svc, game)
		_, err := svc.SetModUpdatePolicy(context.Background(), "steamworkshop", "3000000001", "g1", "default", domain.UpdatePinned)
		require.NoError(t, err)
		mod, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3000000001", "g1", "default")
		require.NoError(t, err)

		out := captureStdout(t, func() error {
			return applySingleUpdate(context.Background(), svc, game, mod, "default")
		})

		assert.NotContains(t, out, workshopContentID, "no line prints the content id as a version:\n%s", out)
		assert.Contains(t, out, "Workshop Item is pinned at revision of "+workshopRevision+" and was not checked.\n")
	})
}

// TestApplySingleUpdate_Workshop_LockedRefusal_ShowsRevisionDates covers
// #428's ~818 line: the "Update available: X → Y" context line printed
// just before a locked mod's canonical refusal.
//
// Only that ONE line is asserted against the raw ids: the refusal sentence
// printed right after it (plan.Refusal, fmt.Println'd verbatim) is built by
// internal/core/update.go's lockedRefUnlockOnlyMessage, which still embeds
// the locked ref's raw version ("... is locked at v%s in profile %s ...")
// - a real, separate gap outside this finding's cmd/lmm/update.go scope.
func TestApplySingleUpdate_Workshop_LockedRefusal_ShowsRevisionDates(t *testing.T) {
	svc, game, src := setupDoUpdateWorkshopTest(t)
	seedWorkshopInstalledMod(t, svc, game)
	src.AddMod(&domain.Mod{ID: "3000000001", SourceID: "steamworkshop", Name: "Workshop Item", Version: newWorkshopContentID, GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "item-new.pak", IsPrimary: true}})

	_, err := svc.SetModLock(context.Background(), "steamworkshop", "3000000001", "g1", "default", workshopContentID)
	require.NoError(t, err)
	mod, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3000000001", "g1", "default")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	line := lineContaining(out, "Update available")
	assert.Equal(t, "Update available: "+workshopRevision+" → newer", line)
}

// TestApplySingleUpdate_Workshop_Apply_ShowsRevisionDates covers #428's
// ~824 ("Updating...") and ~865 ("✓ Updated:") lines, driven through the
// real Service.ApplyUpdate path (fakeUpdateSource's download server).
func TestApplySingleUpdate_Workshop_Apply_ShowsRevisionDates(t *testing.T) {
	svc, game, src := setupDoUpdateWorkshopTest(t)
	mod := seedWorkshopInstalledMod(t, svc, game)
	src.AddMod(&domain.Mod{ID: "3000000001", SourceID: "steamworkshop", Name: "Workshop Item", Version: newWorkshopContentID, GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "item-new.pak", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.NotContains(t, out, workshopContentID, "no line prints the content id as a version:\n%s", out)
	assert.NotContains(t, out, newWorkshopContentID, "no line prints the target content id as a version:\n%s", out)
	assert.Contains(t, out, "Updating Workshop Item "+workshopRevision+" → newer...\n")
	assert.Contains(t, out, "\n✓ Updated: Workshop Item "+workshopRevision+" → newer\n")
}

// TestDoUpdate_Workshop_DryRunAutoUpdateAndChangelog_ShowRevisionDate covers
// #428's ~429 (dry-run "Would auto-update" list) and ~613 (changelog
// header) lines. A real Steam Workshop update never carries a changelog
// (internal/source/steamworkshop/updates.go's CheckUpdatesRefreshing never
// sets domain.Update.Changelog), so the second half of this test is
// defensive - it pins the line's wording for the day some other
// workshop-capable source does supply one.
func TestDoUpdate_Workshop_DryRunAutoUpdateAndChangelog_ShowRevisionDate(t *testing.T) {
	svc, game, src := setupDoUpdateWorkshopTest(t)
	updateDryRun = true
	seedWorkshopInstalledMod(t, svc, game)
	_, err := svc.SetModUpdatePolicy(context.Background(), "steamworkshop", "3000000001", "g1", "default", domain.UpdateAuto)
	require.NoError(t, err)
	src.AddMod(&domain.Mod{ID: "3000000001", SourceID: "steamworkshop", Name: "Workshop Item", Version: newWorkshopContentID, GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "item-new.pak", IsPrimary: true}})
	src.changelogs["3000000001"] = "Fixed things."

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.NotContains(t, out, workshopContentID, "no line prints the content id as a version:\n%s", out)
	assert.NotContains(t, out, newWorkshopContentID, "no line prints the target content id as a version:\n%s", out)
	assert.Contains(t, out, "\nWould auto-update 1 mod(s):\n")
	assert.Contains(t, out, "  - Workshop Item "+workshopRevision+" → newer\n")
	assert.Contains(t, out, "Workshop Item ("+workshopRevision+" → newer):")
}

// TestDoUpdate_Workshop_BulkApply_ShowsRevisionDate covers #428's ~674 line
// (the batch's per-item "✓ X old → new" line), driven through
// Service.ApplyUpdateBatch.
func TestDoUpdate_Workshop_BulkApply_ShowsRevisionDate(t *testing.T) {
	svc, game, src := setupDoUpdateWorkshopTest(t)
	seedWorkshopInstalledMod(t, svc, game)
	_, err := svc.SetModUpdatePolicy(context.Background(), "steamworkshop", "3000000001", "g1", "default", domain.UpdateAuto)
	require.NoError(t, err)
	src.AddMod(&domain.Mod{ID: "3000000001", SourceID: "steamworkshop", Name: "Workshop Item", Version: newWorkshopContentID, GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "item-new.pak", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.NotContains(t, out, workshopContentID, "no line prints the content id as a version:\n%s", out)
	assert.NotContains(t, out, newWorkshopContentID, "no line prints the target content id as a version:\n%s", out)
	assert.Contains(t, out, "  ✓ Workshop Item "+workshopRevision+" → newer\n")
}

// TestDoUpdateRollback_Workshop_ShowsRevisionDates covers #428's ~1044
// ("Rolling back...") and ~1076 ("✓ Rolled back:") lines. Neither
// Service.ApplyUpdate nor ApplyRollback ever touches InstalledMod.UpdatedAt
// (there is no per-revision date to move it to for a source whose only
// identity is the content id), so the installed mod's date stays the one
// seedWorkshopInstalledMod stamped even after the update below - the
// target side has no date at all (domain.InstalledMod carries no
// PreviousUpdatedAt), which is exactly why displayRollbackTarget says
// "previous revision" rather than inventing one.
func TestDoUpdateRollback_Workshop_ShowsRevisionDates(t *testing.T) {
	svc, game, src := setupDoUpdateWorkshopTest(t)
	mod := seedWorkshopInstalledMod(t, svc, game)
	src.AddMod(&domain.Mod{ID: "3000000001", SourceID: "steamworkshop", Name: "Workshop Item", Version: newWorkshopContentID, GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "item-new.pak", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	require.NoError(t, captureStdoutOnlyErr(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	}))

	updated, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3000000001", "g1", "default")
	require.NoError(t, err)
	require.Equal(t, newWorkshopContentID, updated.Version, "fixture: the version identity is the content id")
	require.Equal(t, workshopContentID, updated.PreviousVersion)

	out := captureStdout(t, func() error {
		return doUpdateRollback(context.Background(), svc, game, "3000000001")
	})

	assert.NotContains(t, out, workshopContentID, "no line prints the content id as a version:\n%s", out)
	assert.NotContains(t, out, newWorkshopContentID, "no line prints the target content id as a version:\n%s", out)
	assert.Contains(t, out, "Rolling back Workshop Item "+workshopRevision+" → previous revision...\n")
	assert.Contains(t, out, "\n✓ Rolled back: Workshop Item "+workshopRevision+" → previous revision\n")

	rolledBack, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3000000001", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, workshopContentID, rolledBack.Version, "rollback must restore the previous content id")
}

// TestDoUpdateRollback_Workshop_LockedRefusal_ShowsRevisionDates covers
// #428's ~1038 line: the "Rollback available: X → Y" context line printed
// just before a locked mod's canonical refusal.
//
// As in TestApplySingleUpdate_Workshop_LockedRefusal_ShowsRevisionDates,
// only that ONE line is asserted against the raw ids - the refusal
// sentence after it is internal/core's own text, a separate gap outside
// this finding's cmd/lmm/update.go scope.
func TestDoUpdateRollback_Workshop_LockedRefusal_ShowsRevisionDates(t *testing.T) {
	svc, game, src := setupDoUpdateWorkshopTest(t)
	mod := seedWorkshopInstalledMod(t, svc, game)
	src.AddMod(&domain.Mod{ID: "3000000001", SourceID: "steamworkshop", Name: "Workshop Item", Version: newWorkshopContentID, GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "item-new.pak", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	require.NoError(t, captureStdoutOnlyErr(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	}))

	_, err := svc.SetModLock(context.Background(), "steamworkshop", "3000000001", "g1", "default", newWorkshopContentID)
	require.NoError(t, err)

	out, err := captureStdoutErr(t, func() error {
		return doUpdateRollback(context.Background(), svc, game, "3000000001")
	})
	require.Error(t, err, "a locked rollback refusal exits non-zero (#382)")

	line := lineContaining(out, "Rollback available")
	assert.Equal(t, "Rollback available: "+workshopRevision+" → previous revision", line)
}

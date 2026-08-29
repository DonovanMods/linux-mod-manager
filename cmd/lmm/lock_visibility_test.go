package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setLockedForUpdate locks sourceID:modID in the "default" profile at
// version, for use with setupDoUpdateTest's fakeUpdateSource-backed fixtures
// (mirrors pin_visibility_test.go's setPolicy).
func setLockedForUpdate(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version string) {
	t.Helper()
	require.NoError(t, svc.NewProfileManager().SetModLock(game.ID, "default", sourceID, modID, version))
}

// TestApplySingleUpdate_Locked_RefusesUpdate_Text: `lmm update <mod-id>` on a
// locked mod with an update available must refuse to apply and name both
// remedy commands, instead of silently applying (or bubbling the core gate's
// raw error).
func TestApplySingleUpdate_Locked_RefusesUpdate_Text(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	setLockedForUpdate(t, svc, game, "test-src", "mod1", "1.0")
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.Contains(t, out, "Update available: 1.0 → 2.0 — but Mod One is locked at v1.0.")
	assert.Contains(t, out, "Move the lock: lmm mod lock -s test-src -p default mod1 2.0   |   Unlock: lmm mod unlock -s test-src -p default mod1", "both remedies must carry -s/-p so a copy-paste can never resolve against a different source/profile (#142 round 5)")
	assert.NotContains(t, out, "Updating Mod One", "must never print the applying header - it never applies")

	updated, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version, "a locked mod must not actually update")
}

// TestApplySingleUpdate_Locked_RefusesUpdate_JSON is the --json sibling:
// exactly one document, status "skipped"/reason "locked", to_version set.
func TestApplySingleUpdate_Locked_RefusesUpdate_JSON(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	setLockedForUpdate(t, svc, game, "test-src", "mod1", "1.0")
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	var doc core.UpdateApplyResult
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	assert.Equal(t, "mod1", doc.Mod.ModID)
	assert.Equal(t, "1.0", doc.FromVersion)
	assert.Equal(t, "2.0", doc.ToVersion)
	assert.Equal(t, "skipped", doc.Status.String())
	assert.Equal(t, "locked", doc.Reason)
}

// TestApplySingleUpdate_LockedAndPinned_MessageSaysBoth: a mod that is BOTH
// pinned and locked never reaches the source (pinned filters it out of
// CheckUpdates entirely), so the existing pinned message is the one that
// fires - it must additionally disclose the lock so the user isn't told only
// half the story.
func TestApplySingleUpdate_LockedAndPinned_MessageSaysBoth(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)
	seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	require.NoError(t, svc.SetModUpdatePolicy(context.Background(), "test-src", "mod1", "g1", "default", domain.UpdatePinned))
	setLockedForUpdate(t, svc, game, "test-src", "mod1", "1.0")

	mod, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.Contains(t, out, "Mod One is pinned at v1.0 and was not checked (also locked).")
	assert.Contains(t, out, "Unpin with: lmm mod set-update -s test-src -p default mod1 --notify", "the unpin remedy must carry -s/-p so a copy-paste can never resolve against a different source/profile (#142 round 5)")
}

// TestApplySingleUpdate_LockedAndPinned_JSON is TestApplySingleUpdate_LockedAndPinned_MessageSaysBoth's
// --json sibling, characterized before the Task 9 PlanUpdate lift: the
// document has no locked-distinguishing field for the pinned branch (unlike
// the text branch's "(also locked)" suffix) - reason stays "pinned" whether
// or not the mod is also locked.
func TestApplySingleUpdate_LockedAndPinned_JSON(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	require.NoError(t, svc.SetModUpdatePolicy(context.Background(), "test-src", "mod1", "g1", "default", domain.UpdatePinned))
	setLockedForUpdate(t, svc, game, "test-src", "mod1", "1.0")

	mod, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	var doc core.UpdateApplyResult
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	assert.Equal(t, "mod1", doc.Mod.ModID)
	assert.Equal(t, "1.0", doc.FromVersion)
	assert.Empty(t, doc.ToVersion)
	assert.Equal(t, "skipped", doc.Status.String())
	assert.Equal(t, "pinned", doc.Reason)
}

// TestApplySingleUpdate_LockedUpToDate_MessageUnchanged characterizes the
// up-to-date branch's indifference to lock state before the Task 9 PlanUpdate
// lift: unlike the pinned branch, the up-to-date message never gained a
// "(also locked)" caveat, and this guards against the lift accidentally
// adding one.
func TestApplySingleUpdate_LockedUpToDate_MessageUnchanged(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	setLockedForUpdate(t, svc, game, "test-src", "mod1", "1.0")
	// No AddMod: the mod is up to date regardless of lock state.

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.Equal(t, "Mod One is already up to date (v1.0).\n", out)
}

// TestApplySingleUpdate_UnlockedPinned_MessageUnchanged pins the counterpart:
// a plain pinned (not locked) mod must NOT gain the "(also locked)" suffix -
// this is one of pin_visibility_test.go's contract's siblings, guarding
// against the suffix always being appended.
func TestApplySingleUpdate_UnlockedPinned_MessageUnchanged(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)
	seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	require.NoError(t, svc.SetModUpdatePolicy(context.Background(), "test-src", "mod1", "g1", "default", domain.UpdatePinned))

	mod, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.Contains(t, out, "Mod One is pinned at v1.0 and was not checked.")
	assert.NotContains(t, out, "also locked")
}

// TestApplyUpdate_Locked_CoreGateBackstop bypasses applySingleUpdate's new
// CLI pre-check entirely (calling the package-level applyUpdate directly, as
// applySingleUpdate itself does after its own checks pass) to prove the core
// gate (core.ErrModLocked, added in T2) still refuses on its own - the CLI
// pre-check is a nicety layered on top, not the only thing standing between a
// locked mod and an apply.
func TestApplyUpdate_Locked_CoreGateBackstop(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
	setLockedForUpdate(t, svc, game, "test-src", "mod1", "1.0")
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
	src.AddDownload("new-1", []byte("new-content"))

	plan, err := svc.PlanUpdate(context.Background(), game, "default", mod.SourceID, mod.ID)
	require.NoError(t, err)

	_, err = applyUpdate(context.Background(), svc, game, plan)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModLocked, "the core gate must still refuse even when a CLI pre-check is bypassed")

	updated, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updated.Version)
}

// TestDoUpdate_TableMarksLockedAndSkipsAutoApply: the bulk table's POLICY
// cell must mark a locked row with "[locked@<version>]", and a locked+auto
// row must not be auto-applied (the will-auto-apply "✓" is misleading there
// and must not print either) - the unlocked auto row alongside it proves the
// normal path is untouched.
func TestDoUpdate_TableMarksLockedAndSkipsAutoApply(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)

	seedInstalledForUpdate(t, svc, game, "test-src", "modA", "Mod A", "1.0", []string{"a-old"}, map[string][]byte{"a-old.esp": []byte("old")})
	require.NoError(t, svc.SetModUpdatePolicy(context.Background(), "test-src", "modA", "g1", "default", domain.UpdateAuto))
	setLockedForUpdate(t, svc, game, "test-src", "modA", "1.0")
	src.AddMod(&domain.Mod{ID: "modA", SourceID: "test-src", Name: "Mod A", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "a-new", FileName: "a-new.esp", IsPrimary: true}})
	src.AddDownload("a-new", []byte("new"))

	seedInstalledForUpdate(t, svc, game, "test-src", "modB", "Mod B", "1.0", []string{"b-old"}, map[string][]byte{"b-old.esp": []byte("old")})
	require.NoError(t, svc.SetModUpdatePolicy(context.Background(), "test-src", "modB", "g1", "default", domain.UpdateAuto))
	src.AddMod(&domain.Mod{ID: "modB", SourceID: "test-src", Name: "Mod B", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "b-new", FileName: "b-new.esp", IsPrimary: true}})
	src.AddDownload("b-new", []byte("new"))

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	var rowA, rowB string
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "Mod A"):
			rowA = l
		case strings.HasPrefix(l, "Mod B"):
			rowB = l
		}
	}
	require.NotEmpty(t, rowA)
	require.NotEmpty(t, rowB)
	assert.Contains(t, rowA, "[locked@1.0]")
	assert.NotContains(t, rowA, "✓", "a locked auto row must not claim it will auto-apply")
	assert.Contains(t, rowB, "✓")
	assert.NotContains(t, rowB, "[locked@")

	assert.Contains(t, out, "\nApplying 1 auto-update(s)...\n")
	assert.Contains(t, out, "  ✓ Mod B 1.0 → 2.0\n")
	assert.NotContains(t, out, "Mod A 1.0 → 2.0", "the locked mod must never reach applyUpdate")
	assert.Contains(t, out, "1 locked mod(s) not applied: Mod A — move the lock or unlock to update.")

	updatedA, err := svc.GetInstalledMod(context.Background(), "test-src", "modA", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updatedA.Version, "locked mod must not have applied")

	updatedB, err := svc.GetInstalledMod(context.Background(), "test-src", "modB", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updatedB.Version, "unlocked auto mod must apply as before")
}

// TestDoUpdate_BulkJSON_MarksLockedEntries is the --json sibling of the bulk
// table's "[locked@<version>]" marker (#143 polish): a locked mod's updates[]
// entry must carry "locked": true so a JSON consumer can tell that a locked
// auto-policy mod will not be applied, and an unlocked entry alongside it must
// stay false - now emitted explicitly rather than omitted (#302: under
// encoding/json/v2, `omitempty` drops only empty JSON values - "", null, {},
// [] - never a false bool; `omitzero` is the tag that would).
func TestDoUpdate_BulkJSON_MarksLockedEntries(t *testing.T) {
	withJSONOutput(t)
	svc, game, src := setupDoUpdateTest(t)

	seedInstalledForUpdate(t, svc, game, "test-src", "modA", "Mod A", "1.0", []string{"a-old"}, map[string][]byte{"a-old.esp": []byte("old")})
	require.NoError(t, svc.SetModUpdatePolicy(context.Background(), "test-src", "modA", "g1", "default", domain.UpdateAuto))
	setLockedForUpdate(t, svc, game, "test-src", "modA", "1.0")
	src.AddMod(&domain.Mod{ID: "modA", SourceID: "test-src", Name: "Mod A", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "a-new", FileName: "a-new.esp", IsPrimary: true}})
	src.AddDownload("a-new", []byte("new"))

	seedInstalledForUpdate(t, svc, game, "test-src", "modB", "Mod B", "1.0", []string{"b-old"}, map[string][]byte{"b-old.esp": []byte("old")})
	src.AddMod(&domain.Mod{ID: "modB", SourceID: "test-src", Name: "Mod B", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "b-new", FileName: "b-new.esp", IsPrimary: true}})
	src.AddDownload("b-new", []byte("new"))

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	var doc core.UpdateCheckReport
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	require.Len(t, doc.Updates, 2)

	byID := map[string]domain.Update{}
	for _, u := range doc.Updates {
		byID[u.InstalledMod.ID] = u
	}
	require.Contains(t, byID, "modA")
	require.Contains(t, byID, "modB")
	assert.True(t, byID["modA"].Locked, "locked mod's entry must carry locked: true")
	assert.False(t, byID["modB"].Locked, "unlocked mod's entry must stay unmarked")
	assert.Contains(t, out, `"locked": false`,
		"v2 (#302): domain.Update.Locked's `omitempty` no longer drops a false bool - "+
			"encoding/json/v2 omits only empty JSON values, so an unlocked entry now says so explicitly")
}

// TestDoUpdate_NoAutoUpdates_StillReportsLockedSkip covers the "no-auto
// case too" bullet: when the ONLY auto-policy candidate is locked,
// autoUpdates ends up empty (so "Applying N auto-update(s)..." never
// prints), but the locked-skip line must still print.
func TestDoUpdate_NoAutoUpdates_StillReportsLockedSkip(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)

	seedInstalledForUpdate(t, svc, game, "test-src", "modA", "Mod A", "1.0", []string{"a-old"}, map[string][]byte{"a-old.esp": []byte("old")})
	require.NoError(t, svc.SetModUpdatePolicy(context.Background(), "test-src", "modA", "g1", "default", domain.UpdateAuto))
	setLockedForUpdate(t, svc, game, "test-src", "modA", "1.0")
	src.AddMod(&domain.Mod{ID: "modA", SourceID: "test-src", Name: "Mod A", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "a-new", FileName: "a-new.esp", IsPrimary: true}})
	src.AddDownload("a-new", []byte("new"))

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.NotContains(t, out, "Applying", "no unlocked auto candidate means no apply section at all")
	assert.Contains(t, out, "1 locked mod(s) not applied: Mod A — move the lock or unlock to update.")
}

// TestDoUpdate_AllSkipsLockedNotifyMods: --all must also refuse a locked
// notify-policy mod (it is not auto, so it only ever reaches an apply
// attempt via --all), reporting it with the same locked-skip line.
func TestDoUpdate_AllSkipsLockedNotifyMods(t *testing.T) {
	svc, game, src := setupDoUpdateTest(t)
	updateAll = true

	seedInstalledForUpdate(t, svc, game, "test-src", "modA", "Mod A", "1.0", []string{"a-old"}, map[string][]byte{"a-old.esp": []byte("old")})
	setLockedForUpdate(t, svc, game, "test-src", "modA", "1.0")
	src.AddMod(&domain.Mod{ID: "modA", SourceID: "test-src", Name: "Mod A", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "a-new", FileName: "a-new.esp", IsPrimary: true}})
	src.AddDownload("a-new", []byte("new"))

	seedInstalledForUpdate(t, svc, game, "test-src", "modB", "Mod B", "1.0", []string{"b-old"}, map[string][]byte{"b-old.esp": []byte("old")})
	src.AddMod(&domain.Mod{ID: "modB", SourceID: "test-src", Name: "Mod B", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "b-new", FileName: "b-new.esp", IsPrimary: true}})
	src.AddDownload("b-new", []byte("new"))

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "\nApplying 1 remaining update(s)...\n")
	assert.Contains(t, out, "  ✓ Mod B 1.0 → 2.0\n")
	assert.NotContains(t, out, "Mod A 1.0 → 2.0", "the locked mod must never reach applyUpdate")
	assert.Contains(t, out, "1 locked mod(s) not applied: Mod A — move the lock or unlock to update.")

	updatedA, err := svc.GetInstalledMod(context.Background(), "test-src", "modA", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", updatedA.Version)

	updatedB, err := svc.GetInstalledMod(context.Background(), "test-src", "modB", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", updatedB.Version)
}

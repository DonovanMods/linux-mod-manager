package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for #601: a download the source refuses for the ACCOUNT (a free
// NexusMods key) offers the manual download's remedy, but is never recorded
// as the mod's manual-only fact (#543), which is per mod and for everyone.

const accountReason = "NexusMods serves downloads through its API to Premium accounts only"

// accountRestrictedSource refuses every download for the account.
type accountRestrictedSource struct {
	*fromFileSource
}

func (s *accountRestrictedSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", &source.AccountRestrictedError{Reason: accountReason}
}

// The DownloadError says "fetch it by hand" (ManualDownload) and why
// (AccountRestricted), and is never ErrManualDownload.
func TestDownloadError_AccountRestricted_OffersTheManualRemedy(t *testing.T) {
	const page = "https://www.nexusmods.com/cyberpunk2077/mods/107"
	svc, game, mod, file := downloadFailureFixture(t, &source.AccountRestrictedError{Reason: accountReason}, page)

	_, err := svc.DownloadModForTest(context.Background(), "dl-src", game, mod, file, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrAccountRestricted)
	assert.False(t, errors.Is(err, source.ErrManualDownload), "an account refusal is never the mod's manual-only fact")
	assert.Contains(t, err.Error(), accountReason)

	var dl *core.DownloadError
	require.ErrorAs(t, err, &dl)
	assert.True(t, dl.ManualDownload, "the remedy is the manual download's")
	assert.True(t, dl.AccountRestricted)
	assert.Equal(t, page, dl.ModURL)
}

// A per-mod refusal is not an account restriction.
func TestDownloadError_ManualDownloadIsNotAccountRestricted(t *testing.T) {
	svc, game, mod, file := downloadFailureFixture(t, &source.ManualDownloadError{Reason: manualReason}, "https://example.test/m")
	_, err := svc.DownloadModForTest(context.Background(), "dl-src", game, mod, file, nil)
	var dl *core.DownloadError
	require.ErrorAs(t, err, &dl)
	assert.True(t, dl.ManualDownload)
	assert.False(t, dl.AccountRestricted)
}

// The acceptance case: an explicit update the account is refused records
// nothing in manual_only_mods - the row, the listing and a later batch all
// still treat the mod as one its source serves.
func TestApplyUpdate_AccountRestricted_RecordsNothingManualOnly(t *testing.T) {
	f := newFromFileFixture(t)
	f.svc.RegisterSource(&accountRestrictedSource{fromFileSource: f.src})
	ctx := context.Background()

	plan, err := f.svc.PlanUpdateFrom(ctx, f.game, "default", domain.Update{InstalledMod: *f.row(t), NewVersion: "2.0",
		FileIDReplacements: map[string]string{"100": "200"}})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdate(ctx, f.game, plan, core.UpdateOptions{}, nil)
	require.ErrorIs(t, err, source.ErrAccountRestricted)
	var dl *core.DownloadError
	require.ErrorAs(t, err, &dl)
	assert.True(t, dl.ManualDownload)
	assert.True(t, dl.AccountRestricted)

	assert.False(t, f.row(t).ManualOnly, "the account's refusal is not the mod's")
	mods, err := f.svc.GetInstalledMods(ctx, "g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.False(t, mods[0].ManualOnly)
}

// The install the account was refused leaves no record either: the install
// from the hand-downloaded file that follows does not mark the mod
// manual-only (contrast #543's TestInstallFromFile_AfterTheSourceRefusedTheInstall_RecordsManualOnly).
func TestInstallFromFile_AfterTheAccountWasRefused_NotManualOnly(t *testing.T) {
	f := newInstallFromFileFixture(t)
	f.svc.RegisterSource(&struct {
		*installFromFileSource
		accountRefusal
	}{installFromFileSource: f.src})
	dl := f.failedInstall(t)
	require.True(t, dl.ManualDownload)
	require.True(t, dl.AccountRestricted)
	require.Equal(t, "300", dl.FileID, "the failure still names the file for Install from file")

	plan := f.plan(t, f.archive(t, "MapUtils-1.2.zip"), core.ImportArchiveOptions{ExpectedFileID: dl.FileID})
	_, err := f.apply(t, plan, core.ImportArchiveOptions{})
	require.NoError(t, err)

	assert.False(t, f.row(t).ManualOnly)
}

// accountRefusal is the GetDownloadURL an embedding source refuses with.
type accountRefusal struct{}

func (accountRefusal) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", &source.AccountRestrictedError{Reason: accountReason}
}

// A batch reports the refused item as a failure carrying the manual remedy
// and the account flag, records nothing, and so a second batch still
// attempts it rather than skipping it as manual-only.
func TestApplyUpdateBatch_AccountRestricted_FailsWithTheRemedyAndRecordsNothing(t *testing.T) {
	f := newFromFileFixture(t)
	f.svc.RegisterSource(&accountRestrictedSource{fromFileSource: f.src})
	ctx := context.Background()

	run := func() *core.UpdateBatchResult {
		t.Helper()
		plan, err := f.svc.PlanUpdateBatchFrom(ctx, f.game, "default",
			[]domain.Update{{InstalledMod: *f.row(t), NewVersion: "2.0", FileIDReplacements: map[string]string{"100": "200"}}}, nil)
		require.NoError(t, err)
		result, err := f.svc.ApplyUpdateBatch(ctx, f.game, plan, core.UpdateBatchOptions{}, nil)
		require.NoError(t, err)
		return result
	}

	for range 2 {
		result := run()
		assert.Empty(t, result.Skipped, "never skipped as manual-only")
		require.Len(t, result.Failed, 1)
		failure := result.Failed[0]
		assert.True(t, failure.ManualDownload)
		assert.True(t, failure.AccountRestricted)
		assert.Equal(t, "https://example.test/mods/auctionator", failure.ModURL)
		assert.ErrorIs(t, failure.Cause(), source.ErrAccountRestricted)
		assert.False(t, f.row(t).ManualOnly)
	}
}

// verify --fix's NO CHECKSUM repair cannot re-download a file the account
// is refused any more than one the mod's author opted out of, so it fills
// the checksum from the complete cache entry the same way (#514) - and
// still records nothing against the mod.
func TestVerifyFix_NoChecksum_AccountRestricted_FillsFromCache(t *testing.T) {
	svc, game, src := seedManualOnlyMod(t, false, true)
	restricted := &accountRestrictedVerifySource{manualOnlySource: src}
	svc.RegisterSource(restricted)

	result, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, int32(1), restricted.urlCalls.Load(), "the re-download is tried first, and refused")
	assert.Zero(t, result.Warnings)
	assert.NotEmpty(t, storedChecksum(t, svc, game.ID, "msrc", "8939586", "1"))
	row, err := svc.GetInstalledMod(context.Background(), "msrc", "8939586", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, row.ManualOnly, "the account's refusal is not the mod's")
}

// accountRestrictedVerifySource is manualOnlySource refusing for the account.
type accountRestrictedVerifySource struct {
	*manualOnlySource
}

func (s *accountRestrictedVerifySource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	s.urlCalls.Add(1)
	return "", &source.AccountRestrictedError{Reason: accountReason}
}

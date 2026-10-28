package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedRepairSource serves downloads (mockSourceWithDownloads) AND a
// scripted file list, so a test can say what version the source can serve
// for the recorded file id.
type lockedRepairSource struct {
	*mockSourceWithDownloads
	files []domain.DownloadableFile
}

func (s *lockedRepairSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return s.files, nil
}

// setupLockedMissingFileFixture seeds one source-backed mod recorded (and
// LOCKED) at v1.0 whose cache entry is gone, with the source's file list
// under the caller's control.
func setupLockedMissingFileFixture(t *testing.T, fileVersion string) (*core.Service, *domain.Game, *lockedRepairSource) {
	t.Helper()
	// storeCache=false: the recorded version's cache entry is MISSING.
	return setupLockedRepairFixture(t, fileVersion, false, "sum")
}

// setupLockedRepairFixture is setupLockedMissingFileFixture's general form
// (review I2): the same locked-at-v1.0 mod, with the caller choosing whether
// the recorded version's cache entry EXISTS and what checksum its file row
// carries - the two knobs that decide which of perFileWalk's three repairs
// the row lands in (no entry -> missing; entry + empty checksum ->
// no_checksum).
func setupLockedRepairFixture(t *testing.T, fileVersion string, storeCache bool, checksum string) (*core.Service, *domain.Game, *lockedRepairSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	ctx := context.Background()
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)

	src := &lockedRepairSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("lsrc"),
		files:                   []domain.DownloadableFile{{ID: "f1", FileName: "mod.esp", Version: fileVersion, IsPrimary: true}},
	}
	t.Cleanup(src.Close)
	src.AddDownload("f1", []byte("payload-for-"+fileVersion))
	svc.RegisterSource(src)

	seedVerifyMod(t, svc, game, "lsrc", "locked-mod", "Locked Mod", "1.0", []string{"f1"}, storeCache)
	require.NoError(t, svc.SaveFileChecksum(ctx, "lsrc", "locked-mod", game.ID, "default", "f1", checksum))
	require.NoError(t, pm.UpsertMod(ctx, game.ID, "default", domain.ModReference{
		SourceID: "lsrc", ModID: "locked-mod", Version: "1.0", FileIDs: []string{"f1"},
	}))
	require.NoError(t, pm.SetModLock(ctx, game.ID, "default", "lsrc", "locked-mod", "1.0"))

	return svc, game, src
}

func findingFor(t *testing.T, result *core.VerifyResult, modID string) core.VerifyFinding {
	t.Helper()
	for _, f := range result.Findings {
		if f.ModID == modID {
			return f
		}
	}
	t.Fatalf("no finding for %s in %+v", modID, result.Findings)
	return core.VerifyFinding{}
}

// #325: `verify --fix` refuses to move a LOCKED mod's version record, but
// the MISSING-file repair then redownloaded the source's CURRENT file into
// the RECORDED (locked) version's cache slot - poisoning the cache of the
// one kind of mod whose whole point is version pinning. When the source
// cannot serve the recorded version, the repair is refused instead, and the
// slot is left untouched.
func TestVerifyFix_LockedMod_MissingFile_RefusesWhenSourceMovedOn(t *testing.T) {
	svc, game, src := setupLockedMissingFileFixture(t, "2.0") // the source now serves v2.0
	ctx := context.Background()

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, 0, src.DownloadCount(), "nothing may be fetched into the locked version's slot")
	assert.False(t, svc.GetGameCache(game).Exists(game.ID, "lsrc", "locked-mod", "1.0"),
		"the recorded version's slot must be left exactly as it was")

	f := findingFor(t, result, "locked-mod")
	assert.Equal(t, "missing", f.Status, "a refused repair leaves the issue reported, not resolved")
	assert.Equal(t, "locked", f.Note)
}

// The other branch: the source CAN still serve the recorded version's file,
// so the repair runs and the recorded slot is refilled with its own bytes.
func TestVerifyFix_LockedMod_MissingFile_RepairsWhenSourceStillServesIt(t *testing.T) {
	svc, game, src := setupLockedMissingFileFixture(t, "1.0") // the recorded version
	ctx := context.Background()

	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, src.DownloadCount(), "the recorded version's own file is fetchable, so fetch it")
	assert.True(t, svc.GetGameCache(game).Exists(game.ID, "lsrc", "locked-mod", "1.0"))

	f := findingFor(t, result, "locked-mod")
	assert.Equal(t, "ok", f.Status)
}

// lockedRefusalSentence is the text a refused locked repair must carry:
// LockedRefUnlockOnlyRefusalError's sentence, with the ErrModLocked sentinel
// trimmed back off, behind verify's "--fix skipped: " lead-in. Built from
// the constructor rather than copied, so a wording change cannot leave one
// site behind (#311's whole point).
func lockedRefusalSentence(t *testing.T, mod domain.Mod, profileName, lockedAt string) string {
	t.Helper()
	err := core.LockedRefUnlockOnlyRefusalError(mod, profileName, &domain.ModReference{Version: lockedAt})
	return "--fix skipped: " + strings.TrimPrefix(err.Error(), core.ErrModLocked.Error()+": ")
}

// assertLockedSkip pins what BOTH halves of a refused repair must look like
// (#325, review I2): the row's machine-checkable Note is the short "locked"
// marker rather than a raw error string, and the text sub-line is the
// canonical refusal sentence - not the word "failed" (nothing failed; the
// engine declined, and it will decline identically forever) and not the
// ErrModLocked sentinel, which would stutter "mod is locked: <Name> is
// locked at ...".
func assertLockedSkip(t *testing.T, note, detail string) {
	t.Helper()
	assert.Equal(t, "locked", note, "the Note is the short machine-checkable reason, not a rendered error")
	assert.NotContains(t, note, core.ErrModLocked.Error(), "the sentinel must never reach the wire")
	assert.NotContains(t, detail, core.ErrModLocked.Error()+":", "the sentinel prefix would stutter against the sentence's own head")
	assert.NotContains(t, strings.ToLower(detail), "failed", "a refusal is not a failure - it will decline identically on a retry")
}

// #325 (review I2): the gate covers all three file repairs, but only the
// MISSING branch learned that ErrModLocked means REFUSED. The no_checksum
// repair fell into its generic error arm, so a locked ref was reported as
// "Re-download to populate checksum failed: mod is locked: Locked Mod is
// locked at v1.0 ..." - a failure the user is invited to retry, wearing the
// sentinel prefix twice over.
func TestVerifyFix_LockedMod_NoChecksum_RefusesAsASkipNotAFailure(t *testing.T) {
	// The recorded version IS cached, but its checksum row is empty, so the
	// row lands in perFileWalk's no_checksum repair; the source has moved to
	// v2.0, so the repair is refused.
	svc, game, src := setupLockedRepairFixture(t, "2.0", true, "")
	ctx := context.Background()

	sink, rec := core.RecordEvents()
	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, src.DownloadCount(), "nothing may be fetched into the locked version's slot")

	f := findingFor(t, result, "locked-mod")
	assert.Equal(t, "no_checksum", f.Status, "a refused repair leaves the warning reported, not resolved")

	details := repairDetails(verifyEvents(*rec))
	require.Len(t, details, 1)
	assertLockedSkip(t, f.Note, details[0].Detail)
	assert.Equal(t, lockedRefusalSentence(t,
		domain.Mod{ID: "locked-mod", Name: "Locked Mod", SourceID: "lsrc"}, "default", "1.0"), details[0].Detail)
	assert.False(t, details[0].Fixed)
}

// #325 (review I2), the third repair site: needs_reingest branched on the
// error alone, so a locked ref was reported as "Re-ingest failed: mod is
// locked: ..." with the same two defects. The fixture is
// newNeedsReingestFixGame's pre-#221 pak entry with its ref locked, against
// a source whose file carries no version at all - servesRecordedVersion's
// fail-closed case, which is exactly the shape a locked Icarus mod has.
func TestVerifyFix_LockedMod_NeedsReingest_RefusesAsASkipNotAFailure(t *testing.T) {
	src := &reingestFixSource{fakeCompilerSource: &fakeCompilerSource{downloadURL: newByteServer(t, []byte("legacy-pak-bytes"))}}
	svc, game := newNeedsReingestFixGame(t, src, "fake-compiler")
	ctx := context.Background()
	require.NoError(t, svc.NewProfileManager().SetModLock(ctx, game.ID, "default", "fake-compiler", "legacypak", "1.0"))

	sink, rec := core.RecordEvents()
	result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, sink)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Warnings, "a refused re-ingest leaves the warning outstanding")
	f := findingFor(t, result, "legacypak")
	assert.Equal(t, "needs_reingest", f.Status, "a refused repair is not a repair")

	details := repairDetails(verifyEvents(*rec))
	require.Len(t, details, 1)
	assertLockedSkip(t, f.Note, details[0].Detail)
	assert.Equal(t, lockedRefusalSentence(t,
		domain.Mod{ID: "legacypak", Name: "Legacy Pak", SourceID: "fake-compiler"}, "default", "1.0"), details[0].Detail)
	assert.False(t, details[0].Fixed)
}

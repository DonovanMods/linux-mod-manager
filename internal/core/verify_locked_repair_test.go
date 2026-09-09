package core_test

import (
	"context"
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

	// storeCache=false: the recorded version's cache entry is MISSING.
	seedVerifyMod(t, svc, game, "lsrc", "locked-mod", "Locked Mod", "1.0", []string{"f1"}, false)
	require.NoError(t, svc.SaveFileChecksum(ctx, "lsrc", "locked-mod", game.ID, "default", "f1", "sum"))
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

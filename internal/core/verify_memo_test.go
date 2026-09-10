package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// verifyOnce runs the memo-eligible shape (no sink, no fix, no filter) and
// returns the result.
func verifyOnce(t *testing.T, svc *core.Service, game *domain.Game, opts core.VerifyOptions) *core.VerifyResult {
	t.Helper()
	res, err := svc.VerifyForTest(context.Background(), game, "default", opts, nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	return res
}

// TestVerify_MemoAnswersAnUnchangedInstallation is #336: Mission Control
// hydrates on every route change, job completion and profile switch, and
// each hydrate ran the full verify tier - a per-mod source query and a
// per-file cache walk, for state that had not moved. An unchanged
// installation now answers from the last result, keeping its original
// checked_at so the surface can say how old the answer is.
func TestVerify_MemoAnswersAnUnchangedInstallation(t *testing.T) {
	svc, game := newVerifyTestServiceWithFiles(t, 3)

	first := verifyOnce(t, svc, game, core.VerifyOptions{})
	assert.False(t, first.Cached, "the first run is a real one")
	require.False(t, first.CheckedAt.IsZero())

	second := verifyOnce(t, svc, game, core.VerifyOptions{})
	assert.True(t, second.Cached, "an unchanged installation must not re-run the tier")
	assert.Equal(t, first.CheckedAt, second.CheckedAt,
		"a memo hit reports when the answer was actually computed, not when it was served")
	assert.Equal(t, first.Findings, second.Findings)
	assert.Equal(t, first.Checked, second.Checked)
}

// TestVerify_MemoMissesAfterAnOutOfProcessFileChange: the fingerprint is a
// stat-only walk of the deployed tree, so a file that changes SIZE or MTIME
// under lmm's feet - another process, a manual edit - invalidates it.
func TestVerify_MemoMissesAfterAnOutOfProcessFileChange(t *testing.T) {
	svc, game := newVerifyTestServiceWithFiles(t, 2)
	deployed := filepath.Join(game.ModPath, "stray.esp")
	require.NoError(t, os.WriteFile(deployed, []byte("one"), 0644))

	first := verifyOnce(t, svc, game, core.VerifyOptions{})
	require.False(t, first.Cached)
	require.True(t, verifyOnce(t, svc, game, core.VerifyOptions{}).Cached)

	// A new mtime AND a new size - the two halves the fingerprint reads.
	require.NoError(t, os.WriteFile(deployed, []byte("one plus more"), 0644))
	require.NoError(t, os.Chtimes(deployed, time.Now().Add(time.Minute), time.Now().Add(time.Minute)))

	after := verifyOnce(t, svc, game, core.VerifyOptions{})
	assert.False(t, after.Cached, "a changed deployed tree must re-run the tier")
}

// TestVerify_MemoMissesAfterAMutation: any in-process mutation takes
// beginOp, and beginOp drops the memo - so a deploy, an install or an
// enable makes the next hydrate a real run whether or not the fingerprint
// would have noticed.
func TestVerify_MemoMissesAfterAMutation(t *testing.T) {
	svc, game := newVerifyTestServiceWithFiles(t, 2)

	require.False(t, verifyOnce(t, svc, game, core.VerifyOptions{}).Cached)
	require.True(t, verifyOnce(t, svc, game, core.VerifyOptions{}).Cached)

	// SaveGame is a plain beginOp'd mutation with no verify-visible effect
	// of its own, which is the point: the memo is dropped by the GATE, not
	// by a guess about what each flow touches.
	require.NoError(t, svc.SaveGame(context.Background(), game))

	assert.False(t, verifyOnce(t, svc, game, core.VerifyOptions{}).Cached,
		"a mutation must invalidate the memo")
}

// TestVerify_ForceAlwaysRuns: `lmm verify` typed by a user means a real
// run, and so does the Health card's Re-verify. Force also REPLACES the
// memo, so the next hydrate is served the fresh answer.
func TestVerify_ForceAlwaysRuns(t *testing.T) {
	svc, game := newVerifyTestServiceWithFiles(t, 2)

	first := verifyOnce(t, svc, game, core.VerifyOptions{})
	require.False(t, first.Cached)
	require.True(t, verifyOnce(t, svc, game, core.VerifyOptions{}).Cached)

	forced := verifyOnce(t, svc, game, core.VerifyOptions{Force: true})
	assert.False(t, forced.Cached, "Force always runs")
	assert.True(t, forced.CheckedAt.After(first.CheckedAt), "and its answer is a new one")

	next := verifyOnce(t, svc, game, core.VerifyOptions{})
	assert.True(t, next.Cached)
	assert.Equal(t, forced.CheckedAt, next.CheckedAt, "the forced run replaced the memo")
}

// TestVerify_MemoIsPerGameProfileAndTier: three keys, three separate
// answers - a profile switch must never be handed the other profile's
// verdict, and the offline tier must never answer for the full one.
func TestVerify_MemoIsPerGameProfileAndTier(t *testing.T) {
	svc, game := newVerifyTestServiceWithFiles(t, 1)

	require.False(t, verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal}).Cached)
	assert.False(t, verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyFull}).Cached,
		"the full tier asks the source; the local tier's answer cannot stand in for it")
	assert.True(t, verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyFull}).Cached)

	other := &domain.Game{ID: "other-game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), other))
	_, err := svc.NewProfileManager().Create(context.Background(), other.ID, "default")
	require.NoError(t, err)
	assert.False(t, verifyOnce(t, svc, other, core.VerifyOptions{Tier: core.VerifyFull}).Cached,
		"another game has its own memo")
}

// TestVerify_MemoNeverServesAFixAFilterOrASink pins the three shapes that
// always run: a repair, a single-mod filter, and any caller that passed a
// progress sink (a memo hit has no events to emit).
func TestVerify_MemoNeverServesAFixAFilterOrASink(t *testing.T) {
	svc, game := newVerifyTestServiceWithFiles(t, 2)
	require.False(t, verifyOnce(t, svc, game, core.VerifyOptions{}).Cached)
	require.True(t, verifyOnce(t, svc, game, core.VerifyOptions{}).Cached)

	filtered, err := svc.VerifyForTest(context.Background(), game, "default",
		core.VerifyOptions{ModFilter: "mod-ok"}, nil)
	require.NoError(t, err)
	assert.False(t, filtered.Cached, "a filtered run is not the whole-profile answer")

	sink, events := core.RecordEvents()
	withSink, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{}, sink)
	require.NoError(t, err)
	assert.False(t, withSink.Cached, "a caller watching progress gets a real run")
	assert.NotEmpty(t, *events)

	fixed, err := svc.VerifyForTest(context.Background(), game, "default",
		core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	assert.False(t, fixed.Cached, "a repair never answers from a memo")
}

// TestVerify_MemoMissesAfterAnOutOfProcessChecksumBackfill: the
// deployed_files checksum rows are the FIRST thing a verify run reads
// (GetFilesWithChecksums), and the only input that moves the verdict
// without moving anything on disk. `lmm verify --fix`'s checksum backfill
// writes exactly those rows and touches neither the deployed tree, the
// profile nor installed_mods.
//
// In-process that is safe - the repair takes beginOp, which drops the memo
// - but #317 makes "the CLI beside a running `lmm serve`" the sanctioned
// workflow rather than a warned-against one, and a second process's write
// reaches no beginOp of ours. Without the rows in the fingerprint the
// Health card kept reporting a warning that had been repaired minutes ago,
// indefinitely, until a mutation happened to land in the serve process or
// the user pressed Re-verify.
func TestVerify_MemoMissesAfterAnOutOfProcessChecksumBackfill(t *testing.T) {
	cfgDir, dataDir, cacheDir := t.TempDir(), t.TempDir(), t.TempDir()
	open := func() *core.Service {
		svc, err := core.NewService(core.ServiceConfig{ConfigDir: cfgDir, DataDir: dataDir, CacheDir: cacheDir})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, svc.Close()) })
		return svc
	}

	svc := open()
	game := &domain.Game{ID: "test-game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	// Seeded WITHOUT a checksum, which is the state `verify --fix` repairs:
	// the local tier reports one no_checksum warning for it.
	seedVerifyMod(t, svc, game, "src", "mod-ok", "Mod OK", "1.0", []string{"file-0"}, true)

	first := verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal})
	require.False(t, first.Cached)
	require.Equal(t, 1, first.Warnings, "the un-checksummed file is the warning this repair clears")
	require.True(t, verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal}).Cached)

	// The second process. Its own beginOp cannot reach our memo.
	other := open()
	require.NoError(t, other.SaveFileChecksum(context.Background(),
		"src", "mod-ok", game.ID, "default", "file-0", "backfilled"))

	after := verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal})
	assert.False(t, after.Cached, "a checksum backfill changes what verify reads, so it must invalidate the memo")
	assert.Zero(t, after.Warnings, "and the repaired state is what the surface reports from then on")
}

// TestVerify_MemoHitDoesNotShareItsFindingsSlice: a hit is a shallow copy
// of the stored entry, so without a clone every caller - and the one that
// ran the original verify - would hold the same backing array. Nothing
// outside core writes to Findings today, which is why the review filed
// this Minor; the guard was a comment, and this makes it a test.
func TestVerify_MemoHitDoesNotShareItsFindingsSlice(t *testing.T) {
	svc, game := newVerifyTestServiceWithFiles(t, 3)

	first := verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal})
	require.False(t, first.Cached)
	require.NotEmpty(t, first.Findings)

	hit := verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal})
	require.True(t, hit.Cached)
	require.Equal(t, first.Findings, hit.Findings)

	// A caller mutating what it was handed cannot reach the memo, nor the
	// result of any other caller.
	hit.Findings[0].Status = "clobbered"
	again := verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal})
	require.True(t, again.Cached)
	assert.NotEqual(t, "clobbered", again.Findings[0].Status, "the memo kept its own copy")
	assert.NotEqual(t, "clobbered", first.Findings[0].Status, "and so did the original caller")
}

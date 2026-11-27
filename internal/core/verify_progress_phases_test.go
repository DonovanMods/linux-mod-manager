package core_test

// #677: the passes after the version pass - the per-file walk, the
// deployed-content pass, the blocked-paths pass, the records pass and the
// convergence - said nothing while they ran, so a frontend held the version
// pass's last tick ("16/16") for most of a long verify. Each now reports
// itself through a VerifyEvProgress tick naming its phase, throttled so a
// pass over 23k deployed files does not emit 23k events.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// progressTicks runs a verify of f's default profile and returns its
// VerifyEvProgress ticks, each with when it arrived.
type progressTick struct {
	core.VerifyEvent
	at time.Time
}

func verifyProgressTicks(t *testing.T, f *legacyFixture, opts core.VerifyOptions) ([]progressTick, *core.VerifyReport) {
	t.Helper()
	var ticks []progressTick
	report, err := f.svc.VerifyReport(context.Background(), f.game, "default", opts, func(e core.Event) {
		if ev, ok := e.(core.VerifyEvent); ok && ev.Kind == core.VerifyEvProgress {
			ticks = append(ticks, progressTick{ev, time.Now()})
		}
	})
	require.NoError(t, err)
	return ticks, report
}

// verifyPhasesOf is the distinct phases of ticks, in the order they began.
func verifyPhasesOf(ticks []progressTick) []core.VerifyPhase {
	var phases []core.VerifyPhase
	for _, tk := range ticks {
		if len(phases) == 0 || phases[len(phases)-1] != tk.Phase {
			phases = append(phases, tk.Phase)
		}
	}
	return phases
}

// verifyTicksOf is the ticks of one phase.
func verifyTicksOf(ticks []progressTick, phase core.VerifyPhase) []progressTick {
	var out []progressTick
	for _, tk := range ticks {
		if tk.Phase == phase {
			out = append(out, tk)
		}
	}
	return out
}

// numberedFiles is n files under dir, each with its own content.
func numberedFiles(dir string, n int) map[string]string {
	files := make(map[string]string, n)
	for i := range n {
		files[fmt.Sprintf("%s/f%05d.lua", dir, i)] = fmt.Sprintf("file %d", i)
	}
	return files
}

func TestVerify_LaterPassesReportTheirPhase(t *testing.T) {
	f := newLegacyFixture(t, handoffGame(t, "wow", t.TempDir(), domain.LinkSymlink))
	f.profile(t, "default", true, "a", "b")
	f.deployed(t, "default", "a", domain.LinkSymlink, numberedFiles("AddOns/A", 3), nil)
	f.deployed(t, "default", "b", domain.LinkSymlink, numberedFiles("AddOns/B", 2), nil)
	// A checksummed file record, so the per-file walk has a row to walk.
	require.NoError(t, f.svc.ExecForTest(context.Background(),
		`INSERT INTO installed_mod_files (source_id, mod_id, game_id, profile_name, file_id, checksum) VALUES ('local', 'a', ?, 'default', 'archive', 'abc')`, f.game.ID))

	ticks, _ := verifyProgressTicks(t, f, core.VerifyOptions{Tier: core.VerifyFull, Force: true})

	assert.Equal(t, []core.VerifyPhase{
		core.VerifyPhaseVersions, core.VerifyPhaseFiles, core.VerifyPhaseDeployed,
		core.VerifyPhaseBlocked, core.VerifyPhaseRecords, core.VerifyPhaseConvergence,
	}, verifyPhasesOf(ticks))

	versions := verifyTicksOf(ticks, core.VerifyPhaseVersions)
	require.Len(t, versions, 2, "the version pass still ticks once per mod")
	assert.Equal(t, "Mod a", versions[0].ModName)

	files := verifyTicksOf(ticks, core.VerifyPhaseFiles)
	require.Len(t, files, 1)
	assert.Equal(t, 1, files[0].Index)
	assert.Equal(t, 1, files[0].Total)

	deployed := verifyTicksOf(ticks, core.VerifyPhaseDeployed)
	require.Len(t, deployed, 5, "a small pass ticks every file")
	for i, tk := range deployed {
		assert.Equal(t, i+1, tk.Index)
		assert.Equal(t, 5, tk.Total)
		assert.Equal(t, core.OpVerify, tk.Op)
	}

	blocked := verifyTicksOf(ticks, core.VerifyPhaseBlocked)
	require.Len(t, blocked, 2, "one tick per installed mod")
	assert.Equal(t, 2, blocked[1].Index)
	assert.Equal(t, 2, blocked[1].Total)

	// The records pass and the convergence say what they are doing, with
	// no position to report.
	for _, phase := range []core.VerifyPhase{core.VerifyPhaseRecords, core.VerifyPhaseConvergence} {
		one := verifyTicksOf(ticks, phase)
		require.Len(t, one, 1, phase)
		assert.Zero(t, one[0].Index, phase)
		assert.Zero(t, one[0].Total, phase)
	}
}

// A profile with nothing checksummed - the #217 branch - reports the same
// later phases, with no file walk.
func TestVerify_LaterPhasesOnAProfileWithNoChecksummedFiles(t *testing.T) {
	f := newLegacyFixture(t, handoffGame(t, "wow", t.TempDir(), domain.LinkSymlink))
	f.profile(t, "default", true, "a")
	f.deployed(t, "default", "a", domain.LinkSymlink, numberedFiles("AddOns/A", 2), nil)

	ticks, report := verifyProgressTicks(t, f, core.VerifyOptions{Force: true})
	require.False(t, report.Result.HasFiles)
	assert.Equal(t, []core.VerifyPhase{
		core.VerifyPhaseDeployed, core.VerifyPhaseBlocked, core.VerifyPhaseRecords, core.VerifyPhaseConvergence,
	}, verifyPhasesOf(ticks))
}

// A long pass is throttled to a tick per whole percent, plus its first and
// last, so its frontends are not flooded.
func TestVerify_DeployedPhaseTicksAreThrottled(t *testing.T) {
	f := newLegacyFixture(t, handoffGame(t, "wow", t.TempDir(), domain.LinkSymlink))
	f.profile(t, "default", true, "a")
	f.deployed(t, "default", "a", domain.LinkSymlink, numberedFiles("AddOns/A", 1000), nil)

	ticks, _ := verifyProgressTicks(t, f, core.VerifyOptions{Force: true})
	deployed := verifyTicksOf(ticks, core.VerifyPhaseDeployed)
	require.NotEmpty(t, deployed)
	assert.LessOrEqual(t, len(deployed), 101)
	assert.Equal(t, 1, deployed[0].Index, "the pass's first tick says it started")
	last := deployed[len(deployed)-1]
	assert.Equal(t, 1000, last.Index, "its last says it finished")
	assert.Equal(t, 1000, last.Total)
	for i := 1; i < len(deployed); i++ {
		assert.Greater(t, deployed[i].Index, deployed[i-1].Index)
	}
}

// TestVerify_DeployedPassIsFastOnALargeProfile is #677's performance check,
// on a synthetic profile shaped like the owner's WoW one: 16 addons, symlink
// deployed, 20k deployed_files rows. Each deployed file's judgement looks up
// the path's records; before the path index that lookup scanned the game's
// every row, and this pass took tens of seconds (37 s of a 45 s verify at
// 23k rows). It now takes well under a second; the budget is generous so a
// slow runner does not flake. Under -race the detector's instrumentation of
// the pure-Go SQLite makes any wall-clock budget meaningless, so it skips
// there: the query-plan test in internal/storage/db is the guard that runs
// everywhere.
func TestVerify_DeployedPassIsFastOnALargeProfile(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("a wall-clock check over 20k seeded deployed files; skipped under -short and -race")
	}
	const mods, perMod = 16, 1250
	f := newLegacyFixture(t, handoffGame(t, "wow", t.TempDir(), domain.LinkSymlink))
	refs := make([]string, mods)
	for m := range mods {
		refs[m] = fmt.Sprintf("addon%02d", m)
		seedSymlinkedMod(t, f, "default", refs[m], perMod)
	}
	f.profile(t, "default", true, refs...)

	started := time.Now()
	ticks, report := verifyProgressTicks(t, f, core.VerifyOptions{Force: true})
	total := time.Since(started)

	deployed := verifyTicksOf(ticks, core.VerifyPhaseDeployed)
	require.NotEmpty(t, deployed)
	require.Equal(t, mods*perMod, deployed[len(deployed)-1].Total)
	blocked := verifyTicksOf(ticks, core.VerifyPhaseBlocked)
	require.NotEmpty(t, blocked)
	pass := blocked[0].at.Sub(deployed[0].at)
	t.Logf("verify of %d deployed files: deployed-content pass %v, whole verify %v", mods*perMod, pass, total)
	assert.Less(t, pass, 10*time.Second, "the deployed-content pass over %d files", mods*perMod)
	for _, fd := range report.Result.Findings {
		assert.NotEqual(t, core.VerifyStatusDeployedModified, fd.Status, "%+v", fd)
	}
}

// seedSymlinkedMod is legacyFixture.deployed for a mod of n files, written
// in bulk: the cache entry's files, a symlink to each in the game
// directory, and one statement recording every deployed_files row.
func seedSymlinkedMod(t *testing.T, f *legacyFixture, profile, modID string, n int) {
	t.Helper()
	ctx := context.Background()
	entry := f.svc.GetGameCache(f.game).ModPath(cache.Key{GameID: f.game.ID, SourceID: "local", ModID: modID, Version: "unknown"})
	dir := "Interface/AddOns/" + modID
	require.NoError(t, os.MkdirAll(filepath.Join(entry, filepath.FromSlash(dir)), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(f.game.ModPath, filepath.FromSlash(dir)), 0o755))
	for i := range n {
		rel := filepath.FromSlash(fmt.Sprintf("%s/f%05d.lua", dir, i))
		src := filepath.Join(entry, rel)
		require.NoError(t, os.WriteFile(src, []byte(rel), 0o644))
		require.NoError(t, os.Symlink(src, filepath.Join(f.game.ModPath, rel)))
	}
	require.NoError(t, f.svc.ExecForTest(ctx, `
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i + 1 < ?)
		INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id)
		SELECT ?, ?, printf('%s/f%05d.lua', ?, i), 'local', ? FROM n`,
		n, f.game.ID, profile, dir, modID))
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "local", Name: "Mod " + modID, Version: "unknown", GameID: f.game.ID},
		ProfileName:  profile,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   domain.LinkSymlink,
	}))
}

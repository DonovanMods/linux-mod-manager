package core_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heldNewFile is the file heldDeploy's deploy places for the first time.
const heldNewFile = "Data/k3.esp"

// heldDeploy gives mod k a file it has not deployed yet - what an update's
// new version brings - and starts a deploy of f's profile, held once that
// file is placed and before its deployed_files row is written: the
// half-written tree a Mission Control hydrate walked during #588's
// automatic update, where lmm's own new file reads as one "lmm did not
// put". It returns once the deploy is held there; release lets it finish,
// and done receives its error when it has.
func heldDeploy(t *testing.T, f *legacyFixture) (release func(), done <-chan error) {
	t.Helper()
	require.NoError(t, f.svc.GetGameCache(f.game).Store(f.game.ID, "local", "k", "unknown", heldNewFile, []byte("mod k3")))
	reached := make(chan struct{})
	gate := make(chan struct{})
	var reachOnce, releaseOnce sync.Once
	f.svc.SetAfterDeployLinkForTest(func(file string) {
		if filepath.ToSlash(file) != heldNewFile {
			return
		}
		reachOnce.Do(func() { close(reached) })
		<-gate
	})
	release = func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)

	finished := make(chan error, 1)
	go func() {
		_, err := f.svc.DeployProfile(context.Background(), f.game, "default", core.DeployOptions{}, nil)
		finished <- err
	}()
	select {
	case <-reached:
	case err := <-finished:
		t.Fatalf("the deploy finished without placing a file: %v", err)
	}
	return release, finished
}

// noBlockedFinding fails when result carries #588's false "not deployed".
func noBlockedFinding(t *testing.T, result *core.VerifyResult) {
	t.Helper()
	for _, fd := range result.Findings {
		assert.NotEqual(t, core.VerifyStatusDeployedBlocked, fd.Status, "a file the running deploy placed is not the user's: %+v", fd)
	}
}

// TestVerify_DuringADeployIsDeferredWithoutALastCheck is #588: a health
// check landing while a deploy has placed a file but not yet recorded it
// must not walk the tree. With no earlier answer to show, it says it is
// deferred and reports nothing - and it answers at once rather than waiting
// for the deploy (the test would hang if it waited).
func TestVerify_DuringADeployIsDeferredWithoutALastCheck(t *testing.T) {
	f := ledgerState(t, domain.LinkSymlink)
	release, done := heldDeploy(t, f)

	for _, opts := range []core.VerifyOptions{
		{Tier: core.VerifyLocal},
		{Tier: core.VerifyLocal, Force: true},
	} {
		result, err := f.svc.VerifyForTest(context.Background(), f.game, "default", opts, nil)
		require.NoError(t, err)
		assert.Equal(t, core.VerifyDeferredJobRunning, result.Deferred, "force=%t", opts.Force)
		assert.Empty(t, result.Findings, "nothing walked, nothing reported")
		assert.Zero(t, result.Issues+result.Warnings)
		assert.True(t, result.CheckedAt.IsZero(), "no check happened, so none is dated")
		noBlockedFinding(t, result)
	}

	release()
	require.NoError(t, <-done)
	after, err := f.svc.VerifyForTest(context.Background(), f.game, "default", core.VerifyOptions{Tier: core.VerifyLocal}, nil)
	require.NoError(t, err)
	assert.Empty(t, after.Deferred, "with the deploy done, the check runs")
	assert.False(t, after.Cached)
	noBlockedFinding(t, after)
	assert.Zero(t, after.Issues+after.Warnings, "%+v", after.Findings)
}

// TestVerify_DuringADeployShowsTheLastCheck: with an earlier answer for the
// same game, profile and tier, the deferred check hands that back, dated as
// it was, rather than an empty one - even though the deploy's own gate has
// already invalidated it as a match for the installation.
func TestVerify_DuringADeployShowsTheLastCheck(t *testing.T) {
	f := ledgerState(t, domain.LinkSymlink)
	before, err := f.svc.VerifyForTest(context.Background(), f.game, "default", core.VerifyOptions{Tier: core.VerifyLocal}, nil)
	require.NoError(t, err)
	require.Empty(t, before.Deferred)

	release, done := heldDeploy(t, f)
	stale, err := f.svc.VerifyForTest(context.Background(), f.game, "default", core.VerifyOptions{Tier: core.VerifyLocal}, nil)
	require.NoError(t, err)
	assert.Equal(t, core.VerifyDeferredJobRunning, stale.Deferred)
	assert.Equal(t, before.CheckedAt, stale.CheckedAt, "the last check, dated when it ran")
	assert.Equal(t, before.Findings, stale.Findings)
	assert.False(t, stale.Cached, "not 'unchanged since' - the installation is changing")
	noBlockedFinding(t, stale)

	release()
	require.NoError(t, <-done)
	after, err := f.svc.VerifyForTest(context.Background(), f.game, "default", core.VerifyOptions{Tier: core.VerifyLocal}, nil)
	require.NoError(t, err)
	assert.Empty(t, after.Deferred)
	assert.False(t, after.Cached, "the deploy invalidated the last check as a match")
	assert.True(t, after.CheckedAt.After(before.CheckedAt))
}

// TestVerify_AMutationDuringTheWalkIsNotMemoised: a mutation that starts
// (here: runs to completion) while a check is walking leaves that walk
// unreliable. It is reported as deferred, not as findings, and is never
// stored as the answer the next hydrate gets.
func TestVerify_AMutationDuringTheWalkIsNotMemoised(t *testing.T) {
	svc, game := newVerifyTestServiceWithFiles(t, 2)

	var once sync.Once
	report, err := svc.VerifyReportWithProgress(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyLocal}, func(core.Event) {
		once.Do(func() { require.NoError(t, svc.SaveGame(context.Background(), game)) })
	})
	require.NoError(t, err)
	assert.Equal(t, core.VerifyDeferredJobRunning, report.Result.Deferred)
	assert.Empty(t, report.Result.Findings)

	next := verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal})
	assert.False(t, next.Cached, "a walk a mutation overlapped must not be the memo")
	assert.Empty(t, next.Deferred)
}

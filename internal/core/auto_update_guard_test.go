package core_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auto_update_guard_test.go - #586's review: an automatic update never
// installs, nor restarts lmm-serve.service, while an lmm mutation is
// running; it records the update before the restart; and a release check
// that fails backs off as an install that fails does.

// holdMutationLock takes path's cross-process mutation lock the way another lmm
// mid-mutation holds it, until the returned func.
func holdMutationLock(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// guardFixture is autoFixture with the mutation lock configured.
func guardFixture(t *testing.T) (*selfUpdateFixture, *clock, string) {
	t.Helper()
	f, c := autoFixture(t)
	lock := filepath.Join(f.opts.DataDir, "lmm.lock")
	f.opts.OpLockPath = lock
	return f, c, lock
}

func autoStatus(f *selfUpdateFixture) core.AutoUpdateStatus {
	return core.ServiceConfig{ConfigDir: f.opts.ConfigDir, DataDir: f.opts.DataDir}.AutoUpdateStatus(core.InstallMethod{Method: core.InstallSelfManaged})
}

func TestRunAuto_StepsAsideWhileAnotherLmmIsMidMutation(t *testing.T) {
	f, c, lock := guardFixture(t)
	release := holdMutationLock(t, lock)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	u := f.updater(rel.serve(t))

	out, err := u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.NoError(t, err, "a running mutation is a reason to wait, not a failure")
	assert.Contains(t, out.Skipped, "operation is in progress")
	assert.False(t, out.Result.Updated)
	data, _ := os.ReadFile(f.exe)
	assert.Equal(t, "old lmm", string(data))
	assert.Zero(t, f.restarts.calls)
	assert.Nil(t, autoStatus(f).Failure, "no failure, no back-off")

	release()
	c.now = c.now.Add(61 * time.Minute)
	assert.True(t, u.AutoUpdateDue("2.2.0"), "tried again after the usual hold")
	out, err = u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.NoError(t, err)
	assert.True(t, out.Result.Updated)
}

func TestRunAuto_StepsAsideWhileThisServerRunsAJob(t *testing.T) {
	f, _, _ := guardFixture(t)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	u := f.updater(rel.serve(t))
	resumed := 0
	out, err := u.RunAuto(context.Background(), core.AutoRunOptions{
		Quiesce: func() (func(), bool) { return func() { resumed++ }, false },
	})
	require.NoError(t, err)
	assert.Contains(t, out.Skipped, "operation is in progress")
	assert.False(t, out.Result.Updated)
	assert.Empty(t, rel.requests, "nothing is asked of GitHub while a job runs")
	assert.Nil(t, autoStatus(f).Failure)
}

// TestRunAuto_NeverRestartsServe (#586 review): lmm-update.timer's run and
// the CLI's detached one see only the cross-process mutation lock, which a
// serve job holds for part of its life - an install's download runs outside
// it - so no RunAuto restarts lmm-serve.service. It replaces the binary and
// stops; lmm serve sees the newer binary and restarts itself
// (RestartServeWhenIdle).
func TestRunAuto_NeverRestartsServe(t *testing.T) {
	for name, opts := range map[string]core.AutoRunOptions{
		"the timer, or the CLI's detached run": {},
		"lmm serve's own run":                  {Quiesce: func() (func(), bool) { return func() {}, true }},
	} {
		t.Run(name, func(t *testing.T) {
			f, _, _ := guardFixture(t)
			f.restarts.restarted = true
			out, err := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t)).RunAuto(context.Background(), opts)
			require.NoError(t, err)
			assert.True(t, out.Result.Updated)
			assert.Zero(t, f.restarts.calls, "the binary is replaced; lmm-serve.service is not restarted")
			assert.False(t, out.Result.ServeRestarted)
			_, ok := core.ServiceConfig{ConfigDir: f.opts.ConfigDir, DataDir: f.opts.DataDir}.PendingAutoUpdateNotice()
			assert.True(t, ok, "recorded")
		})
	}
}

// restartUpdater is an updater whose only use is RestartServeWhenIdle: it
// asks nothing of GitHub.
func restartUpdater(f *selfUpdateFixture) *core.SelfUpdater {
	opts := f.opts
	opts.APIBase, opts.DownloadBase = "http://127.0.0.1:0", "http://127.0.0.1:0"
	return core.NewSelfUpdater(opts)
}

// TestRestartServeWhenIdle_WaitsForThisServersJobs: lmm serve restarting
// itself onto a binary replaced under it asks systemd only once none of its
// jobs runs, keeping new ones off while it asks, and gives up after the
// wait.
func TestRestartServeWhenIdle_WaitsForThisServersJobs(t *testing.T) {
	run := func(t *testing.T, idleAfter int, restarted bool) (*selfUpdateFixture, bool, error, int) {
		f, _, _ := guardFixture(t)
		f.restarts.restarted = restarted
		calls, resumed := 0, 0
		ok, err := restartUpdater(f).RestartServeWhenIdle(context.Background(), core.AutoRunOptions{
			RestartWait: 150 * time.Millisecond,
			Quiesce: func() (func(), bool) {
				calls++
				idle := idleAfter >= 0 && calls > idleAfter
				// Counts the resumes of a hold that was taken: idle.
				return func() {
					if idle {
						resumed++
					}
				}, idle
			},
		})
		return f, ok, err, resumed
	}
	t.Run("a job runs past the wait", func(t *testing.T) {
		f, ok, err, _ := run(t, -1, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "operation")
		assert.False(t, ok)
		assert.Zero(t, f.restarts.calls, "never restarted under a running job")
	})
	t.Run("the job ends in time", func(t *testing.T) {
		f, ok, err, resumed := run(t, 2, true)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, 1, f.restarts.calls)
		assert.Zero(t, resumed, "the restart's hold is kept: the server is going down")
	})
	t.Run("idle, and this server is not the service", func(t *testing.T) {
		f, ok, err, resumed := run(t, 0, false)
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Equal(t, 1, f.restarts.calls)
		assert.Equal(t, 1, resumed, "nothing restarted: jobs may start again")
	})
	t.Run("systemctl fails", func(t *testing.T) {
		f, _, _ := guardFixture(t)
		f.restarts.err = errors.New("systemctl --user try-restart: boom")
		resumed := 0
		ok, err := restartUpdater(f).RestartServeWhenIdle(context.Background(), core.AutoRunOptions{
			Quiesce: func() (func(), bool) { return func() { resumed++ }, true },
		})
		require.ErrorContains(t, err, "boom")
		assert.False(t, ok)
		assert.Equal(t, 1, resumed)
	})
}

// TestRestartServeWhenIdle_NeverHoldsTheMutationLock (#586 review): another
// lmm's mutation is not this server's to wait for - restarting lmm serve
// does not touch it - and no restart holds the mutation lock while it asks
// systemd, which would leave a waiting stop to wait behind it.
func TestRestartServeWhenIdle_NeverHoldsTheMutationLock(t *testing.T) {
	f, _, lock := guardFixture(t)
	f.restarts.restarted = true
	var freeAtRestart bool
	f.restarts.onRestart = func() { freeAtRestart = lockFree(lock) }
	ok, err := restartUpdater(f).RestartServeWhenIdle(context.Background(), core.AutoRunOptions{
		RestartWait: 150 * time.Millisecond,
		Quiesce:     func() (func(), bool) { return func() {}, true },
	})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.True(t, freeAtRestart, "the mutation lock is not taken around systemctl")

	release := holdMutationLock(t, lock)
	defer release()
	f.restarts.calls = 0
	ok, err = restartUpdater(f).RestartServeWhenIdle(context.Background(), core.AutoRunOptions{
		RestartWait: 150 * time.Millisecond,
		Quiesce:     func() (func(), bool) { return func() {}, true },
	})
	require.NoError(t, err, "a CLI command's mutation does not hold off this server's restart")
	assert.True(t, ok)
	assert.Equal(t, 1, f.restarts.calls)
}

// TestRestartServeWhenIdle_WithoutARestarterRestartsNothing: an lmm serve
// started by hand has none; its banner asks for the restart.
func TestRestartServeWhenIdle_WithoutARestarterRestartsNothing(t *testing.T) {
	f, _, _ := guardFixture(t)
	f.opts.Restarter = nil
	ok, err := restartUpdater(f).RestartServeWhenIdle(context.Background(), core.AutoRunOptions{
		Quiesce: func() (func(), bool) { t.Fatal("nothing to quiesce for"); return nil, false },
	})
	require.NoError(t, err)
	assert.False(t, ok)
}

// lockFree reports whether path's lock can be taken right now.
func lockFree(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}

func TestRunAuto_AFailingReleaseCheckBacksOff(t *testing.T) {
	f, c, _ := guardFixture(t)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	rel.latestStatus = http.StatusBadGateway
	u := f.updater(rel.serve(t))
	t0 := c.now

	_, err := u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.Error(t, err)
	fail := autoStatus(f).Failure
	require.NotNil(t, fail, "a release check that fails is a failure too")
	assert.Empty(t, fail.Version, "no release was named")
	assert.Equal(t, t0.Add(time.Hour), fail.RetryAt)

	c.now = t0.Add(30 * time.Minute)
	assert.False(t, u.AutoUpdateDue("2.2.0"))
	before := len(rel.requests)
	out, err := u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.NoError(t, err, "a backed-off run is no new failure")
	assert.NotEmpty(t, out.Skipped)
	assert.Len(t, rel.requests, before, "nor a new request to GitHub")

	c.now = t0.Add(61 * time.Minute)
	assert.True(t, u.AutoUpdateDue("2.2.0"))
	_, err = u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.Error(t, err)
	assert.Equal(t, c.now.Add(2*time.Hour), autoStatus(f).Failure.RetryAt, "the back-off doubles")

	for range 10 {
		c.now = autoStatus(f).Failure.RetryAt.Add(time.Minute)
		_, err = u.RunAuto(context.Background(), core.AutoRunOptions{})
		require.Error(t, err)
	}
	assert.Equal(t, c.now.Add(24*time.Hour), autoStatus(f).Failure.RetryAt, "and stops at a day")

	rel.latestStatus = 0
	c.now = autoStatus(f).Failure.RetryAt.Add(time.Minute)
	out, err = u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.NoError(t, err)
	assert.True(t, out.Result.Updated)
	assert.Nil(t, autoStatus(f).Failure, "a success clears it")
}

func TestRunAuto_AnUnsettledInstallOrABadTagBacksOffForADay(t *testing.T) {
	t.Run("tag", func(t *testing.T) {
		f, c, _ := guardFixture(t)
		rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
		rel.tag = "v2.2.0-../../evil"
		_, err := f.updater(rel.serve(t)).RunAuto(context.Background(), core.AutoRunOptions{})
		var tagErr *core.ReleaseTagError
		require.ErrorAs(t, err, &tagErr)
		require.NotNil(t, autoStatus(f).Failure)
		assert.Equal(t, c.now.Add(24*time.Hour), autoStatus(f).Failure.RetryAt)
	})
	t.Run("install method", func(t *testing.T) {
		f, c, _ := guardFixture(t)
		f.opts.Probe.LookPath = func(name string) (string, error) {
			if name == "pacman" {
				return "/usr/bin/pacman", nil
			}
			return "", os.ErrNotExist
		}
		f.opts.Probe.Run = func(context.Context, string, ...string) ([]byte, error) {
			return nil, os.ErrPermission // the owner query itself fails: unsettled
		}
		_, err := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t)).RunAuto(context.Background(), core.AutoRunOptions{})
		var unknown *core.InstallMethodUnknownError
		require.ErrorAs(t, err, &unknown)
		require.NotNil(t, autoStatus(f).Failure)
		assert.Equal(t, c.now.Add(24*time.Hour), autoStatus(f).Failure.RetryAt)
	})
}

func TestAutoUpdateNotice_IsToldOnceWhicheverFrontendTellsIt(t *testing.T) {
	f, _, _ := guardFixture(t)
	_, err := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t)).RunAuto(context.Background(), core.AutoRunOptions{})
	require.NoError(t, err)
	st := autoStatus(f)
	require.NotNil(t, st.Last)
	assert.False(t, st.Noticed, "the web UI sees it is still to be told")

	// The web UI told it (POST /api/v1/updates/lmm/notice).
	rc := core.NewReleaseChecker(core.ReleaseCheckOptions{ConfigDir: f.opts.ConfigDir, DataDir: f.opts.DataDir, Version: "2.2.0", APIBase: "http://127.0.0.1:0"})
	require.NoError(t, rc.MarkAutoUpdateNoticed())
	assert.True(t, autoStatus(f).Noticed)
	_, ok := core.ServiceConfig{ConfigDir: f.opts.ConfigDir, DataDir: f.opts.DataDir}.PendingAutoUpdateNotice()
	assert.False(t, ok, "so the CLI does not tell it again")
}

// TestAutoUpdateDue_AFailureWithoutRetryAtBacksOffADayFromIt (#586
// review): an earlier build recorded a failed update with no retry_at; it
// is held off a day from its At, not retried at once.
func TestAutoUpdateDue_AFailureWithoutRetryAtBacksOffADayFromIt(t *testing.T) {
	f, c, _ := guardFixture(t)
	t0 := c.now
	require.NoError(t, os.WriteFile(filepath.Join(f.opts.DataDir, "auto-update.json"),
		[]byte(`{"failure":{"version":"2.2.0","at":"`+t0.Format(time.RFC3339)+`","error":"checksum mismatch"}}`), 0o644))
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	u := f.updater(rel.serve(t))

	c.now = t0.Add(2 * time.Hour)
	assert.False(t, u.AutoUpdateDue("2.2.0"))
	out, err := u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.NoError(t, err)
	assert.Contains(t, out.Skipped, "failed")
	assert.Equal(t, "old lmm", readFile(t, f.exe))

	c.now = t0.Add(25 * time.Hour)
	assert.True(t, u.AutoUpdateDue("2.2.0"))
}

// TestRunAuto_ACheckFailureNeverClearsAReleaseFailure (#586 review): a
// release whose update failed stays held off its day, through a release
// check that fails after it and one that then succeeds.
func TestRunAuto_ACheckFailureNeverClearsAReleaseFailure(t *testing.T) {
	f, c, _ := guardFixture(t)
	t0 := c.now
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	u := f.updater(rel.serve(t))
	// The install fails: lmm's directory is not writable.
	require.NoError(t, os.Chmod(filepath.Dir(f.exe), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(f.exe), 0o700) })
	_, err := u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.Error(t, err)
	require.NotNil(t, autoStatus(f).Failure)
	require.Equal(t, "2.2.0", autoStatus(f).Failure.Version)
	require.NoError(t, os.Chmod(filepath.Dir(f.exe), 0o700))

	c.now = t0.Add(2 * time.Hour)
	rel.latestStatus = http.StatusBadGateway
	_, err = u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.Error(t, err, "the release check fails")

	c.now = t0.Add(4 * time.Hour)
	rel.latestStatus = 0
	out, err := u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.NoError(t, err)
	assert.Contains(t, out.Skipped, "failed", "the release's own failure still holds it off")
	assert.Equal(t, "old lmm", readFile(t, f.exe))
	assert.False(t, u.AutoUpdateDue("2.2.0"))

	c.now = t0.Add(25 * time.Hour)
	require.True(t, u.AutoUpdateDue("2.2.0"))
	out, err = u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.NoError(t, err)
	assert.True(t, out.Result.Updated, "after its day it is tried again")
	assert.Nil(t, autoStatus(f).Failure)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

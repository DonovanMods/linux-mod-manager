package core_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func TestRunAuto_RecordsTheUpdateBeforeTheRestart(t *testing.T) {
	f, _, _ := guardFixture(t)
	f.restarts.restarted = true
	cfg := core.ServiceConfig{ConfigDir: f.opts.ConfigDir, DataDir: f.opts.DataDir}
	var recorded bool
	f.restarts.onRestart = func() { _, recorded = cfg.PendingAutoUpdateNotice() }
	u := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t))

	out, err := u.RunAuto(context.Background(), core.AutoRunOptions{})
	require.NoError(t, err)
	require.True(t, out.Result.ServeRestarted)
	assert.True(t, recorded, "a restarted lmm serve may exit before RunAuto returns: the record is already written")
}

func TestRunAuto_TheRestartWaitsForAMutationThatStartedDuringTheInstall(t *testing.T) {
	t.Run("it finishes in time", func(t *testing.T) {
		f, _, lock := guardFixture(t)
		f.restarts.restarted = true
		rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
		var lockedAtRestart bool
		f.restarts.onRestart = func() { lockedAtRestart = !lockFree(lock) }
		rel.onAsset = func(name string) {
			if strings.HasSuffix(name, ".tar.gz") {
				release := holdMutationLock(t, lock)
				time.AfterFunc(100*time.Millisecond, release)
			}
		}
		out, err := f.updater(rel.serve(t)).RunAuto(context.Background(), core.AutoRunOptions{RestartWait: 5 * time.Second})
		require.NoError(t, err)
		assert.True(t, out.Result.Updated)
		assert.True(t, out.Result.ServeRestarted, "restarted once the mutation let go")
		assert.True(t, lockedAtRestart, "the update holds the lock itself while it asks for the restart")
	})
	t.Run("it does not", func(t *testing.T) {
		f, _, lock := guardFixture(t)
		f.restarts.restarted = true
		rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
		rel.onAsset = func(name string) {
			if strings.HasSuffix(name, ".tar.gz") {
				holdMutationLock(t, lock)
			}
		}
		out, err := f.updater(rel.serve(t)).RunAuto(context.Background(), core.AutoRunOptions{RestartWait: 150 * time.Millisecond})
		require.NoError(t, err, "the update itself succeeded")
		assert.True(t, out.Result.Updated)
		assert.Zero(t, f.restarts.calls, "never restarted under a running mutation")
		assert.False(t, out.Result.ServeRestarted)
		assert.Contains(t, out.Result.ServeRestartError, "operation")
		_, ok := core.ServiceConfig{ConfigDir: f.opts.ConfigDir, DataDir: f.opts.DataDir}.PendingAutoUpdateNotice()
		assert.True(t, ok, "recorded all the same")
	})
}

func TestRunAuto_TheRestartWaitsForThisServersJobs(t *testing.T) {
	run := func(t *testing.T, idleAtRestart, restarted bool) (*selfUpdateFixture, core.AutoUpdateOutcome, int) {
		f, _, _ := guardFixture(t)
		f.restarts.restarted = restarted
		calls, resumed := 0, 0
		out, err := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t)).RunAuto(context.Background(), core.AutoRunOptions{
			RestartWait: 150 * time.Millisecond,
			Quiesce: func() (func(), bool) {
				calls++
				return func() { resumed++ }, calls == 1 || idleAtRestart
			},
		})
		require.NoError(t, err)
		return f, out, resumed
	}
	t.Run("a job is running", func(t *testing.T) {
		f, out, _ := run(t, false, true)
		assert.True(t, out.Result.Updated)
		assert.Zero(t, f.restarts.calls)
		assert.NotEmpty(t, out.Result.ServeRestartError)
	})
	t.Run("idle, and this server is the service", func(t *testing.T) {
		f, out, resumed := run(t, true, true)
		assert.Equal(t, 1, f.restarts.calls)
		assert.True(t, out.Result.ServeRestarted)
		assert.Equal(t, 1, resumed, "the pre-install check resumes; the restart's hold is kept, the server is going down")
	})
	t.Run("idle, and this server is not the service", func(t *testing.T) {
		f, out, resumed := run(t, true, false)
		assert.Equal(t, 1, f.restarts.calls)
		assert.False(t, out.Result.ServeRestarted)
		assert.Equal(t, 2, resumed, "nothing restarted: jobs may start again")
	})
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

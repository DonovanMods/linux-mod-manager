package serve

// #586 review: `lmm serve` never lets an automatic update restart it under
// a running job, waits for the update's record before it exits, and tells
// the update once whichever frontend tells it.

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobRegistry_QuiesceRefusesWhileAJobRunsAndHoldsOffNewOnes(t *testing.T) {
	r := newJobRegistry(t.Context(), slog.New(slog.DiscardHandler), 0, 0)
	t.Cleanup(func() { r.shutdown(t.Context()) })
	gate := make(chan struct{})
	_, err := r.Start("deploy", func(context.Context, core.EventSink) (any, error) {
		<-gate
		return nil, nil
	})
	require.NoError(t, err)

	_, idle := r.quiesce()
	assert.False(t, idle, "a running job: no update, no restart")

	close(gate)
	require.Eventually(t, func() bool { return r.QueueDepth() == 0 }, 5*time.Second, 5*time.Millisecond)
	resume, idle := r.quiesce()
	require.True(t, idle)
	_, err = r.Start("deploy", func(context.Context, core.EventSink) (any, error) { return nil, nil })
	require.ErrorIs(t, err, errRegistryClosing, "no job starts while the restart is being asked for")
	assert.ErrorIs(t, err, errRegistryRestarting)

	resume()
	_, err = r.Start("deploy", func(context.Context, core.EventSink) (any, error) { return nil, nil })
	assert.NoError(t, err, "nothing restarted: jobs start again")
}

func TestServe_AnAutomaticUpdateSeesTheRunningJob(t *testing.T) {
	svc, _ := newDeployFixtureService(t)
	rc := releaseChecker(t, "2.1.0", selfManagedProbe())
	idle := make(chan bool, 1)
	au := &fakeAutoUpdater{due: true, run: func(_ context.Context, opts core.AutoRunOptions) (core.AutoUpdateOutcome, error) {
		require.NotNil(t, opts.Quiesce, "serve hands its jobs to the update")
		resume, ok := opts.Quiesce()
		resume()
		idle <- ok
		return core.AutoUpdateOutcome{Skipped: "busy"}, nil
	}}
	srv := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr, ReleaseChecker: rc, AutoUpdater: au})
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	_, err := srv.jobs.Start("deploy", func(context.Context, core.EventSink) (any, error) {
		<-gate
		return nil, nil
	})
	require.NoError(t, err)
	require.NoError(t, rc.Refresh(t.Context()))

	srv.autoUpdate(t.Context())
	assert.False(t, <-idle, "the running deploy is seen")
}

func TestServe_ShutdownWaitsForTheAutomaticUpdateToFinishRecording(t *testing.T) {
	svc, _ := newDeployFixtureService(t)
	rc := releaseChecker(t, "2.1.0", selfManagedProbe())
	started := make(chan struct{})
	var recorded atomic.Bool
	au := &fakeAutoUpdater{due: true, run: func(ctx context.Context, _ core.AutoRunOptions) (core.AutoUpdateOutcome, error) {
		close(started)
		<-ctx.Done() // the restart it asked for arrives as the shutdown
		time.Sleep(200 * time.Millisecond)
		recorded.Store(true)
		return core.AutoUpdateOutcome{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(t.Context())
	srv := New(ctx, svc, slog.New(slog.DiscardHandler), Options{Addr: "127.0.0.1:0", ReleaseChecker: rc, AutoUpdater: au})
	_, err := srv.Listen()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	<-started
	cancel()
	require.NoError(t, <-done)
	assert.True(t, recorded.Load(), "Serve returned only after the update's goroutine did")
}

func TestAPIAutoUpdateNotice_MarksItToldForEveryFrontend(t *testing.T) {
	svc, _ := newDeployFixtureService(t)
	dataDir := t.TempDir()
	rc := releaseCheckerAt(t, svc.ConfigDir(), dataDir, "2.1.0", selfManagedProbe())
	srv := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr, ReleaseChecker: rc})
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "auto-update.json"),
		[]byte(`{"last":{"from":"2.0.0","to":"2.0.1","at":"2026-10-30T03:00:00Z","first":true},"updates":1}`), 0o644))

	status := func() core.ReleaseStatus {
		rec := doAPI(srv, http.MethodGet, "/api/v1/updates/lmm", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var st core.ReleaseStatus
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
		require.NotNil(t, st.AutoUpdate)
		return st
	}
	assert.False(t, status().AutoUpdate.Noticed)

	rec := doAPIWithoutCSRF(srv, http.MethodPost, "/api/v1/updates/lmm/notice", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, status().AutoUpdate.Noticed)

	rec = doAPI(srv, http.MethodPost, "/api/v1/updates/lmm/notice", "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.True(t, status().AutoUpdate.Noticed)
	_, pending := core.ServiceConfig{ConfigDir: svc.ConfigDir(), DataDir: dataDir}.PendingAutoUpdateNotice()
	assert.False(t, pending, "the CLI does not tell it again")
}

// countingRestarter counts the restarts of lmm-serve.service asked for.
type countingRestarter struct{ calls atomic.Int32 }

func (r *countingRestarter) RestartServeIfActive(context.Context) (bool, error) {
	r.calls.Add(1)
	return true, nil
}

// selfRestartFixture is a server running from a binary a test can replace
// with lmm 2.2.0, as lmm-update.timer or the CLI's detached update does,
// and whose updater restarts lmm-serve.service through restarts.
type selfRestartFixture struct {
	srv      *Server
	restarts *countingRestarter
	replace  func()
}

func newSelfRestartFixture(t *testing.T, ctx context.Context) *selfRestartFixture {
	t.Helper()
	svc, _ := newDeployFixtureService(t)
	exe := filepath.Join(t.TempDir(), "lmm")
	require.NoError(t, os.WriteFile(exe, []byte("2.0.1"), 0o755))
	probe := &core.InstallProbe{
		Executable: func() (string, error) { return exe, nil },
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == exe && len(args) == 1 && args[0] == "--version" {
				return []byte("lmm version 2.2.0\n"), nil
			}
			return nil, os.ErrNotExist
		},
	}
	dataDir := t.TempDir()
	// Up to date by GitHub's answer: no automatic update of its own runs,
	// only the restart onto what another process installed.
	rc := releaseCheckerAt(t, svc.ConfigDir(), dataDir, "2.0.1", probe)
	restarts := &countingRestarter{}
	au := core.NewSelfUpdater(core.SelfUpdateOptions{
		ConfigDir: svc.ConfigDir(), DataDir: dataDir, Version: "2.0.1",
		APIBase: "http://127.0.0.1:0", DownloadBase: "http://127.0.0.1:0",
		Probe: probe, Restarter: restarts,
	})
	srv := New(ctx, svc, slog.New(slog.DiscardHandler), Options{Addr: "127.0.0.1:0", ReleaseChecker: rc, AutoUpdater: au})
	return &selfRestartFixture{srv: srv, restarts: restarts, replace: func() {
		require.NoError(t, os.WriteFile(exe, []byte("2.2.0"), 0o755))
		later := time.Now().Add(time.Minute)
		require.NoError(t, os.Chtimes(exe, later, later))
	}}
}

// TestServe_RestartsItselfOntoANewerBinaryOnceItsJobEnds (#586 review):
// the timer and the CLI replace the binary and stop; lmm serve sees it and
// restarts itself, but only once its running job has ended.
func TestServe_RestartsItselfOntoANewerBinaryOnceItsJobEnds(t *testing.T) {
	f := newSelfRestartFixture(t, t.Context())
	gate := make(chan struct{})
	_, err := f.srv.jobs.Start("deploy", func(context.Context, core.EventSink) (any, error) {
		<-gate
		return nil, nil
	})
	require.NoError(t, err)
	f.replace()

	done := make(chan struct{})
	go func() { defer close(done); f.srv.restartOntoInstalled(t.Context()) }()
	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, f.restarts.calls.Load(), "never restarted under the running deploy")

	close(gate)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the restart did not follow the job's end")
	}
	assert.Equal(t, int32(1), f.restarts.calls.Load())
	_, err = f.srv.jobs.Start("deploy", func(context.Context, core.EventSink) (any, error) { return nil, nil })
	assert.ErrorIs(t, err, errRegistryRestarting, "no job starts while the restart arrives")
}

// TestServe_NoticesTheReplacedBinaryOnItsOwnAndRestartsWhenIdle: Serve
// looks at its binary often, so the restart follows the replacement
// within its check interval, not the next day's release check.
func TestServe_NoticesTheReplacedBinaryOnItsOwnAndRestartsWhenIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	f := newSelfRestartFixture(t, ctx)
	f.srv.installedCheckEvery = 20 * time.Millisecond
	_, err := f.srv.Listen()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- f.srv.Serve(ctx) }()

	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, f.restarts.calls.Load(), "nothing replaced: nothing restarted")
	f.replace()
	require.Eventually(t, func() bool { return f.restarts.calls.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), f.restarts.calls.Load(), "asked once for that binary")
	cancel()
	require.NoError(t, <-done)
}

// TestServe_DoesNotRestartItselfWhenAutoUpdateIsOff: with auto_update off
// the replacement was the person's own, and the banner asks them.
func TestServe_DoesNotRestartItselfWhenAutoUpdateIsOff(t *testing.T) {
	f := newSelfRestartFixture(t, t.Context())
	require.NoError(t, os.WriteFile(filepath.Join(f.srv.svc.ConfigDir(), "config.yaml"), []byte("auto_update: false\n"), 0o644))
	f.replace()
	f.srv.restartOntoInstalled(t.Context())
	assert.Zero(t, f.restarts.calls.Load())
	assert.Equal(t, "2.2.0", f.srv.releases.InstalledNewer(t.Context()), "the banner still says so")
}

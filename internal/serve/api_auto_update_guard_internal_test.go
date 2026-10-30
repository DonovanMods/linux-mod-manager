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

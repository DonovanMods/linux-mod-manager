package serve

// Internal (package serve) tests for the Server's ownership of the plan
// store and the job registry (docs/plans/2026-08-30-serve-impl.md Task 6):
// New roots both, and Serve's existing graceful drain now covers running
// jobs too - they get the same bounded grace in-flight requests get, and
// are cancelled only once it expires.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDrainServer builds a listening Server over a throwaway Service with
// the given shutdown grace, and returns it alongside the cancel that starts
// its drain and the channel Serve's return lands on.
func newDrainServer(t *testing.T, grace time.Duration) (*Server, context.CancelFunc, <-chan error) {
	t.Helper()
	sandboxEnv(t)

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	srv := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{
		Addr:          "127.0.0.1:0",
		ShutdownGrace: grace,
	})
	_, err = srv.Listen()
	require.NoError(t, err)

	serveCtx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(serveCtx) }()
	t.Cleanup(cancel)

	return srv, cancel, done
}

func TestServer_NewWiresThePlanStoreAndJobRegistry(t *testing.T) {
	type ctxKey string
	const key ctxKey = "server-root-marker"

	sandboxEnv(t)
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	srv := New(context.WithValue(t.Context(), key, "present"), svc, slog.New(slog.DiscardHandler), Options{Addr: "127.0.0.1:7420"})
	require.NotNil(t, srv.plans, "New must wire a plan store")
	require.NotNil(t, srv.jobs, "New must wire a job registry")

	seen := make(chan any, 1)
	id, err := srv.jobs.Start("probe", func(ctx context.Context, _ core.EventSink) (any, error) {
		seen <- ctx.Value(key)
		return nil, nil
	})
	require.NoError(t, err)
	j, ok := srv.jobs.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")
	assert.Equal(t, "present", <-seen, "jobs must derive from the context New was given, not a fresh root")

	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
	defer cancel()
	srv.jobs.shutdown(drainCtx)
}

// TestServer_ServeWaitsForRunningJobsWithinTheGrace pins the design's
// "running jobs get a bounded grace": a cancelled Serve does not return
// while a job is still applying, so `lmm serve` exiting cannot cut a deploy
// in half.
func TestServer_ServeWaitsForRunningJobsWithinTheGrace(t *testing.T) {
	srv, cancelServe, done := newDrainServer(t, 10*time.Second)

	release := make(chan struct{})
	id, err := srv.jobs.Start("deploy", func(context.Context, core.EventSink) (any, error) {
		<-release
		return &core.DeployResult{}, nil
	})
	require.NoError(t, err)

	cancelServe()
	requireNotYet(t, done, "Serve returning while a job is still running")

	close(release)
	waitFor(t, done, "Serve returning once the job finished")

	j, ok := srv.jobs.job(id)
	require.True(t, ok)
	assert.Equal(t, jobSucceeded, j.status().State, "the job ran to completion, it was not cancelled by shutdown")
}

// TestServer_ServeCancelsJobsOnceTheGraceExpires pins the bounded half: a
// job that will not finish is cancelled when the grace runs out, so the
// command still exits.
func TestServer_ServeCancelsJobsOnceTheGraceExpires(t *testing.T) {
	srv, cancelServe, done := newDrainServer(t, 50*time.Millisecond)

	id, err := srv.jobs.Start("deploy", func(ctx context.Context, _ core.EventSink) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	require.NoError(t, err)

	start := time.Now()
	cancelServe()
	waitFor(t, done, "Serve returning after the grace expired")
	assert.Less(t, time.Since(start), 5*time.Second, "the drain is bounded by ShutdownGrace, not open-ended")

	j, ok := srv.jobs.job(id)
	require.True(t, ok)
	got := j.status()
	assert.Equal(t, jobFailed, got.State)
	require.NotNil(t, got.Error)
	assert.Contains(t, got.Error.Error, context.Canceled.Error())
}

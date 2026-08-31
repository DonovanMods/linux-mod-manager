package serve

// Internal (package serve) tests for the job registry
// (docs/plans/2026-08-30-serve-impl.md Task 6, design §"Jobs and SSE").
// Everything here is concurrency: a job's context must outlive the request
// that started it (the Ruling-16 analogue), two jobs must queue behind
// core's single mutation slot, the event ring must replay exactly for a
// late subscriber and drop deterministically when it overflows, finished
// jobs must age out, and a drained registry must leave no goroutine behind.
// Run under -race.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitFor blocks until ch is closed/readable, failing the test if that
// takes longer than a generous timeout - the "this must happen" half of
// every concurrency assertion here (its "this must NOT happen yet" half is
// requireNotYet below).
func waitFor[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// requireNotYet asserts ch stays empty for a short window - the same
// bounded-negative pattern internal/core's own beginOp serialization test
// uses (core/ops_test.go).
func requireNotYet[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s happened while it should still have been blocked", what)
	case <-time.After(50 * time.Millisecond):
	}
}

// newTestRegistry builds a registry rooted at ctx with a small ring and
// retention so overflow and eviction are cheap to assert, and drains it at
// test end so no test can leak a job goroutine into the next one.
func newTestRegistry(t *testing.T, ctx context.Context, ring, retain int) *jobRegistry {
	t.Helper()
	r := newJobRegistry(ctx, slog.New(slog.DiscardHandler), ring, retain)
	t.Cleanup(func() {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		r.shutdown(drainCtx)
	})
	return r
}

// newJobsService builds a bare *core.Service over temp dirs - the internal
// (package serve) twin of testhelpers_test.go's newFixtureService, which
// lives in package serve_test and isn't visible here.
func newJobsService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// seedInstalledFixture registers game "g1" with a "default" profile holding
// one installed mod, so a job can run a real core mutation
// (SetModUpdatePolicy) and the test can assert the database actually moved.
func seedInstalledFixture(t *testing.T, svc *core.Service) *domain.Game {
	t.Helper()
	ctx := t.Context()
	game := &domain.Game{
		ID:          "g1",
		Name:        "Fixture Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
		SourceIDs:   map[string]string{blockingSourceID: ""},
	}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err := svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{
			ID: "m1", SourceID: blockingSourceID, Name: "Mod One", Version: "1.0", GameID: game.ID,
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	return game
}

const blockingSourceID = "blocking"

// blockingSource is a source.ModSource whose GetDownloadURL parks until the
// test releases it - the only injectable point inside a core mutation that
// holds beginOp for as long as a test wants (ApplyInstall ->
// downloadModToCache -> src.GetDownloadURL), and therefore how
// TestJobRegistry_SecondJobQueuesBehindCoreMutationSlot proves the second
// job really is waiting on the slot.
type blockingSource struct {
	mod     domain.Mod
	file    domain.DownloadableFile
	reached chan struct{} // closed once GetDownloadURL is entered
	release chan struct{} // closed by the test to let it return
}

func newBlockingSource() *blockingSource {
	return &blockingSource{
		mod: domain.Mod{
			ID: "m2", SourceID: blockingSourceID, Name: "Blocking Mod", Version: "1.0",
		},
		file: domain.DownloadableFile{
			ID: "f1", Name: "Blocking Mod", FileName: "blocking-1.0.zip",
			Version: "1.0", IsPrimary: true, Category: "MAIN",
		},
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingSource) ID() string      { return blockingSourceID }
func (s *blockingSource) Name() string    { return "Blocking Source" }
func (s *blockingSource) AuthURL() string { return "" }

func (s *blockingSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (s *blockingSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}

func (s *blockingSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	if modID != s.mod.ID {
		return nil, domain.ErrModNotFound
	}
	mod := s.mod
	return &mod, nil
}

func (s *blockingSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

func (s *blockingSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{s.file}, nil
}

// GetDownloadURL signals that the install has reached the download (so it
// is provably holding core's mutation slot), then blocks until the test
// releases it and fails the install - the install must NOT succeed, or it
// would move the installed-mod set and make the second job's plan stale
// instead of merely queued.
func (s *blockingSource) GetDownloadURL(ctx context.Context, _ *domain.Mod, _ string) (string, error) {
	close(s.reached)
	select {
	case <-s.release:
		return "", errors.New("blocking source: released")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *blockingSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

var _ source.ModSource = (*blockingSource)(nil)

func TestJobRegistry_RunsToCompletionAndStoresTheResult(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 4)

	want := &core.EnableResult{Changed: true}
	id := r.Start("enable", func(context.Context, core.EventSink) (any, error) {
		return want, nil
	})

	j, ok := r.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")

	got := j.status()
	assert.Equal(t, id, got.ID)
	assert.Equal(t, "enable", got.Kind)
	assert.Equal(t, jobSucceeded, got.State)
	assert.Same(t, want, got.Result)
	assert.Nil(t, got.Error)
	assert.False(t, got.StartedAt.IsZero())
	assert.False(t, got.EndedAt.IsZero())
}

// TestJobRegistry_FailedJobStoresTheErrorEnvelope pins the design's "failed
// (with the envelope)" state: the stored failure is the same
// {"error","details"} document /api/v1 answers with, typed details
// included, and the raw error stays reachable so Task 8's confirm page can
// branch on *core.ConflictError to offer an overwrite.
func TestJobRegistry_FailedJobStoresTheErrorEnvelope(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 4)

	conflictErr := &core.ConflictError{Conflicts: []core.Conflict{{
		RelativePath:    "Mods/a.pak",
		CurrentSourceID: "fake",
		CurrentModID:    "m9",
	}}}
	id := r.Start("install", func(context.Context, core.EventSink) (any, error) {
		return nil, conflictErr
	})

	j, ok := r.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")

	got := j.status()
	assert.Equal(t, jobFailed, got.State)
	assert.Nil(t, got.Result)
	require.NotNil(t, got.Error)
	assert.Equal(t, conflictErr.Error(), got.Error.Error)
	require.NotNil(t, got.Error.Details, "a Details() error must keep its typed details in the stored envelope")

	var buf bytes.Buffer
	require.NoError(t, core.EncodeJSON(&buf, got.Error))
	assert.Contains(t, buf.String(), `"conflicts"`)
	assert.Contains(t, buf.String(), "Mods/a.pak")

	var stored *core.ConflictError
	require.ErrorAs(t, j.failure(), &stored, "the raw error stays reachable for the overwrite flow")
	assert.Equal(t, conflictErr.Conflicts, stored.Conflicts)
}

// TestJobRegistry_JobOutlivesTheStartingRequestContext is the Ruling-16
// analogue the design calls for: "killing a job's request does not kill the
// Apply (jobs own their context)". The run function parks until the test
// has cancelled the request context, so the cancellation lands mid-Apply -
// and the database must still move.
func TestJobRegistry_JobOutlivesTheStartingRequestContext(t *testing.T) {
	svc := newJobsService(t)
	seedInstalledFixture(t, svc)
	r := newTestRegistry(t, t.Context(), 8, 4)

	reqCtx, cancelRequest := context.WithCancel(t.Context())
	started := make(chan struct{})
	proceed := make(chan struct{})

	id := r.Start("set-update-policy", func(ctx context.Context, _ core.EventSink) (any, error) {
		close(started)
		<-proceed // the request is cancelled while we sit here: mid-Apply
		return svc.SetModUpdatePolicy(ctx, blockingSourceID, "m1", "g1", "default", domain.UpdateAuto)
	})
	waitFor(t, started, "job start")
	cancelRequest()
	require.Error(t, reqCtx.Err(), "the starting request is cancelled while the Apply is in flight")
	close(proceed)

	j, ok := r.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")

	require.Equal(t, jobSucceeded, j.status().State, "a cancelled request must not fail the job it started")

	mod, err := svc.GetInstalledMod(t.Context(), blockingSourceID, "m1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, mod.UpdatePolicy, "the Apply must have committed despite the request cancellation")
}

// TestJobRegistry_JobContextDerivesFromTheServerRoot pins the other half of
// the contract: the job context carries the SERVER root context's values
// (it is derived from it, not conjured from context.Background), while the
// registry - not the root's own cancellation - decides when jobs are
// cancelled, so shutdown can give them a bounded grace first.
func TestJobRegistry_JobContextDerivesFromTheServerRoot(t *testing.T) {
	type ctxKey string
	const key ctxKey = "server-root-marker"

	rootCtx, cancelRoot := context.WithCancel(context.WithValue(t.Context(), key, "present"))
	defer cancelRoot()
	r := newTestRegistry(t, rootCtx, 8, 4)

	seen := make(chan any, 1)
	cancelled := make(chan struct{})
	id := r.Start("long", func(ctx context.Context, _ core.EventSink) (any, error) {
		seen <- ctx.Value(key)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})

	select {
	case v := <-seen:
		assert.Equal(t, "present", v, "the job context carries the server root's values, so it derives from it")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for job start")
	}

	cancelRoot()
	requireNotYet(t, cancelled, "job cancellation on server-root cancel")

	drainCtx, cancelDrain := context.WithCancel(context.WithoutCancel(rootCtx))
	cancelDrain() // grace already expired: shutdown must cancel the jobs
	r.shutdown(drainCtx)
	waitFor(t, cancelled, "job cancellation at shutdown")

	j, ok := r.job(id)
	require.True(t, ok)
	assert.Equal(t, jobFailed, j.status().State)
}

// TestJobRegistry_SecondJobQueuesBehindCoreMutationSlot proves the design's
// "beginOp already serializes mutations in-process, so concurrent Apply jobs
// queue": while job A holds the slot inside ApplyInstall, job B's own core
// mutation cannot start, and it completes only once A lets go.
func TestJobRegistry_SecondJobQueuesBehindCoreMutationSlot(t *testing.T) {
	svc := newJobsService(t)
	game := seedInstalledFixture(t, svc)
	src := newBlockingSource()
	svc.RegisterSource(src)
	r := newTestRegistry(t, t.Context(), 8, 4)

	plan, err := svc.PlanInstall(t.Context(), game, "default", blockingSourceID, "m2", false)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1, "the fixture install must resolve exactly one file to download")

	blocker := r.Start("install", func(ctx context.Context, sink core.EventSink) (any, error) {
		return svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, sink)
	})
	waitFor(t, src.reached, "the blocking install to reach the download")

	queued := r.Start("set-update-policy", func(ctx context.Context, _ core.EventSink) (any, error) {
		return svc.SetModUpdatePolicy(ctx, blockingSourceID, "m1", "g1", "default", domain.UpdateAuto)
	})
	queuedJob, ok := r.job(queued)
	require.True(t, ok)
	requireNotYet(t, queuedJob.done(), "the second job's mutation")

	close(src.release)

	blockerJob, ok := r.job(blocker)
	require.True(t, ok)
	waitFor(t, blockerJob.done(), "the blocking install to finish")
	waitFor(t, queuedJob.done(), "the queued job to finish once the slot is free")
	assert.Equal(t, jobSucceeded, queuedJob.status().State)
	assert.Equal(t, jobFailed, blockerJob.status().State, "the blocked install fails when the source is released")
}

func TestJobRegistry_LateSubscriberReplaysEveryBufferedEvent(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 4)

	emitted := make(chan struct{})
	proceed := make(chan struct{})
	id := r.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		for i := range 3 {
			sink(core.StepEvent{Scope: core.Scope{Op: core.OpDeploy, Index: i + 1}, Detail: "before"})
		}
		close(emitted)
		<-proceed
		sink(core.StepEvent{Scope: core.Scope{Op: core.OpDeploy, Index: 4}, Detail: "after"})
		return &core.DeployResult{}, nil
	})
	j, ok := r.job(id)
	require.True(t, ok)
	waitFor(t, emitted, "the first three events")

	replay, live, cancel := j.subscribe(8)
	defer cancel()
	require.Len(t, replay, 3, "a late subscriber replays the whole buffer first")
	for i, e := range replay {
		step, isStep := e.(core.StepEvent)
		require.True(t, isStep)
		assert.Equal(t, i+1, step.Index)
		assert.Equal(t, "before", step.Detail)
	}

	close(proceed)
	select {
	case e := <-live:
		step, isStep := e.(core.StepEvent)
		require.True(t, isStep)
		assert.Equal(t, 4, step.Index, "events after subscribe arrive live, exactly once")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the live event")
	}

	waitFor(t, j.done(), "job completion")
	_, stillOpen := <-live
	assert.False(t, stillOpen, "a finished job closes its subscribers")
}

// TestJobRegistry_RingBufferDropsOldestOnOverflow pins the DEFINED overflow
// behaviour: the ring keeps the newest ringSize events, the oldest are
// dropped, and the count of what was dropped is reported so a late
// subscriber can say so rather than silently showing a partial history.
func TestJobRegistry_RingBufferDropsOldestOnOverflow(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 4, 4)

	id := r.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		for i := range 10 {
			sink(core.StepEvent{Scope: core.Scope{Op: core.OpDeploy, Index: i + 1}})
		}
		return nil, nil
	})
	j, ok := r.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")

	replay, live, cancel := j.subscribe(4)
	defer cancel()
	require.Len(t, replay, 4)
	for i, e := range replay {
		assert.Equal(t, 7+i, e.(core.StepEvent).Index, "the ring retains the NEWEST events, oldest first")
	}
	_, stillOpen := <-live
	assert.False(t, stillOpen, "subscribing to a finished job yields a closed live channel")

	got := j.status()
	assert.Equal(t, 10, got.EventCount)
	assert.Equal(t, 6, got.DroppedEvents)
}

// TestJobRegistry_LaggingSubscriberIsDisconnected pins the other overflow
// rule: a subscriber that stops reading is dropped rather than allowed to
// stall the Apply. Its channel closes while the job is still running, which
// is the signal to re-subscribe (the ring replay closes the gap).
func TestJobRegistry_LaggingSubscriberIsDisconnected(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 64, 4)

	startEmit := make(chan struct{})
	emitted := make(chan struct{})
	holdOpen := make(chan struct{})
	id := r.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		<-startEmit
		for i := range 8 {
			sink(core.StepEvent{Scope: core.Scope{Op: core.OpDeploy, Index: i + 1}})
		}
		close(emitted)
		<-holdOpen
		return nil, nil
	})
	j, ok := r.job(id)
	require.True(t, ok)

	_, live, cancel := j.subscribe(2)
	defer cancel()
	close(startEmit)
	waitFor(t, emitted, "the emitting job")

	drained := 0
	for range live {
		drained++
	}
	assert.LessOrEqual(t, drained, 2, "a lagging subscriber only ever sees what fit in its buffer")
	assert.Equal(t, jobRunning, j.status().State, "the subscriber was dropped, not the job")

	close(holdOpen)
	waitFor(t, j.done(), "job completion")
}

func TestJobRegistry_EvictsAllButTheMostRecentJobs(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 3)

	var ids []jobID
	for range 5 {
		id := r.Start("enable", func(context.Context, core.EventSink) (any, error) { return nil, nil })
		j, ok := r.job(id)
		require.True(t, ok)
		waitFor(t, j.done(), "job completion")
		ids = append(ids, id)
	}

	for _, id := range ids[:2] {
		_, ok := r.job(id)
		assert.False(t, ok, "an aged-out job is forgotten: the DB, not the registry, is the record")
	}
	for _, id := range ids[2:] {
		_, ok := r.job(id)
		assert.True(t, ok, "the most recent jobs are retained")
	}
}

func TestJobRegistry_NeverEvictsARunningJob(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 2)

	release := make(chan struct{})
	long := r.Start("deploy", func(context.Context, core.EventSink) (any, error) {
		<-release
		return nil, nil
	})

	for range 4 {
		id := r.Start("enable", func(context.Context, core.EventSink) (any, error) { return nil, nil })
		j, ok := r.job(id)
		require.True(t, ok)
		waitFor(t, j.done(), "job completion")
	}

	j, ok := r.job(long)
	require.True(t, ok, "a running job is never evicted, however many jobs follow it")
	close(release)
	waitFor(t, j.done(), "the long job")
}

// TestJobRegistry_ShutdownWaitsForRunningJobsAndLeavesNoGoroutines is the
// leak ratchet: shutdown returns only once every job goroutine is gone.
func TestJobRegistry_ShutdownWaitsForRunningJobsAndLeavesNoGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()
	r := newJobRegistry(t.Context(), slog.New(slog.DiscardHandler), 8, 8)

	release := make(chan struct{})
	finished := make(chan struct{}, 3)
	var ids []jobID
	for range 3 {
		ids = append(ids, r.Start("deploy", func(context.Context, core.EventSink) (any, error) {
			<-release
			finished <- struct{}{}
			return nil, nil
		}))
	}

	close(release)
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
	defer cancel()
	r.shutdown(drainCtx)

	assert.Len(t, finished, 3, "shutdown waits for running jobs rather than cutting them off")
	for _, id := range ids {
		j, ok := r.job(id)
		require.True(t, ok)
		assert.Equal(t, jobSucceeded, j.status().State)
	}
	requireNoExtraGoroutines(t, baseline)
}

// requireNoExtraGoroutines fails unless the goroutine count settles back to
// baseline. It polls rather than asserting once because a goroutine that has
// already run its deferred wg.Done can still be a few instructions from
// actually exiting - the same settling window goroutine-leak detectors use.
// Written as a loop rather than assert.Eventually deliberately: Eventually
// evaluates its condition on goroutines of its own, which would count
// towards the very number under test.
func requireNoExtraGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := runtime.NumGoroutine()
		if got <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the registry leaked a goroutine: baseline %d, still %d after shutdown", baseline, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestJobRegistry_ShutdownCancelsJobsOnceTheGraceExpires pins the bounded
// half of the drain: a job that will not finish is cancelled when the grace
// runs out, so `lmm serve` can still exit.
func TestJobRegistry_ShutdownCancelsJobsOnceTheGraceExpires(t *testing.T) {
	r := newJobRegistry(t.Context(), slog.New(slog.DiscardHandler), 8, 8)

	id := r.Start("deploy", func(ctx context.Context, _ core.EventSink) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	drainCtx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	cancel() // the grace has already expired
	r.shutdown(drainCtx)

	j, ok := r.job(id)
	require.True(t, ok)
	got := j.status()
	assert.Equal(t, jobFailed, got.State)
	require.NotNil(t, got.Error)
	assert.Contains(t, got.Error.Error, context.Canceled.Error())
}

// TestJobRegistry_PanickingRunFailsTheJob: an Apply that panics is a bug,
// but it must fail its own job rather than take the whole server down with
// it (an unrecovered panic in a non-handler goroutine kills the process).
func TestJobRegistry_PanickingRunFailsTheJob(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 4)

	id := r.Start("deploy", func(context.Context, core.EventSink) (any, error) {
		panic("boom")
	})
	j, ok := r.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")

	got := j.status()
	assert.Equal(t, jobFailed, got.State)
	require.NotNil(t, got.Error)
	assert.Contains(t, got.Error.Error, "boom")
}

func TestJobRegistry_UnknownJobIsNotFound(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 4)
	_, ok := r.job("nope")
	assert.False(t, ok)
}

// TestJobRegistry_ConcurrentSubscribersAndEmits is the -race sweep: many
// subscribers joining and leaving while a job emits.
func TestJobRegistry_ConcurrentSubscribersAndEmits(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 32, 8)

	go1 := make(chan struct{})
	id := r.Start("deploy", func(_ context.Context, sink core.EventSink) (any, error) {
		close(go1)
		for i := range 200 {
			sink(core.StepEvent{Scope: core.Scope{Op: core.OpDeploy, Index: i + 1}})
		}
		return nil, nil
	})
	j, ok := r.job(id)
	require.True(t, ok)
	waitFor(t, go1, "job start")

	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			replay, live, cancel := j.subscribe(4)
			_ = replay
			for range live {
			}
			cancel()
			_ = j.status()
		}()
	}
	for range 8 {
		waitFor(t, done, "a subscriber goroutine")
	}
	waitFor(t, j.done(), "job completion")
}

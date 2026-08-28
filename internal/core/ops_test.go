package core

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newOpsService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// TestBeginOp_SecondMutationBlocksUntilRelease pins service-wide
// serialization of mutations.
func TestBeginOp_SecondMutationBlocksUntilRelease(t *testing.T) {
	svc := newOpsService(t)
	ctx := context.Background()

	release, err := svc.beginOp(ctx)
	require.NoError(t, err)

	acquired := make(chan struct{})
	go func() {
		r2, err := svc.beginOp(ctx)
		require.NoError(t, err)
		close(acquired)
		r2()
	}()

	select {
	case <-acquired:
		t.Fatal("second mutation acquired while the first was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second mutation never acquired after release")
	}
}

// TestBeginOp_WaiterIsCancellable pins that a caller waiting for the slot
// returns ctx.Err() without ever acquiring.
func TestBeginOp_WaiterIsCancellable(t *testing.T) {
	svc := newOpsService(t)
	release, err := svc.beginOp(context.Background())
	require.NoError(t, err)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = svc.beginOp(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, svc.opSem, 1, "slot must still be held by the first caller")
}

// TestBeginOp_BlockedWaiterIsCancellable pins the one assertion spec §3
// makes about beginOp that no test covered before this fix: cancelling a
// context while a caller is genuinely blocked on <-ctx.Done() (not
// pre-cancelled - TestBeginOp_PreCancelledCtxNeverAcquires covers that path)
// returns ctx.Err() with a nil release, and the slot stays with its
// original holder (#279 Unit B final review finding I1; a coverage run
// showed the <-ctx.Done() arm at ops.go:20-21 was never executed).
func TestBeginOp_BlockedWaiterIsCancellable(t *testing.T) {
	svc := newOpsService(t)
	release, err := svc.beginOp(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		release func()
		err     error
	}
	results := make(chan result, 1)
	go func() {
		r, err := svc.beginOp(ctx)
		results <- result{r, err}
	}()

	// Let the spawned goroutine actually reach the blocking select before
	// cancelling: the slot is still held by the first caller (len == 1),
	// plus a settle window so the scheduler has run it.
	require.Len(t, svc.opSem, 1)
	time.Sleep(50 * time.Millisecond)

	cancel()

	select {
	case r := <-results:
		require.ErrorIs(t, r.err, context.Canceled)
		require.Nil(t, r.release)
	case <-time.After(time.Second):
		t.Fatal("blocked waiter never returned after cancel")
	}
	require.Len(t, svc.opSem, 1, "slot must still be held by the first caller")

	release()
	require.Len(t, svc.opSem, 0, "slot must free once the first holder releases")
}

// TestBeginOp_PreCancelledCtxNeverAcquires pins that beginOp is
// deterministic when ctx is already done before the select ever runs: with
// the slot free, a done ctx must always lose rather than racing the free
// slot 50/50 (v2 Phase 1 Task 6 fix round 1, #279 review finding I1).
func TestBeginOp_PreCancelledCtxNeverAcquires(t *testing.T) {
	svc := newOpsService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for i := 0; i < 200; i++ {
		release, err := svc.beginOp(ctx)
		if err == nil {
			release()
			t.Fatalf("iteration %d: beginOp acquired the slot with an already-cancelled ctx and a free slot; want ctx.Err() every time", i)
		}
		require.ErrorIs(t, err, context.Canceled, "iteration %d", i)
		require.Len(t, svc.opSem, 0, "iteration %d: slot must stay free when the caller never acquires", i)
	}
}

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

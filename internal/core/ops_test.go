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

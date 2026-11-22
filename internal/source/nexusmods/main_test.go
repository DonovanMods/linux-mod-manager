package nexusmods

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestMain makes every client this package's tests build retry at once
// instead of waiting out its backoff (#671): a test that answers a 5xx
// would otherwise spend seconds per call. The attempts, the holds and the
// errors are untouched; throttle_test.go puts the retries on a fake clock
// to assert the waits themselves.
func TestMain(m *testing.M) {
	retrySleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	os.Exit(m.Run())
}

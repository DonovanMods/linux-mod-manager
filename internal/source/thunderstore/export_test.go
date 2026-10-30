package thunderstore

import (
	"context"
	"testing"
	"time"
)

// SkipRetryBackoffForTest makes every Source built for the rest of the test
// retry at once instead of waiting out its backoff, and restores the real
// sleep when the test ends (#576). The attempts, the holds and the errors
// they produce are untouched; the internal transport tests assert the
// backoff policy itself. A cancelled ctx still ends the retries.
func SkipRetryBackoffForTest(t *testing.T) {
	previous := retrySleep
	retrySleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	t.Cleanup(func() { retrySleep = previous })
}

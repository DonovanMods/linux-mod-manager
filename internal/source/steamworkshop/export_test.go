package steamworkshop

import (
	"context"
	"testing"
	"time"
)

// FetchDetailsForTest exposes the batching/caching client to the package's
// external tests, which need to drive it directly to assert the 100-ids-per-
// request contract and the cache's refresh bypass without going through a
// flow that only ever asks for one item.
func (s *Source) FetchDetailsForTest(ctx context.Context, ids []string, refresh bool) (int, error) {
	got, err := s.client.fetchDetails(ctx, ids, refresh)
	return len(got), err
}

// SetHeartbeatForTest shortens the steamcmd progress heartbeat so a test
// can observe it without waiting the production interval, and restores it
// when the test ends. The heartbeat is the ONLY progress a silent
// multi-gigabyte download produces, so it needs a real test rather than a
// comment.
func SetHeartbeatForTest(t *testing.T, d time.Duration) {
	previous := steamcmdHeartbeat
	steamcmdHeartbeat = d
	t.Cleanup(func() { steamcmdHeartbeat = previous })
}

// SetWaitDelayForTest shortens the bound on cmd.Wait's I/O drain so a test
// can prove a leftover grandchild cannot hold the fetch open, without
// waiting the production delay.
func SetWaitDelayForTest(t *testing.T, d time.Duration) {
	previous := steamcmdWaitDelay
	steamcmdWaitDelay = d
	t.Cleanup(func() { steamcmdWaitDelay = previous })
}

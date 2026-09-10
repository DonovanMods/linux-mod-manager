package steamworkshop

import "context"

// FetchDetailsForTest exposes the batching/caching client to the package's
// external tests, which need to drive it directly to assert the 100-ids-per-
// request contract and the cache's refresh bypass without going through a
// flow that only ever asks for one item.
func (s *Source) FetchDetailsForTest(ctx context.Context, ids []string, refresh bool) (int, error) {
	got, err := s.client.fetchDetails(ctx, ids, refresh)
	return len(got), err
}

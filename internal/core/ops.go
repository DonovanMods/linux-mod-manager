package core

import "context"

// beginOp acquires the Service's single mutation slot, waiting until it is
// free or ctx is done. It returns the release func on success.
//
// Concurrency contract (see the Service doc comment): query methods may
// run concurrently with each other and with at most one in-flight
// mutation; mutating operations are serialized service-wide. Exported
// mutating methods acquire the slot; their unexported implementations do
// not, so flows can compose them without re-entering the semaphore.
func (s *Service) beginOp(ctx context.Context) (release func(), err error) {
	select {
	case s.opSem <- struct{}{}:
		return func() { <-s.opSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

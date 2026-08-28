package core

import (
	"context"
	"sync"
)

// beginOp acquires the Service's single mutation slot, waiting until it is
// free or ctx is done. It returns the release func on success; release is
// idempotent, so a second call is a no-op rather than consuming the next
// caller's slot.
//
// Concurrency contract (see the Service doc comment): query methods may
// run concurrently with each other and with at most one in-flight
// mutation; mutating operations are serialized service-wide. Exported
// mutating methods acquire the slot; their unexported implementations do
// not, so flows can compose them without re-entering the semaphore.
//
// beginOp panics if s.opSem is nil - a Service built as a struct literal
// (common in internal tests that only need a few fields) rather than via
// NewService - so the failure is an immediate, clear panic instead of a
// hang until the test's 10-minute timeout.
func (s *Service) beginOp(ctx context.Context) (release func(), err error) {
	if s.opSem == nil {
		panic("core: beginOp called on a Service with a nil opSem; construct it via NewService, not a struct literal")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case s.opSem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-s.opSem }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

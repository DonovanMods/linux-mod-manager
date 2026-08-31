package serve

// Internal (package serve) test for errorDetails' ErrStalePlan guard
// (docs/plans/2026-08-30-serve-impl.md Task 5 gate review Minor 4).

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
)

// detailsAndStaleErr wraps core.ErrStalePlan AND implements Details() any -
// contrived, since no production error does both today, but it proves
// errorDetails' ErrStalePlan guard takes priority over the Details()
// extension point, the same order cmd/lmm's own errorDetails
// (jsonout.go) checks them in.
type detailsAndStaleErr struct{}

func (detailsAndStaleErr) Error() string { return "stale" }
func (detailsAndStaleErr) Unwrap() error { return core.ErrStalePlan }
func (detailsAndStaleErr) Details() any  { return map[string]string{"leaked": "true"} }

// TestErrorDetails_ErrStalePlan is the task-5 gate review's Minor 4 fix:
// serve's errorDetails must drop ErrStalePlan's Details() the same way
// cmd/lmm's own errorDetails does. No Details() implementer wraps
// ErrStalePlan today (behaviour is unchanged for anything currently
// reachable), but Unit 4 routes job failures - including a stale plan -
// through this same writer, exactly the case cmd/lmm guarded against.
func TestErrorDetails_ErrStalePlan(t *testing.T) {
	assert.Nil(t, errorDetails(detailsAndStaleErr{}), "ErrStalePlan must never leak a Details() payload into the /api/v1 envelope")
}

package serve

// Internal (package serve) tests for the server-side plan store
// (docs/plans/2026-08-30-serve-impl.md Task 6): single-use Take under
// concurrent racers, TTL expiry on an injectable clock, and - the property
// the whole Plan/Apply contract rests on - Take handing back the STORED
// object itself, so Apply receives the plan's unexported `json:"-"`
// freshness snapshot rather than a re-marshalled wire copy that has lost it.

import (
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a hand-advanced time source for the TTL tests, so expiry is
// asserted deterministically instead of by sleeping (GO.md: "No sleeps;
// prefer fake clocks or synchronization").
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// newFakeClock starts a clock at a fixed, arbitrary instant.
func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}
}

// Now is the func() time.Time seam newPlanStore takes.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// advance moves the clock forward by d.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestPlanStore_TakeReturnsTheStoredObjectItself(t *testing.T) {
	clk := newFakeClock()
	store := newPlanStore(defaultPlanTTL, 0, clk.Now)

	// A real core plan: what matters is that the value Apply receives is
	// this exact pointer, since its freshness snapshot is unexported and
	// json:"-" - a value that survived a marshal/unmarshal round trip
	// would silently apply against an empty precondition.
	plan := &core.InstallPlan{GameID: "g1", Profile: "default", SourceID: "fake"}

	id := store.Put(plan, "install")
	require.NotEmpty(t, id)

	stored, err := store.Take(id)
	require.NoError(t, err)
	assert.Equal(t, "install", stored.Kind)
	assert.Same(t, plan, stored.Plan, "Take must hand back the stored object, never a copy")
}

func TestPlanStore_TakeIsSingleUseUnderConcurrentRacers(t *testing.T) {
	store := newPlanStore(defaultPlanTTL, 0, newFakeClock().Now)
	plan := &core.InstallPlan{GameID: "g1"}
	id := store.Put(plan, "install")

	const racers = 16
	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		mu      sync.Mutex
		wins    int
		refused int
	)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stored, err := store.Take(id)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
				assert.Same(t, plan, stored.Plan)
				return
			}
			assert.ErrorIs(t, err, errPlanUnavailable)
			refused++
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, 1, wins, "exactly one racer may take a single-use plan")
	assert.Equal(t, racers-1, refused)
	assert.Zero(t, store.len(), "a taken plan is gone from the store")
}

func TestPlanStore_ExpiredPlanIsUnavailable(t *testing.T) {
	clk := newFakeClock()
	store := newPlanStore(10*time.Minute, 0, clk.Now)
	id := store.Put(&core.InstallPlan{GameID: "g1"}, "install")

	clk.advance(10*time.Minute - time.Nanosecond)
	_, err := store.Take(id)
	require.NoError(t, err, "a plan inside its TTL is still takeable")

	id = store.Put(&core.InstallPlan{GameID: "g1"}, "install")
	clk.advance(10 * time.Minute)
	_, err = store.Take(id)
	assert.ErrorIs(t, err, errPlanUnavailable)
	assert.Zero(t, store.len(), "an expired plan Take found is dropped, not left to accumulate")
}

func TestPlanStore_UnknownIDIsUnavailable(t *testing.T) {
	store := newPlanStore(defaultPlanTTL, 0, newFakeClock().Now)
	_, err := store.Take("nope")
	assert.ErrorIs(t, err, errPlanUnavailable)
}

func TestPlanStore_PutSweepsExpiredPlans(t *testing.T) {
	clk := newFakeClock()
	store := newPlanStore(10*time.Minute, 0, clk.Now)

	store.Put(&core.InstallPlan{GameID: "g1"}, "install")
	store.Put(&core.InstallPlan{GameID: "g2"}, "install")
	require.Equal(t, 2, store.len())

	clk.advance(11 * time.Minute)
	fresh := store.Put(&core.InstallPlan{GameID: "g3"}, "uninstall")

	assert.Equal(t, 1, store.len(), "Put sweeps every expired plan, so an abandoned confirm page can't grow the store without bound")
	stored, err := store.Take(fresh)
	require.NoError(t, err)
	assert.Equal(t, "uninstall", stored.Kind)
}

// TestPlanStore_PutEvictsOldestPlanOnceOverCap pins task-6-review.md Minor 4:
// a cap bounds the store even when nothing has expired yet, so a burst of
// confirm pages within the TTL window can't grow it without bound. The TTL
// here is long enough that eviction, not sweeping, is what's under test.
func TestPlanStore_PutEvictsOldestPlanOnceOverCap(t *testing.T) {
	clk := newFakeClock()
	store := newPlanStore(defaultPlanTTL, 3, clk.Now)

	oldest := store.Put(&core.InstallPlan{GameID: "g1"}, "install")
	clk.advance(time.Second)
	middle := store.Put(&core.InstallPlan{GameID: "g2"}, "install")
	clk.advance(time.Second)
	newest := store.Put(&core.InstallPlan{GameID: "g3"}, "install")
	require.Equal(t, 3, store.len(), "the cap is not exceeded yet")

	clk.advance(time.Second)
	fourth := store.Put(&core.InstallPlan{GameID: "g4"}, "install")

	assert.Equal(t, 3, store.len(), "Put evicts to make room rather than growing past cap")
	_, err := store.Take(oldest)
	assert.ErrorIs(t, err, errPlanUnavailable, "the oldest entry is the one evicted")

	for _, id := range []planID{middle, newest, fourth} {
		_, err := store.Take(id)
		assert.NoError(t, err, "every entry newer than the evicted one survives")
	}
}

func TestPlanStore_IDsAreUnique(t *testing.T) {
	store := newPlanStore(defaultPlanTTL, 0, newFakeClock().Now)
	seen := map[planID]bool{}
	for range 64 {
		id := store.Put(&core.InstallPlan{}, "install")
		require.False(t, seen[id], "plan IDs must be unique")
		seen[id] = true
	}
}

// TestPlanStore_ConcurrentPutAndTake is the -race sweep over the store's
// whole surface: concurrent Puts, Takes of live ids, and Takes of ids that
// were already taken.
func TestPlanStore_ConcurrentPutAndTake(t *testing.T) {
	store := newPlanStore(defaultPlanTTL, 0, newFakeClock().Now)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 32 {
				id := store.Put(&core.InstallPlan{}, "install")
				_, _ = store.Take(id)
				_, _ = store.Take(id)
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, store.len())
}

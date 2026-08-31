// plans.go implements Task 6's server-side plan store
// (docs/plans/2026-08-30-serve-design.md §HTTP surface: "Plans carry
// unexported json:"-" freshness snapshots, so the stored server-side object -
// not the wire copy - is what Apply receives. The store is in-memory,
// single-use, ~10-minute TTL").
//
// Why a store at all: every core Plan embeds an unexported installedSnapshot
// tagged json:"-" (internal/core/plan.go), the precondition its Apply
// re-derives and compares under checkPlanFresh. A plan that reached the
// browser as JSON and came back would arrive with that field zeroed, so its
// Apply would compare the live world against an EMPTY snapshot and refuse
// every non-empty profile as stale. The confirm page therefore round-trips
// only an opaque plan_id, and Take hands the Apply the very object PlanX
// returned.
package serve

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// defaultPlanTTL is how long a stored plan stays takeable - long enough for
// a user to read a confirm page and decide, short enough that an abandoned
// tab's plan (and the mod/file data it pins) doesn't linger for the life of
// the process.
const defaultPlanTTL = 10 * time.Minute

// defaultPlanStoreCap bounds how many live plans the store holds at once.
// The TTL alone only reclaims a plan once it has sat unswept for up to ten
// minutes; a user (or a script hitting the plan endpoints) who opens confirm
// pages faster than that can grow the store without bound in the meantime.
// 128 is generous for this tool's single-user, local use and cheap to hold
// entirely in memory (task-6-review.md Minor 4).
const defaultPlanStoreCap = 128

// errPlanUnavailable is the single sentinel every failed Take reports: the
// id was never issued, its plan was already applied (Take is single-use), or
// its TTL elapsed. The three are deliberately indistinguishable - a used
// plan is deleted, so a "used" answer could only ever be a guess, and every
// caller answers all three the same way anyway (re-plan and show a fresh
// confirm page, docs/plans/2026-08-30-serve-impl.md Task 8). Callers branch
// on it with errors.Is.
var errPlanUnavailable = errors.New("plan is no longer available: it expired or was already applied")

// planID is the opaque handle a confirm page round-trips instead of the
// plan itself. Random rather than sequential so one browser tab cannot
// guess (and consume) another's pending plan.
type planID string

// storedPlan is one live entry: the exact plan object a core PlanX method
// returned, plus the kind that tells the job handler which Apply to run it
// through ("install", "uninstall", "deploy", ...).
type storedPlan struct {
	// ID is the handle Put issued for this plan.
	ID planID
	// Kind names the mutation the plan belongs to, as passed to Put.
	Kind string
	// Plan is the object Put was given - pointer identity preserved, so
	// its unexported json:"-" freshness snapshot survives to Apply.
	Plan any
	// StoredAt is when Put accepted it, by the store's clock.
	StoredAt time.Time
}

// planStore is the in-memory, single-use, TTL'd plan store. Safe for
// concurrent use: every method takes mu, and Take's lookup-and-delete is one
// critical section, so exactly one of any number of racing callers can ever
// receive a given plan.
type planStore struct {
	ttl time.Duration
	// cap is the most live entries the store holds at once; Put evicts the
	// oldest surviving entries (by StoredAt) once sweeping still leaves it
	// over cap. A non-positive cap passed to newPlanStore falls back to
	// defaultPlanStoreCap.
	cap int
	// now is the clock seam - time.Now in production, a hand-advanced fake
	// in the TTL tests.
	now func() time.Time

	mu    sync.Mutex
	plans map[planID]*storedPlan
}

// newPlanStore builds an empty store whose entries expire ttl after they
// were Put, measured by now, and holds at most cap of them at once (a
// non-positive cap takes defaultPlanStoreCap).
func newPlanStore(ttl time.Duration, cap int, now func() time.Time) *planStore {
	if cap < 1 {
		cap = defaultPlanStoreCap
	}
	return &planStore{ttl: ttl, cap: cap, now: now, plans: map[planID]*storedPlan{}}
}

// Put stores plan under kind and returns the id a later Take redeems it
// with. It also sweeps every already-expired entry, which is what keeps the
// store bounded without a background goroutine: entries only ever arrive
// through Put, so sweeping here means an abandoned confirm page's plan is
// reclaimed by the next mutation anyone starts. If the store is still at or
// over cap after sweeping, Put evicts the oldest surviving entries until it
// is not - the TTL alone only reclaims a plan once it has sat unswept for up
// to ten minutes, and this bounds the store against churn faster than that.
func (s *planStore) Put(plan any, kind string) planID {
	now := s.now()
	entry := &storedPlan{ID: newPlanID(), Kind: kind, Plan: plan, StoredAt: now}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	s.evictOldestLocked()
	s.plans[entry.ID] = entry
	return entry.ID
}

// evictOldestLocked drops the oldest-stored entries, by StoredAt, until the
// store holds fewer than cap - leaving room for the one Put is about to
// insert. The caller must hold mu.
func (s *planStore) evictOldestLocked() {
	for len(s.plans) >= s.cap {
		var oldestID planID
		var oldest time.Time
		first := true
		for id, entry := range s.plans {
			if first || entry.StoredAt.Before(oldest) {
				oldestID, oldest = id, entry.StoredAt
				first = false
			}
		}
		delete(s.plans, oldestID)
	}
}

// Take removes and returns the plan stored under id. It is single-use: the
// entry is deleted inside the same critical section as the lookup, so a
// double-submitted confirm form (or two racing tabs) can never apply the
// same plan twice. An unknown, already-taken, or expired id reports
// errPlanUnavailable.
func (s *planStore) Take(id planID) (*storedPlan, error) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.plans[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errPlanUnavailable, id)
	}
	delete(s.plans, id)
	if s.expired(entry, now) {
		return nil, fmt.Errorf("%w: %s", errPlanUnavailable, id)
	}
	return entry, nil
}

// Kind reports the kind stored under id without consuming it - the peek
// the job endpoint needs to find the right planKind (and therefore the
// right options decoder) BEFORE it takes the plan. Without it, a request
// carrying options the kind refuses would have to burn its single-use plan
// just to discover that, forcing a re-plan for what is purely a bad
// request. An expired entry answers false, exactly as Take would; the
// entry is left for the next Put's sweep rather than deleted here, so a
// peek never has a side effect.
func (s *planStore) Kind(id planID) (string, bool) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.plans[id]
	if !ok || s.expired(entry, now) {
		return "", false
	}
	return entry.Kind, true
}

// len reports how many live entries the store holds. Test-facing: nothing
// in the request path needs it.
func (s *planStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.plans)
}

// expired reports whether entry's TTL has elapsed by now. Exactly ttl old
// counts as expired, so a plan is takeable for the half-open window
// [StoredAt, StoredAt+ttl).
func (s *planStore) expired(entry *storedPlan, now time.Time) bool {
	return !now.Before(entry.StoredAt.Add(s.ttl))
}

// sweepLocked drops every expired entry. The caller must hold mu.
func (s *planStore) sweepLocked(now time.Time) {
	for id, entry := range s.plans {
		if s.expired(entry, now) {
			delete(s.plans, id)
		}
	}
}

// newPlanID returns a fresh unguessable id from crypto/rand (GO.md:
// security-sensitive randomness never comes from math/rand - an id another
// tab could guess is an id it could consume).
func newPlanID() planID {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// The OS entropy source being broken is not a recoverable
		// request-time condition; fail the same way newCSRFGuard does.
		panic(fmt.Errorf("serve: generating plan id: %w", err))
	}
	return planID(hex.EncodeToString(b))
}

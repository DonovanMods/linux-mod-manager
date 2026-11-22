// Package httpclient: this file is the in-memory HoldKeeper (#671) - a
// RetryTransport's holds for a host whose holds need not outlive the
// process: Steam Workshop's circuit breaker, and NexusMods' throttle and
// quota holds. Thunderstore keeps its own on disk (thunderstore/hold.go),
// with the same rules.
package httpclient

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// MemoryHolds is a HoldKeeper for one whole host, kept for the life of the
// process. It is safe for concurrent use.
//
// A hold has one of two reasons. The host NAMED a wait (a Retry-After past
// what a request sits through, a quota used up), which stands until it
// expires - an answer to some other request in the meantime does not lift
// it. Or lmm tripped its own breaker after threshold failed requests in a
// row, which the next answer lifts.
type MemoryHolds struct {
	service   string
	now       func() time.Time
	threshold int
	cooldown  time.Duration
	maxHold   time.Duration

	mu          sync.Mutex
	until       time.Time
	reason      string
	named       bool
	failures    int
	lastFailure time.Time
}

// NewMemoryHolds returns an empty keeper for service. threshold failed
// requests in a row trip a breaker that holds for cooldown, and a streak
// lasts cooldown without another failure; no named hold ends further than
// maxHold ahead.
func NewMemoryHolds(service string, now func() time.Time, threshold int, cooldown, maxHold time.Duration) *MemoryHolds {
	if now == nil {
		now = time.Now
	}
	return &MemoryHolds{service: service, now: now, threshold: threshold, cooldown: cooldown, maxHold: maxHold}
}

func (h *MemoryHolds) activeAt(now time.Time) bool {
	return !h.until.IsZero() && now.Before(h.until)
}

// Active implements HoldKeeper.
func (h *MemoryHolds) Active(*http.Request) (source.Hold, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.activeAt(h.now()) {
		return source.Hold{}, false
	}
	return source.Hold{Source: h.service, Until: h.until, Reason: h.reason}, true
}

// Named implements HoldKeeper.
func (h *MemoryHolds) Named(_ *http.Request, until time.Time, why string) source.Hold {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if h.maxHold > 0 && until.After(now.Add(h.maxHold)) {
		until = now.Add(h.maxHold)
	}
	until = ceilSecond(until)
	if !h.activeAt(now) || until.After(h.until) {
		h.until, h.reason, h.named = until, why, true
	}
	return source.Hold{Source: h.service, Until: h.until, Reason: h.reason}
}

// Failed implements HoldKeeper.
func (h *MemoryHolds) Failed(_ *http.Request, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if !h.until.IsZero() && !h.activeAt(now) {
		// An expired hold is over, streak and all: the request that failed
		// was the probe it allowed, not the next strike of the outage that
		// set it.
		h.until, h.reason, h.named, h.failures = time.Time{}, "", false, 0
	}
	if h.failures > 0 && now.Sub(h.lastFailure) > h.cooldown {
		h.failures = 0
	}
	h.failures++
	h.lastFailure = now
	if h.threshold > 0 && h.failures >= h.threshold && !h.activeAt(now) {
		h.until = ceilSecond(now.Add(h.cooldown))
		h.reason = fmt.Sprintf("suspended after %d failed requests in a row; the last: %v", h.failures, err)
		h.named = false
	}
}

// Succeeded implements HoldKeeper.
func (h *MemoryHolds) Succeeded(*http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures, h.lastFailure = 0, time.Time{}
	if !h.named || !h.activeAt(h.now()) {
		h.until, h.reason, h.named = time.Time{}, "", false
	}
}

// Holds reports the hold in force, if any - what a source's
// source.HoldReporter answers without asking the host anything.
func (h *MemoryHolds) Holds() []source.Hold {
	if held, ok := h.Active(nil); ok {
		return []source.Hold{held}
	}
	return nil
}

// ceilSecond rounds t UP to a whole second, so every rendering of a hold -
// a notice's clock time, a document's retry time - names the same moment,
// and none names one before lmm will actually ask.
func ceilSecond(t time.Time) time.Time {
	if t.Truncate(time.Second).Equal(t) {
		return t
	}
	return t.Truncate(time.Second).Add(time.Second)
}

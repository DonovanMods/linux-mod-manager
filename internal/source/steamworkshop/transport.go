// Package steamworkshop: this file is the backoff half of the spike's
// "caching and backoff are mandatory, not optional" finding - retries with
// jitter for a throttled or failing API, and a process-lifetime circuit
// breaker so a sustained outage stops costing a request per item.
//
// The transport itself is the shared one (httpclient.RetryTransport, #671
// lifted it from here and from Thunderstore): it lives in a RoundTripper
// rather than around each call because only that layer sees the
// Retry-After header, and only that layer can rewind a form POST's body
// (http.Request.GetBody) to send it again. What is Steam's own is the
// numbers below and the wording: every request it gives up on is
// ErrMetadataUnavailable.
package steamworkshop

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

// serviceName is how a notice or an error names the host being waited on.
const serviceName = "Steam Workshop"

const (
	// maxAttempts is the total number of tries for one request - the
	// original plus two retries.
	maxAttempts = 3
	// baseBackoff is the first retry delay; each further retry doubles it.
	baseBackoff = 500 * time.Millisecond
	// maxBackoff caps lmm's OWN exponential wait, and is the most
	// Valve-named waiting one request sits through (#671: a longer
	// Retry-After is honoured as a hold rather than slept or cut short), so
	// an update check never sits behind a five-minute sleep.
	maxBackoff = 30 * time.Second
	// maxHold caps any one wait Valve can name, so a bogus header cannot
	// shut lmm out of the Workshop for longer than a day.
	maxHold = 24 * time.Hour
	// breakerThreshold is how many consecutive failed requests trip the
	// circuit breaker.
	breakerThreshold = 3
	// breakerCooldown is how long the breaker holds calls off once tripped.
	breakerCooldown = 5 * time.Minute
)

// retryTransport retries a throttled (429) or failing (5xx) request with
// exponential backoff and jitter, and refuses to dial at all while the
// circuit breaker is open.
//
// Only idempotent, body-rewindable requests are retried: everything this
// package sends is a read (GetPublishedFileDetails is a POST only because
// Valve's endpoint takes a form), so re-sending it is safe by construction.
type retryTransport struct {
	*httpclient.RetryTransport
	holds *httpclient.MemoryHolds
}

// retryPolicy is Steam Workshop's numbers and wording.
var retryPolicy = httpclient.RetryPolicy{
	Service:       serviceName,
	MaxAttempts:   maxAttempts,
	BaseBackoff:   baseBackoff,
	MaxBackoff:    maxBackoff,
	MaxRetryAfter: maxBackoff,
	MaxHold:       maxHold,
	Exhausted: func(last error, _ int) error {
		return fmt.Errorf("%w: %w", ErrMetadataUnavailable, last)
	},
	Refused: func(err *source.RetryLaterError) error {
		return fmt.Errorf("%w: %w", ErrMetadataUnavailable, err)
	},
}

func newRetryTransport(base http.RoundTripper, now func() time.Time) *retryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	holds := httpclient.NewMemoryHolds(serviceName, now, breakerThreshold, breakerCooldown, maxHold)
	return &retryTransport{
		RetryTransport: &httpclient.RetryTransport{
			Base: base, Policy: retryPolicy, Holds: holds, Now: now, Sleep: retrySleep,
		},
		holds: holds,
	}
}

// retrySleep is the sleep every new retryTransport takes. A var only so the
// package's external tests can build a Source whose retries skip their
// backoff (SkipRetryBackoffForTest, #576); it is read once, at
// construction, so a test's change never reaches a transport already built.
var retrySleep = httpclient.SleepContext

// breakerOpen reports whether calls are currently suspended, and until
// when.
func (t *retryTransport) breakerOpen() (time.Time, bool) {
	held, open := t.holds.Active(nil)
	return held.Until, open
}

// RoundTrip implements http.RoundTripper: the shared transport's, with a
// request whose body cannot be sent again worded as this source's own
// failure, which is what its callers test for.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.RetryTransport.RoundTrip(req)
	if errors.Is(err, httpclient.ErrReplay) {
		return nil, fmt.Errorf("%w: %w", ErrMetadataUnavailable, err)
	}
	return resp, err
}

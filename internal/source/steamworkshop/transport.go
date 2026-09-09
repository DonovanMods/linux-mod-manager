// Package steamworkshop: this file is the backoff half of the spike's
// "caching and backoff are mandatory, not optional" finding - retries with
// jitter for a throttled or failing API, and a process-lifetime circuit
// breaker so a sustained outage stops costing a request per item.
//
// It lives in a RoundTripper rather than around each call for two reasons:
// only this layer sees the Retry-After header, and only this layer can
// rewind a form POST's body (http.Request.GetBody) to send it again.
package steamworkshop

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// maxAttempts is the total number of tries for one request - the
	// original plus two retries.
	maxAttempts = 3
	// baseBackoff is the first retry delay; each further retry doubles it.
	baseBackoff = 500 * time.Millisecond
	// maxBackoff caps one wait, so an absurd Retry-After cannot wedge an
	// update check behind a five-minute sleep.
	maxBackoff = 30 * time.Second
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
	base http.RoundTripper
	now  func() time.Time
	// sleep waits out one backoff, or returns the request context's error
	// the moment the caller gives up. GO.md's context-threading rule: with
	// maxBackoff at 30s and two retries, a Ctrl-C during `lmm update` (or an
	// SSE client disconnect) could otherwise wait out a minute of sleeping
	// before the cancellation was observed. Injectable so the tests assert
	// the backoff POLICY without spending it.
	sleep func(context.Context, time.Duration) error

	mu           sync.Mutex
	failures     int
	suspendUntil time.Time
}

func newRetryTransport(base http.RoundTripper, now func() time.Time) *retryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &retryTransport{base: base, now: now, sleep: sleepCtx}
}

// RoundTrip implements http.RoundTripper.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if until, open := t.breakerOpen(); open {
		return nil, fmt.Errorf("%w: suspended after repeated failures, retrying after %s",
			ErrMetadataUnavailable, until.Format(time.RFC3339))
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, err := rewind(req, attempt)
		if err != nil {
			return nil, err
		}
		resp, err := t.base.RoundTrip(body)
		switch {
		case err != nil:
			lastErr = err
		case retryableStatus(resp.StatusCode):
			lastErr = fmt.Errorf("API error (status %d)", resp.StatusCode)
			wait := retryAfter(resp)
			// The body must be drained and closed before the connection can
			// be reused for the retry.
			_ = resp.Body.Close()
			if attempt == maxAttempts {
				t.recordFailure()
				return nil, fmt.Errorf("%w: %v after %d attempts", ErrMetadataUnavailable, lastErr, maxAttempts)
			}
			if err := t.sleep(req.Context(), backoffFor(attempt, wait)); err != nil {
				return nil, err
			}
			continue
		default:
			t.recordSuccess()
			return resp, nil
		}

		if attempt == maxAttempts {
			break
		}
		if err := t.sleep(req.Context(), backoffFor(attempt, 0)); err != nil {
			return nil, err
		}
	}

	t.recordFailure()
	return nil, lastErr
}

// sleepCtx is the production sleep: it waits out d, or gives up the moment
// ctx is done and reports that instead. A cancelled backoff is the caller's
// error, not an API failure, so it is returned rather than folded into
// ErrMetadataUnavailable and never counts toward the circuit breaker.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// rewind returns the request to send on this attempt: the original on the
// first, a body-rewound clone on every retry. A request whose body cannot
// be rewound is never retried - GetBody is set for every form POST
// http.NewRequest builds from a strings.Reader, so this is a guard, not a
// path lmm takes.
func rewind(req *http.Request, attempt int) (*http.Request, error) {
	if attempt == 1 || req.Body == nil {
		return req, nil
	}
	if req.GetBody == nil {
		return nil, fmt.Errorf("%w: request body cannot be replayed", ErrMetadataUnavailable)
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("%w: rewinding request body: %v", ErrMetadataUnavailable, err)
	}
	clone := req.Clone(req.Context())
	clone.Body = body
	return clone, nil
}

// retryableStatus reports whether a status is worth trying again: Valve
// throttling (429) or a server-side failure (5xx). A 4xx that is not 429 is
// lmm's own request being wrong, and repeating it would only be rude.
func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// retryAfter reads a Retry-After header in its delay-seconds form (the form
// Valve sends). An HTTP-date value, or anything unparseable, yields 0 and
// lets the caller's own backoff decide.
func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// backoffFor returns the wait before the next attempt: the server's own
// Retry-After when it named one, else exponential from baseBackoff, in
// both cases capped at maxBackoff and jittered so a batch of requests
// throttled together does not retry in lockstep.
func backoffFor(attempt int, serverHint time.Duration) time.Duration {
	d := serverHint
	if d <= 0 {
		d = baseBackoff << (attempt - 1)
	}
	d = min(d, maxBackoff)
	// Full jitter over the lower half keeps the wait bounded below by half
	// the nominal delay while still spreading a herd.
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// recordFailure counts one exhausted request and trips the breaker at
// breakerThreshold consecutive failures.
func (t *retryTransport) recordFailure() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failures++
	if t.failures >= breakerThreshold {
		t.suspendUntil = t.now().Add(breakerCooldown)
	}
}

// recordSuccess clears the failure streak: the breaker counts CONSECUTIVE
// failures, so one good answer means the API is back.
func (t *retryTransport) recordSuccess() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failures = 0
	t.suspendUntil = time.Time{}
}

// breakerOpen reports whether calls are currently suspended, and until
// when. Expiry resets the streak so the next call is a real probe rather
// than the third strike of an old outage.
func (t *retryTransport) breakerOpen() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.suspendUntil.IsZero() {
		return time.Time{}, false
	}
	if !t.now().Before(t.suspendUntil) {
		t.suspendUntil = time.Time{}
		t.failures = 0
		return time.Time{}, false
	}
	return t.suspendUntil, true
}

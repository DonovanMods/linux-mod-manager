// Package thunderstore: this file is the backoff half of the house pattern
// (steamworkshop/transport.go, lifted): retries with jitter for a throttled
// or failing host, and a process-lifetime circuit breaker so a sustained
// outage stops costing a 34 MB request per search.
//
// It lives in a RoundTripper rather than around the call because only this
// layer sees the Retry-After header, and because a retry has to happen
// BEFORE the streaming decode starts reading the body.
//
// # What Thunderstore documents, and what these numbers rest on (#436)
//
// Thunderstore publishes NO rate limit for the endpoints lmm uses - the
// community listing (/c/<community>/api/v1/package/) and the package
// download (/package/download/<ns>/<name>/<version>/). Checked 2026-09-16:
//
//   - The API documentation (https://thunderstore.io/api/docs/) states no
//     limit, quota or throttle for any endpoint.
//   - The server's own source (github.com/thunderstore-io/Thunderstore,
//     django/thunderstore/core/settings.py) configures Django REST
//     Framework with NO default throttle classes or rates. The only
//     throttle in the codebase is 6 requests per MINUTE on the experimental
//     legacy-profile upload (modpacks/api/experimental/views/legacyprofile.py),
//     which lmm never calls.
//   - The listing itself is a precomputed per-community blob
//     (repository/api/v1/viewsets.py, APIV1PackageCache) served with
//     Last-Modified - a conditional GET is a 304 with no body - and
//     measured with Cache-Control: max-age=30 (docs/plans/
//     2026-09-10-thunderstore-design.md, Appendix A). With no blob it
//     answers 503 "No cache available", which is retried like any 5xx.
//
// So a 429 lmm sees comes from the edge in front of the application, at a
// rate nobody publishes. The constants below are therefore pinned to the
// numbers that ARE real rather than to a guessed quota: a Retry-After is
// honoured as a floor and waited out up to maxRetryAfter - one minute, the
// only window Thunderstore's own throttling uses, across the whole request
// - and a longer one is refused and remembered, by every lmm process
// (hold.go), rather than slept through; lmm's own exponential
// backoff tops out below that window; and the index TTL (index.go, six
// hours) already keeps a warm index from asking more often than the
// listing's own 30-second max-age could ever make useful.
package thunderstore

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

// serviceName is how a notice or an error names the host being waited on.
const serviceName = "Thunderstore"

const (
	// maxAttempts is the total number of tries for one request - the
	// original plus two retries.
	maxAttempts = 3
	// baseBackoff is the first retry delay; each further retry doubles it.
	baseBackoff = 500 * time.Millisecond
	// maxBackoff caps lmm's OWN exponential wait. It sits below
	// maxRetryAfter, so lmm never outwaits what a server would ask for.
	maxBackoff = 30 * time.Second
	// maxRetryAfter is the most server-named waiting one request sits
	// through, in total (T3 review F8): one minute, the only throttling
	// window Thunderstore's code uses (see the package doc). A wait that
	// would take it past that is not slept - a search that went silent for
	// five minutes would read as a hang, which is #436 - but it IS
	// honoured: no lmm process sends the host a request before the time it
	// named (hold.go).
	maxRetryAfter = time.Minute
	// breakerThreshold is how many consecutive failed requests - across
	// every lmm process, since the streak is persisted - trip the circuit
	// breaker, for the host or for one community (hold.go).
	breakerThreshold = 3
	// breakerCooldown is how long the breaker holds calls off once tripped,
	// and how long a failure streak lasts without another failure.
	breakerCooldown = 5 * time.Minute
)

// retryTransport retries a throttled (429) or failing (5xx, or no answer
// at all) request with exponential backoff and jitter, and refuses to dial
// at all while the host is held (hold.go).
//
// Every wait it is about to sit through, and every refusal, is reported as
// a source.Notice on the request's context (#436), so a frontend can say
// "rate limited by Thunderstore, retrying in 12s" instead of going quiet.
//
// It keeps only the HOST's holds: a 429 or a 5xx is the shared host's, and
// holds every community (T3 review F9). A failure that belongs to one
// community - a 404, a document that will not parse - is recorded against
// that community by the index build, which is the layer that can tell.
//
// Everything this package sends is an idempotent, bodiless GET, so
// re-sending one is safe by construction.
type retryTransport struct {
	base http.RoundTripper
	now  func() time.Time
	// sleep waits out one backoff, or returns the request context's error
	// the moment the caller gives up. GO.md's context-threading rule: with
	// maxRetryAfter at a minute and two retries, a Ctrl-C during a cold
	// index build (or a closed browser tab) could otherwise wait out two
	// minutes of sleeping before the cancellation was observed. Injectable
	// so the tests assert the backoff POLICY without spending it.
	sleep func(context.Context, time.Duration) error
	// holds is the persisted hold state, shared with the index build.
	holds *holdStore
}

func newRetryTransport(base http.RoundTripper, now func() time.Time, holds *holdStore) *retryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	if holds == nil {
		holds = newHoldStore("", now)
	}
	return &retryTransport{base: base, now: now, sleep: sleepCtx, holds: holds}
}

// RoundTrip implements http.RoundTripper.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if held, _, open := t.holds.active(""); open {
		source.Notify(ctx, source.Notice{Kind: source.NoticeSuspended, Source: serviceName, Until: held.Until})
		return nil, &source.RetryLaterError{Source: serviceName, Until: held.Until, Reason: held.Reason}
	}
	community := communityOfPath(req.URL.Path)

	var lastErr error
	// named is the server-named wait this request has already sat
	// through. maxRetryAfter bounds the TOTAL, not each wait (T3 review
	// F8): two Retry-After: 60 answers used to hold a search silent for
	// two minutes.
	var named time.Duration
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := t.base.RoundTrip(req)
		retry := source.Notice{Kind: source.NoticeRetry, Source: serviceName, Attempt: attempt + 1, MaxAttempts: maxAttempts}
		var hint time.Duration
		switch {
		case err != nil:
			if ctx.Err() != nil {
				// The caller gave up: not an upstream failure, and nothing
				// to retry or count against the host.
				return nil, err
			}
			lastErr = err
			retry.Reason, retry.Err = source.RetryNetworkError, err
			if errors.Is(err, httpclient.ErrStalled) {
				retry.Reason = source.RetryStalled
			}
		case retryableStatus(resp.StatusCode):
			lastErr = statusFailure(resp.StatusCode)
			retry.Reason, retry.Status, retry.Err = retryReason(resp.StatusCode), resp.StatusCode, lastErr
			hint = retryAfter(resp, t.now())
			// Closed so the connection can be released before the retry.
			// Not drained: an unread 429/5xx body means the connection is
			// not returned to the pool, which is the right trade for an
			// error response whose size lmm has no reason to trust.
			_ = resp.Body.Close()
			if hint > maxRetryAfter-named {
				held := t.holds.named("", t.now().Add(hint), fmt.Sprintf("%v, which asked lmm to wait %s", lastErr, hint))
				return nil, &source.RetryLaterError{Source: serviceName, Until: held.Until, Reason: held.Reason}
			}
			if attempt == maxAttempts {
				t.failed(lastErr, community)
				if hint > 0 {
					// Out of attempts is not out of obligation: the time the
					// server named still holds for the next request.
					t.holds.named("", t.now().Add(hint), fmt.Sprintf("%v, which asked lmm to wait %s", lastErr, hint))
				}
				return nil, fmt.Errorf("%w: %w after %d attempts", ErrIndexUnavailable, lastErr, maxAttempts)
			}
		default:
			t.holds.succeeded("")
			return resp, nil
		}

		if attempt == maxAttempts {
			break
		}
		retry.Wait = backoffFor(attempt, hint)
		named += hint
		source.Notify(ctx, retry)
		if err := t.sleep(ctx, retry.Wait); err != nil {
			return nil, err
		}
	}

	t.failed(lastErr, community)
	return nil, lastErr
}

// failed records one exhausted request against the host, naming the
// community it was for: a hold the host trips then says which game's index
// the last failure was fetching (T3 review F9).
func (t *retryTransport) failed(err error, community string) {
	why := err.Error()
	if community != "" {
		why = fmt.Sprintf("%s (fetching the %s index)", why, community)
	}
	t.holds.failed("", why)
}

// communityOfPath names the community a listing request is for, or "".
func communityOfPath(path string) string {
	rest, ok := strings.CutPrefix(path, "/c/")
	if !ok {
		return ""
	}
	community, _, _ := strings.Cut(rest, "/")
	if !communityPattern.MatchString(community) {
		return ""
	}
	return community
}

// statusFailure words a retryable status for the error a user finally sees.
func statusFailure(status int) error {
	if status == http.StatusTooManyRequests {
		return fmt.Errorf("rate limited by %s (HTTP %d)", serviceName, status)
	}
	return fmt.Errorf("%s answered HTTP %d", serviceName, status)
}

// retryReason classifies a retryable status for a notice.
func retryReason(status int) string {
	if status == http.StatusTooManyRequests {
		return source.RetryRateLimited
	}
	return source.RetryServerError
}

// sleepCtx is the production sleep: it waits out d, or gives up the moment
// ctx is done and reports that instead. A cancelled backoff is the caller's
// error, not an upstream failure, so it is returned rather than folded into
// ErrIndexUnavailable and never counts toward the circuit breaker.
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

// retryableStatus reports whether a status is worth trying again: a
// throttle (429) or a server-side failure (5xx). A 4xx that is not 429 is
// lmm's own request being wrong, and repeating it would only be rude. A 304
// is a SUCCESS here - it is the answer a conditional GET is asking for.
func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// retryAfter reads a Retry-After header in either of its RFC 9110 forms,
// capped at maxHold (httpclient.RetryAfter). Anything unparseable,
// negative or already past yields 0 and lets the caller's own backoff
// decide.
func retryAfter(resp *http.Response, now time.Time) time.Duration {
	return httpclient.RetryAfter(resp.Header.Get("Retry-After"), now, maxHold)
}

// backoffFor returns the wait before the next attempt.
//
// A server's own Retry-After is a FLOOR: coming back early is how a
// throttled client gets throttled again, so the hint is waited in full and
// the jitter only ever adds to it (a tenth, at most a second) to keep a
// herd from returning in lockstep. Without a hint the wait is exponential
// from baseBackoff, capped at maxBackoff, with full jitter over the lower
// half so it is still bounded below by half the nominal delay.
func backoffFor(attempt int, serverHint time.Duration) time.Duration {
	if serverHint > 0 {
		spread := min(serverHint/10, time.Second)
		return serverHint + time.Duration(rand.Int64N(int64(spread)+1))
	}
	d := min(baseBackoff<<(attempt-1), maxBackoff)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

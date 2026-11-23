// Package httpclient: this file is the RETRY half of the house pattern
// (#436, lifted here by #671 from steamworkshop/transport.go and
// thunderstore/transport.go, which both run on it now, as does NexusMods).
//
// A RetryTransport retries a throttled (429) or failing (5xx, or no answer
// at all) request with exponential backoff and jitter, honours the wait a
// host names - a Retry-After header, or one the policy reads from the body
// (Cloudflare's rate-limit page, CloudflareRetryAfter) - as a FLOOR, and
// refuses rather than sleeps through a wait longer than the policy will sit
// through, remembering it as a HOLD (HoldKeeper) so no later request asks
// before the time the host named. Every wait it is about to sit through,
// and every refusal, is reported as a source.Notice on the request's
// context, so a frontend says "rate limited by NexusMods, retrying in 30s"
// instead of going quiet.
//
// It lives in a RoundTripper rather than around each call because only this
// layer sees the response headers before a decoder does, and only this
// layer can rewind a request body (http.Request.GetBody) to send a POST -
// a Steam Web API form, a NexusMods GraphQL query - again.
//
// What differs between the hosts is data, not code: the policy's numbers,
// how a refusal or an exhausted request is worded (the sentinel each
// source's callers test for), and where a hold is kept (in memory, or a
// file every lmm process shares: FileHolds).
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// retryHintBodyLimit bounds how much of a retryable answer's body is read
// for a wait it names: Cloudflare's problem document is a few hundred bytes.
const retryHintBodyLimit = 16 << 10

// RetryPolicy is one host's retry numbers and wording.
type RetryPolicy struct {
	// Service is how a notice or an error names the host ("NexusMods").
	Service string
	// MaxAttempts is the total number of tries for one request, the
	// original included.
	MaxAttempts int
	// BaseBackoff is the first retry delay when the host named none; each
	// further retry doubles it, up to MaxBackoff.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// MaxRetryAfter is the most host-named waiting one request sits
	// through, in total. A wait that would take it past that is not slept:
	// it is recorded as a hold and the request refused.
	MaxRetryAfter time.Duration
	// MaxHold caps any one wait a host can name, so a bogus header cannot
	// shut lmm out for a year.
	MaxHold time.Duration
	// WaitOutHold, when set, is how close to its end a hold must be for a
	// request to wait it out rather than be refused: a host that asked for
	// thirty seconds a moment ago has a bulk check sleep the rest of them,
	// not fail every remaining mod. Zero refuses every held request.
	WaitOutHold time.Duration
	// BodyHint, when set, reads a wait from a retryable answer's body (its
	// first retryHintBodyLimit bytes), for a host that names one there and
	// not in a Retry-After header. Zero is "no hint".
	BodyHint func(body []byte) time.Duration
	// Observe, when set, sees every answer, and may name a time before
	// which the host must not be asked again with this credential: a
	// quota the answer says is used up. It is recorded as a quota hold
	// (QuotaKeeper, else an ordinary named hold), and on a retryable
	// answer the later of it and the host's own wait is the hint. A zero
	// time records nothing.
	Observe func(resp *http.Response, now time.Time) (until time.Time, why string)
	// Exhausted words a request whose attempts ran out on a retryable
	// status; last is a *StatusError carrying the attempts. Nil returns
	// last as is.
	Exhausted func(last error, attempts int) error
	// Refused words a request a hold refuses. Nil is the
	// *source.RetryLaterError itself.
	Refused func(err *source.RetryLaterError) error
}

// Backoff returns the wait before attempt+1.
//
// A host's own wait is a FLOOR: coming back early is how a throttled client
// gets throttled again, so the hint is waited in full and the jitter only
// adds to it (a tenth, at most a second) to keep a herd from returning in
// lockstep. Without a hint the wait is exponential from BaseBackoff, capped
// at MaxBackoff, with full jitter over the lower half so it is still bounded
// below by half the nominal delay.
func (p RetryPolicy) Backoff(attempt int, hint time.Duration) time.Duration {
	if hint > 0 {
		spread := min(hint/10, time.Second)
		return hint + time.Duration(rand.Int64N(int64(spread)+1))
	}
	d := min(p.BaseBackoff<<(attempt-1), p.MaxBackoff)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// HoldKeeper keeps a RetryTransport's holds: the waits a host named past
// what a request sits through, and the failure streak that trips the
// circuit breaker. req is the request in question, for a keeper that holds
// part of a host (a Thunderstore community); it may be nil.
type HoldKeeper interface {
	// Active reports the hold in force for req, if any.
	Active(req *http.Request) (source.Hold, bool)
	// Named records a wait the host asked for, never shortening one
	// already in force, and returns the hold now in force.
	Named(req *http.Request, until time.Time, why string) source.Hold
	// Failed records one request whose attempts ran out.
	Failed(req *http.Request, err error)
	// Succeeded records an answer: the failure streak, and a hold lmm set
	// itself, end. A hold the host named stands until it expires.
	Succeeded(req *http.Request)
}

// QuotaKeeper is a HoldKeeper that keeps a quota hold apart from the
// host's own (#671): a hold Observe names belongs to the credential the
// request was sent with, so a new credential can lift it while a throttle
// or a breaker hold - the host's, or the address's - stands. A keeper
// that does not implement it records a quota hold with Named.
type QuotaKeeper interface {
	// Quota records a quota hold, never shortening one already in force,
	// and returns the hold now in force for req.
	Quota(req *http.Request, until time.Time, why string) source.Hold
}

// RetryTransport is the retrying http.RoundTripper. Base, Policy and Holds
// are required; Now and Sleep default to the real clock.
type RetryTransport struct {
	Base   http.RoundTripper
	Policy RetryPolicy
	Holds  HoldKeeper
	// Now is the clock a hint and a hold are judged against.
	Now func() time.Time
	// Sleep waits out one backoff, or returns the request context's error
	// the moment the caller gives up: a Ctrl-C or a closed browser tab is
	// observed during a thirty-second wait, not after it. Injectable so a
	// test asserts the policy without spending it.
	Sleep func(context.Context, time.Duration) error
}

// StatusError is a retryable status a host answered: a throttle (429) or a
// server failure (5xx). RetryIn is the wait the host named, if any, and
// Attempts how many tries it was the answer to once lmm gave up (0 while it
// is still being retried).
type StatusError struct {
	Service  string
	Status   int
	RetryIn  time.Duration
	Attempts int
}

// RateLimited reports whether the host was throttling lmm.
func (e *StatusError) RateLimited() bool { return e.Status == http.StatusTooManyRequests }

// Error words the answer for a person: a throttle as a rate limit with the
// time to come back, never the host's raw error document.
func (e *StatusError) Error() string {
	if e.Attempts == 0 {
		if e.RateLimited() {
			return fmt.Sprintf("rate limited by %s (HTTP %d)", e.Service, e.Status)
		}
		return fmt.Sprintf("%s answered HTTP %d", e.Service, e.Status)
	}
	if e.RateLimited() {
		when := "later"
		if e.RetryIn > 0 {
			when = "in " + e.RetryIn.Round(time.Second).String()
		}
		return fmt.Sprintf("%s rate limit: try again %s (still limited after %d attempts)", e.Service, when, e.Attempts)
	}
	return fmt.Sprintf("%s answered HTTP %d (gave up after %d attempts)", e.Service, e.Status, e.Attempts)
}

// RoundTrip implements http.RoundTripper.
func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	p := t.Policy
	waited, err := t.waitForHold(req)
	if err != nil {
		return nil, err
	}

	var lastErr error
	// named is the host-named wait this request has already sat through,
	// a hold it waited out included: MaxRetryAfter bounds the TOTAL, not
	// each wait (Thunderstore T3 review F8) - two Retry-After: 60 answers
	// would otherwise hold one request silent for two minutes.
	named := waited
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		send, err := rewind(req, attempt)
		if err != nil {
			return nil, err
		}
		resp, err := t.Base.RoundTrip(send)
		retry := source.Notice{Kind: source.NoticeRetry, Source: p.Service, Attempt: attempt + 1, MaxAttempts: p.MaxAttempts}
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
			if errors.Is(err, ErrStalled) {
				retry.Reason = source.RetryStalled
			}
		case retryableStatus(resp.StatusCode):
			var hostHint time.Duration
			hostHint, hint = t.hint(req, resp)
			// Closed so the connection can be released before the retry.
			// Not drained past the hint: an unread error body means the
			// connection is not returned to the pool, which is the right
			// trade for a response whose size lmm has no reason to trust.
			_ = resp.Body.Close()
			failure := &StatusError{Service: p.Service, Status: resp.StatusCode, RetryIn: hint}
			lastErr = failure
			retry.Reason, retry.Status, retry.Err = retryReason(resp.StatusCode), resp.StatusCode, failure
			if hint > p.MaxRetryAfter-named {
				// Past what this request sits through. A quota's wait is
				// already recorded (hint); the host's own is recorded here,
				// and the refusal names whichever ends later.
				if hostHint > 0 {
					t.Holds.Named(req, t.now().Add(hostHint), fmt.Sprintf("%v, which asked lmm to wait %s", failure, hostHint))
				}
				held, ok := t.Holds.Active(req)
				if !ok {
					held = source.Hold{Source: p.Service, Until: t.now().Add(hint), Reason: failure.Error()}
				}
				return nil, t.refuse(ctx, held)
			}
			if attempt == p.MaxAttempts {
				t.Holds.Failed(req, failure)
				if hostHint > 0 {
					// Out of attempts is not out of obligation: the time the
					// host named still holds for the next request.
					t.Holds.Named(req, t.now().Add(hostHint), fmt.Sprintf("%v, which asked lmm to wait %s", failure, hostHint))
				}
				failure.Attempts = p.MaxAttempts
				if p.Exhausted != nil {
					return nil, p.Exhausted(failure, p.MaxAttempts)
				}
				return nil, failure
			}
		default:
			t.Holds.Succeeded(req)
			if p.Observe != nil {
				if until, why := p.Observe(resp, t.now()); !until.IsZero() {
					t.holdQuota(req, until, why)
				}
			}
			return resp, nil
		}

		if attempt == p.MaxAttempts {
			break
		}
		retry.Wait = p.Backoff(attempt, hint)
		named += hint
		source.Notify(ctx, retry)
		if err := t.sleep(ctx, retry.Wait); err != nil {
			return nil, err
		}
	}

	t.Holds.Failed(req, lastErr)
	return nil, lastErr
}

// waitForHold returns how long it waited before req may be sent: nothing
// when no hold is in force (or the one in force has just ended), the rest
// of a hold that ends within the policy's WaitOutHold, which it waits out.
// A hold further off refuses the request.
//
// A waited-out hold is reported as the suspension it is - "not asking ...
// again until" - not as a retry of a status: lmm does not know here
// whether a throttle, a server failure or a spent quota set it.
func (t *RetryTransport) waitForHold(req *http.Request) (time.Duration, error) {
	ctx := req.Context()
	held, open := t.Holds.Active(req)
	if !open {
		return 0, nil
	}
	left := held.Until.Sub(t.now())
	if left <= 0 {
		return 0, nil
	}
	if left > t.Policy.WaitOutHold || left > t.Policy.MaxRetryAfter {
		return 0, t.refuse(ctx, held)
	}
	source.Notify(ctx, source.Notice{Kind: source.NoticeSuspended, Source: t.Policy.Service, GameID: held.GameID, Until: held.Until})
	return left, t.sleep(ctx, left)
}

// refuse reports held as a suspension notice and returns the refusal.
func (t *RetryTransport) refuse(ctx context.Context, held source.Hold) error {
	source.Notify(ctx, source.Notice{Kind: source.NoticeSuspended, Source: t.Policy.Service, GameID: held.GameID, Until: held.Until})
	err := &source.RetryLaterError{Source: t.Policy.Service, GameID: held.GameID, Until: held.Until, Reason: held.Reason}
	if t.Policy.Refused != nil {
		return t.Policy.Refused(err)
	}
	return err
}

// holdQuota records a quota hold: apart from the host's holds when the
// keeper keeps them apart (QuotaKeeper), else as a named hold.
func (t *RetryTransport) holdQuota(req *http.Request, until time.Time, why string) {
	if q, ok := t.Holds.(QuotaKeeper); ok {
		q.Quota(req, until, why)
		return
	}
	t.Holds.Named(req, until, why)
}

// hint reads the waits a retryable answer names. host is the host's own:
// its Retry-After header, else what the policy reads from its body. A
// quota the answer says is spent (Observe) is recorded as a quota hold
// here, and hint is the later of the two - a 429 that names thirty
// seconds while the hour's quota is gone is not over in thirty seconds.
// Neither is ever more than MaxHold.
func (t *RetryTransport) hint(req *http.Request, resp *http.Response) (host, hint time.Duration) {
	now := t.now()
	host = RetryAfter(resp.Header.Get("Retry-After"), now, t.Policy.MaxHold)
	if host == 0 && t.Policy.BodyHint != nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, retryHintBodyLimit))
		host = min(max(t.Policy.BodyHint(body), 0), t.Policy.MaxHold)
	}
	hint = host
	if t.Policy.Observe != nil {
		if until, why := t.Policy.Observe(resp, now); until.After(now) {
			t.holdQuota(req, until, why)
			hint = max(hint, min(until.Sub(now), t.Policy.MaxHold))
		}
	}
	return host, hint
}

func (t *RetryTransport) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *RetryTransport) sleep(ctx context.Context, d time.Duration) error {
	if t.Sleep != nil {
		return t.Sleep(ctx, d)
	}
	return SleepContext(ctx, d)
}

// SleepContext waits out d, or gives up the moment ctx is done and reports
// that instead. A cancelled backoff is the caller's error, not an upstream
// failure, so it is returned as is and never counts against the host.
func SleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ErrReplay reports a request whose body cannot be sent again.
var ErrReplay = errors.New("request body cannot be replayed")

// rewind returns the request to send on this attempt: the original on the
// first, a body-rewound clone on every retry. A request whose body cannot
// be rewound is never retried - GetBody is set for every request
// http.NewRequest builds from a bytes or strings reader, so this is a
// guard, not a path lmm takes.
func rewind(req *http.Request, attempt int) (*http.Request, error) {
	if attempt == 1 || req.Body == nil || req.Body == http.NoBody {
		return req, nil
	}
	if req.GetBody == nil {
		return nil, ErrReplay
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReplay, err)
	}
	clone := req.Clone(req.Context())
	clone.Body = body
	return clone, nil
}

// retryableStatus reports whether a status is worth trying again: a
// throttle (429) or a server-side failure (5xx). A 4xx that is not 429 is
// lmm's own request being wrong, and repeating it would only be rude.
func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// retryReason classifies a retryable status for a notice.
func retryReason(status int) string {
	if status == http.StatusTooManyRequests {
		return source.RetryRateLimited
	}
	return source.RetryServerError
}

// CloudflareRetryAfter reads the retry_after a Cloudflare rate-limit page
// (error 1015, an RFC 9457 problem document) names in seconds - the one
// place its 429 says how long to wait, since it sends no Retry-After header
// (#671). Anything else is 0.
func CloudflareRetryAfter(body []byte) time.Duration {
	var doc struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if len(body) == 0 || json.Unmarshal(body, &doc) != nil || doc.RetryAfter <= 0 {
		return 0
	}
	// Bounded before it becomes a Duration, so an absurd value saturates
	// rather than wraps; the transport caps it at MaxHold.
	const ceiling = float64(365 * 24 * time.Hour / time.Second)
	return time.Duration(min(doc.RetryAfter, ceiling) * float64(time.Second))
}

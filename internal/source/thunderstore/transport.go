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
	"fmt"
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

// retryTransport is the shared retrying transport (httpclient.RetryTransport,
// #671 lifted it from here) over this host's holds: it retries a throttled
// (429) or failing (5xx, or no answer at all) request with exponential
// backoff and jitter, and refuses to dial at all while the host is held
// (hold.go).
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
	*httpclient.RetryTransport
	// holds is the persisted hold state, shared with the index build.
	holds *holdStore
}

// retryPolicy is Thunderstore's numbers (see the package doc) and its
// wording: an exhausted request is ErrIndexUnavailable.
var retryPolicy = httpclient.RetryPolicy{
	Service:       serviceName,
	MaxAttempts:   maxAttempts,
	BaseBackoff:   baseBackoff,
	MaxBackoff:    maxBackoff,
	MaxRetryAfter: maxRetryAfter,
	MaxHold:       maxHold,
	Exhausted: func(last error, _ int) error {
		return fmt.Errorf("%w: %w", ErrIndexUnavailable, last)
	},
}

func newRetryTransport(base http.RoundTripper, now func() time.Time, holds *holdStore) *retryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	if holds == nil {
		holds = newHoldStore("", now)
	}
	return &retryTransport{
		RetryTransport: &httpclient.RetryTransport{
			Base: base, Policy: retryPolicy, Holds: hostHolds{holds}, Now: now, Sleep: retrySleep,
		},
		holds: holds,
	}
}

// retrySleep is the sleep every new retryTransport takes. A var only so the
// package's external tests can build a Source whose retries skip their
// backoff (SkipRetryBackoffForTest, #576); it is read once, at
// construction, so a test's change never reaches a transport already built.
var retrySleep = httpclient.SleepContext

// hostHolds is the holdStore as the shared transport's HoldKeeper: the
// host's scope for a hold, and the request's community named in a failure,
// so a hold the host trips says which game's index the last failure was
// fetching (T3 review F9).
type hostHolds struct{ store *holdStore }

func (h hostHolds) Active(*http.Request) (source.Hold, bool) {
	held, _, open := h.store.active("")
	return source.Hold{Source: serviceName, Until: held.Until, Reason: held.Reason}, open
}

func (h hostHolds) Named(_ *http.Request, until time.Time, why string) source.Hold {
	held := h.store.named("", until, why)
	return source.Hold{Source: serviceName, Until: held.Until, Reason: held.Reason}
}

func (h hostHolds) Failed(req *http.Request, err error) {
	why := err.Error()
	if community := communityOfPath(req.URL.Path); community != "" {
		why = fmt.Sprintf("%s (fetching the %s index)", why, community)
	}
	h.store.failed("", why)
}

func (h hostHolds) Succeeded(*http.Request) { h.store.succeeded("") }

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

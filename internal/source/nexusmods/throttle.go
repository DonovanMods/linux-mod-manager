// Package nexusmods: this file is the throttle half of the client (#671) -
// the shared retrying transport (httpclient.RetryTransport) with NexusMods'
// numbers, and the two places NexusMods says how long to wait.
//
// # What NexusMods and its edge say, and what these numbers rest on
//
// A throttle reaches lmm from two places:
//
//   - Cloudflare, in front of api.nexusmods.com, answers a burst with error
//     1015: a 429 whose wait is ONLY in its JSON problem body
//     ("retry_after":30), with no Retry-After header. That is what the
//     owner's verify of a 353-mod collection hit (#671), on GetModFiles
//     sent once per mod, back to back.
//   - NexusMods' own API meters a key by the hour and the day, and says so
//     on every v1 answer: X-RL-Hourly-Remaining / X-RL-Daily-Remaining, with
//     X-RL-Hourly-Reset the time the hourly budget refills. A request is
//     served while EITHER budget has some left, so only both at zero means
//     "no more until the reset".
//
// Vortex (Nexus-Mods/node-nexus-api, src/Quota.ts and src/Nexus.ts) reads
// the same two Remaining headers on every answer and, when both are spent,
// sends nothing until the next full hour; a 429 with budget left is retried
// at most twice (MAX_RATE_LIMIT_RETRIES). lmm does the same, with the reset
// time NexusMods names when it can be read and the next full hour when it
// cannot, and honours Cloudflare's own wait, which Vortex does not read.
//
// The numbers: three attempts; lmm's own backoff from a second, doubling to
// thirty; a host-named wait sat through up to two minutes in total per
// request - Cloudflare's thirty seconds, twice over - and refused and held
// past that; and a hold that ends within those two minutes (the thirty
// seconds Cloudflare named a moment ago) waited out rather than refused, so
// a bulk check pauses rather than failing every remaining mod.
package nexusmods

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

// serviceName is how a notice or an error names the host being waited on.
const serviceName = "NexusMods"

const (
	maxAttempts   = 3
	baseBackoff   = time.Second
	maxBackoff    = 30 * time.Second
	maxRetryAfter = 2 * time.Minute
	// maxHold caps any one wait the host names: a day is past any quota
	// window NexusMods has (its daily budget), and short enough that a
	// bogus header cannot shut lmm out for longer.
	maxHold = 24 * time.Hour
	// breakerThreshold failed requests in a row hold the host off for
	// breakerCooldown - the house numbers (Steam Workshop, Thunderstore).
	breakerThreshold = 3
	breakerCooldown  = 5 * time.Minute
)

// retryPolicy is NexusMods' retry numbers and the two places it names a
// wait beyond Retry-After.
var retryPolicy = httpclient.RetryPolicy{
	Service:       serviceName,
	MaxAttempts:   maxAttempts,
	BaseBackoff:   baseBackoff,
	MaxBackoff:    maxBackoff,
	MaxRetryAfter: maxRetryAfter,
	MaxHold:       maxHold,
	WaitOutHold:   maxRetryAfter,
	BodyHint:      httpclient.CloudflareRetryAfter,
	Observe:       quotaHold,
}

// retrySleep is the sleep every new client's retries take. A var only so a
// test can build a source whose retries skip their backoff; it is read once,
// at construction.
var retrySleep = httpclient.SleepContext

// newRetryingClient returns httpClient with its transport wrapped in the
// retrying one, and the holds that transport keeps.
func newRetryingClient(httpClient *http.Client) (*http.Client, *httpclient.RetryTransport, *httpclient.MemoryHolds) {
	holds := httpclient.NewMemoryHolds(serviceName, nil, breakerThreshold, breakerCooldown, maxHold)
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	rt := &httpclient.RetryTransport{Base: base, Policy: retryPolicy, Holds: holds, Sleep: retrySleep}
	retrying := *httpClient
	retrying.Transport = rt
	return &retrying, rt, holds
}

// quotaWhy is the reason a spent quota holds the host for.
const quotaWhy = "NexusMods' API quota for this key is used up (none left this hour or today)"

// quotaHold reads NexusMods' quota headers: both budgets spent holds the
// host until the hourly reset it names, or the next full hour when the
// reset cannot be read (Vortex's rule). Anything else holds nothing.
func quotaHold(resp *http.Response, now time.Time) (time.Time, string) {
	daily, okDaily := quotaHeader(resp, "X-RL-Daily-Remaining")
	hourly, okHourly := quotaHeader(resp, "X-RL-Hourly-Remaining")
	if !okDaily || !okHourly || daily > 0 || hourly > 0 {
		return time.Time{}, ""
	}
	if reset, ok := parseReset(resp.Header.Get("X-RL-Hourly-Reset")); ok && reset.After(now) {
		return reset, quotaWhy
	}
	return now.Truncate(time.Hour).Add(time.Hour), quotaWhy
}

func quotaHeader(resp *http.Response, name string) (int, bool) {
	v := strings.TrimSpace(resp.Header.Get(name))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}

// resetLayouts are the forms a reset time has been seen in.
var resetLayouts = []string{time.RFC3339, "2006-01-02T15:04:05-0700", "2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05 MST"}

func parseReset(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	for _, layout := range resetLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Holds implements source.HoldReporter: the throttle or quota hold in
// force, if any, without asking NexusMods anything.
func (n *NexusMods) Holds(context.Context) []source.Hold { return n.client.holds.Holds() }

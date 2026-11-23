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
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"regexp"
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

// holdsFileName is the persisted hold state (#675), in the data dir: a hold
// one lmm process records is honoured by the next. Its lock is
// nexusmods-holds.lock, beside it.
const holdsFileName = "nexusmods-holds.json"

// Option configures a NexusMods source or client.
type Option func(*options)

type options struct {
	dataDir string
}

// WithDataDir keeps the source's throttle and quota holds in dataDir
// (<dataDir>/nexusmods-holds.json), where every lmm process reads them
// (#675). Without it they live in memory, for the life of the process.
func WithDataDir(dataDir string) Option {
	return func(o *options) { o.dataDir = dataDir }
}

// holdsPath is where o's holds are kept, or "" for in memory.
func (o options) holdsPath() string {
	if o.dataDir == "" {
		return ""
	}
	return filepath.Join(o.dataDir, holdsFileName)
}

// newRetryingClient returns httpClient with its transport wrapped in the
// retrying one, over holds.
func newRetryingClient(httpClient *http.Client, holds *nexusHolds) (*http.Client, *httpclient.RetryTransport) {
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	rt := &httpclient.RetryTransport{Base: base, Policy: retryPolicy, Holds: holds, Sleep: retrySleep}
	retrying := *httpClient
	retrying.Transport = rt
	return &retrying, rt
}

// nexusHolds keeps NexusMods' two kinds of hold apart (#671 review), in
// one store every lmm process shares (#675). A throttle Cloudflare named
// and lmm's own circuit breaker belong to the host - and to the address
// lmm asks it from - so they are the store's HOST holds and stand whatever
// key is configured. A spent quota belongs to the KEY: it is held in the
// scope of the key's fingerprint (keyFingerprint), so a user whose free key
// ran out and who adds a Premium one is not refused until the free key's
// hourly reset, while a second process on the spent key still is.
type nexusHolds struct {
	store *httpclient.FileHolds
	// key is the API key requests are sent with now.
	key func() string
}

// quotaScopePattern is the shape of a quota scope: a key's fingerprint.
var quotaScopePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func newNexusHolds(path string, now func() time.Time, key func() string) *nexusHolds {
	return &nexusHolds{
		store: httpclient.NewFileHolds(httpclient.FileHoldsConfig{
			Path:        path,
			ScopesField: "keys",
			ValidScope:  quotaScopePattern.MatchString,
			Now:         now,
			Threshold:   breakerThreshold,
			Cooldown:    breakerCooldown,
			MaxHold:     maxHold,
			// The data dir's own mode (app.Open): it holds lmm.db too.
			DirMode: 0o700,
		}),
		key: key,
	}
}

// keyFingerprint is the scope a key's quota hold is kept under: a
// domain-separated SHA-256 of the key, cut to 128 bits. It tells one key
// from another and cannot be turned back into either, so the file never
// holds the key itself, nor anything a key could be recovered from.
func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte("lmm/nexusmods/quota-hold\x00" + key))
	return hex.EncodeToString(sum[:16])
}

// quotaScope is the scope of the key requests are sent with now.
func (h *nexusHolds) quotaScope() string { return keyFingerprint(h.key()) }

// requestQuotaScope is the scope of the key req was sent with (#675
// review): a quota a response reports spent is that key's, even when the
// configured key changed while the request was in flight. A request with
// no apikey header is the configured key's.
func (h *nexusHolds) requestQuotaScope(req *http.Request) string {
	if req != nil {
		if key := req.Header.Get("apikey"); key != "" {
			return keyFingerprint(key)
		}
	}
	return h.quotaScope()
}

// asHold is a store record as the hold a refusal or a notice names.
func asHold(rec httpclient.HoldRecord) source.Hold {
	return source.Hold{Source: serviceName, Until: rec.Until, Reason: rec.Reason}
}

// Active implements httpclient.HoldKeeper: the host's hold or the current
// key's quota hold, whichever in force ends later.
func (h *nexusHolds) Active(*http.Request) (source.Hold, bool) {
	rec, _, ok := h.store.Active(h.quotaScope())
	if !ok {
		return source.Hold{}, false
	}
	return asHold(rec), true
}

// Named implements httpclient.HoldKeeper: the host's.
func (h *nexusHolds) Named(req *http.Request, until time.Time, why string) source.Hold {
	h.store.Named("", until, why)
	held, _ := h.Active(req)
	return held
}

// Quota implements httpclient.QuotaKeeper: the key's that req was sent
// with.
func (h *nexusHolds) Quota(req *http.Request, until time.Time, why string) source.Hold {
	h.store.Named(h.requestQuotaScope(req), until, why)
	held, _ := h.Active(req)
	return held
}

// Failed implements httpclient.HoldKeeper: the breaker is the host's.
func (h *nexusHolds) Failed(_ *http.Request, err error) { h.store.Failed("", err.Error()) }

// Succeeded implements httpclient.HoldKeeper.
func (h *nexusHolds) Succeeded(*http.Request) { h.store.Succeeded("") }

// list is every hold in force for this process - the host's first, then
// the current key's quota. Another key's quota hold holds nothing here.
func (h *nexusHolds) list() []source.Hold {
	scope := h.quotaScope()
	var out []source.Hold
	for _, held := range h.store.List() {
		if held.Scope == "" || held.Scope == scope {
			out = append(out, asHold(held.HoldRecord))
		}
	}
	return out
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
func (n *NexusMods) Holds(context.Context) []source.Hold { return n.client.holds.list() }

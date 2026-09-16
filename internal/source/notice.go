package source

import (
	"context"
	"fmt"
	"time"
)

// NoticeKind names what a Notice reports.
type NoticeKind string

// The Notice vocabulary (#436). Each is something a source's network layer
// is WAITING on - the one class of event that, left unreported, reads to a
// user as lmm having hung.
const (
	// NoticeRetry: a request failed in a way worth retrying (a throttle, a
	// server error, a dropped connection) and the source is waiting Wait
	// before attempt Attempt of MaxAttempts.
	NoticeRetry NoticeKind = "retry"
	// NoticeSuspended: the source is refusing to send requests at all
	// until Until - a circuit breaker holding off after repeated failures.
	NoticeSuspended NoticeKind = "suspended"
	// NoticeIndexBuilding: a source is building its local search index for
	// GameID from nothing - the one-time wait of a few seconds (#360 §2.7).
	NoticeIndexBuilding NoticeKind = "index_building"
	// NoticeIndexBuilt: that build finished, with Packages rows in Elapsed.
	NoticeIndexBuilt NoticeKind = "index_built"
)

// The RetryReason vocabulary: why a request is being retried.
const (
	RetryRateLimited  = "rate_limited"
	RetryServerError  = "server_error"
	RetryNetworkError = "network_error"
)

// Notice is one thing a source wants the user to know while a call waits on
// it. It is DATA: the sentence a frontend prints is written once, in core,
// from these fields.
//
// Fields apply by Kind - Reason/Status/Attempt/MaxAttempts/Wait to a retry,
// Until to a suspension, GameID/Packages/Elapsed to an index build - and
// are zero otherwise.
type Notice struct {
	Kind NoticeKind
	// Source is the display name of the service being waited on
	// ("Thunderstore").
	Source string

	// Reason is one of the Retry* constants.
	Reason string
	// Status is the HTTP status that caused the retry, 0 for a transport
	// failure.
	Status int
	// Attempt is the attempt ABOUT to be made (2 of 3), not the one that
	// failed.
	Attempt     int
	MaxAttempts int
	Wait        time.Duration
	// Download is set when the wait is for a FILE download rather than for
	// a source's own metadata, so a frontend can file it under the right
	// operation.
	Download bool
	// Err is the failure being retried, for a log line.
	Err error

	Until time.Time

	GameID   string
	Packages int
	Elapsed  time.Duration
}

// NoticeFunc receives Notices. It may be called from any goroutine - a
// search fans out across sources concurrently - so an implementation that
// is not safe for that must serialise itself.
type NoticeFunc func(Notice)

type noticeKey struct{}

// WithNotices returns ctx carrying fn as the observer for every Notice
// raised under it.
//
// A context value, and deliberately so: net/http/httptrace attaches its
// ClientTrace hooks the same way, for the same reason. A throttle is
// discovered inside a RoundTripper several calls below anything that knows
// who is waiting - a CLI command, a web UI job - and threading a callback
// through every ModSource method in between would change the interface of
// every source to serve the two that have anything to say.
//
// The innermost observer wins, and a nil fn is an explicit "nobody is
// listening here", so a caller that reports the same progress through its
// own channel can keep an enclosing observer from repeating it.
func WithNotices(ctx context.Context, fn NoticeFunc) context.Context {
	return context.WithValue(ctx, noticeKey{}, fn)
}

// Notify reports n to the observer on ctx, if there is one.
func Notify(ctx context.Context, n Notice) {
	if fn, _ := ctx.Value(noticeKey{}).(NoticeFunc); fn != nil {
		fn(n)
	}
}

// RetryLaterError reports that a source will not send a request before
// Until: its circuit breaker is open, or the service asked for a longer wait
// than lmm is prepared to sit through. Until is the data a frontend shows as
// "try again at ...".
//
// It classifies as ErrIndexUnavailable: every caller of a service that
// refuses to be asked is, for now, a caller without an answer.
type RetryLaterError struct {
	Source string
	Until  time.Time
	Reason string
}

// Error names the service, when it will be asked again - in local time,
// which is the clock the reader has - and why not before.
func (e *RetryLaterError) Error() string {
	return fmt.Sprintf("not asking %s again until %s: %s", e.Source, clockTime(e.Until), e.Reason)
}

// clockTime renders a moment the way a person checks it against a clock:
// the time alone when it is within the next twelve hours, the date too
// otherwise.
func clockTime(t time.Time) string {
	local := t.Local()
	if d := time.Until(t); d > -12*time.Hour && d < 12*time.Hour {
		return local.Format("15:04:05")
	}
	return local.Format("2006-01-02 15:04:05")
}

// Is makes errors.Is(err, ErrIndexUnavailable) true.
func (e *RetryLaterError) Is(target error) bool { return target == ErrIndexUnavailable }

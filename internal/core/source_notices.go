// Package core: this file turns what a source is WAITING on into events a
// frontend shows (#436).
//
// A source reports a throttle, a suspension or a one-time index build as a
// source.Notice on the context of the call that is waiting (the httptrace
// shape - see source.WithNotices for why a context value). WithSourceNotices
// is how a frontend listens: it installs an observer that renders each
// notice as the core.Event the rest of that frontend already displays, with
// the sentence written here, once, for both frontends.
package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// WithSourceNotices returns ctx with every source.Notice raised under it
// delivered to sink as an Event:
//
//   - a retry or a suspension is a WarningEvent (SourceRetrying,
//     SourceSuspended) - the user must see why nothing is moving;
//   - an index build is a StepEvent (IndexRefreshStarted,
//     IndexRefreshDone), the same phases an explicit refresh reports.
//
// Notices can arrive from several goroutines at once - an unscoped search
// asks every source concurrently - so the notices are serialised among
// themselves here. They are NOT serialised with the flow's own calls to the
// same sink: a notice is delivered on whatever goroutine is waiting, which
// may not be the operation's. A sink given here must therefore be safe to
// call concurrently with the operation's own events - which every frontend
// sink is (the CLI's printer writes one line per call; a web UI job's sink
// takes the job's own lock).
//
// A nil sink returns ctx unchanged: a flow run with nothing to report to
// must not silence an observer set further out.
func WithSourceNotices(ctx context.Context, sink EventSink) context.Context {
	if sink == nil {
		return ctx
	}
	var mu sync.Mutex
	return source.WithNotices(ctx, func(n source.Notice) {
		e := noticeEvent(n, time.Now())
		if e == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		sink(e)
	})
}

// noticeEvent renders one notice as its event, or nil for a kind this
// build does not know.
func noticeEvent(n source.Notice, now time.Time) Event {
	op := OpSourceIndex
	if n.Download {
		op = OpDownload
	}
	text := noticeText(n, now)
	switch n.Kind {
	case source.NoticeRetry:
		return WarningEvent{Scope: Scope{Op: op}, Phase: SourceRetrying, Message: text}
	case source.NoticeSuspended:
		return WarningEvent{Scope: Scope{Op: op}, Phase: SourceSuspended, Message: text}
	case source.NoticeIndexBuilding:
		return StepEvent{Scope: Scope{Op: OpSourceIndex}, Phase: IndexRefreshStarted, Detail: text}
	case source.NoticeIndexBuilt:
		return StepEvent{Scope: Scope{Op: OpSourceIndex}, Phase: IndexRefreshDone, Detail: text}
	default:
		return nil
	}
}

// noticeText is the sentence a notice is shown as. now anchors a
// suspension's "in 4m30s".
func noticeText(n source.Notice, now time.Time) string {
	switch n.Kind {
	case source.NoticeRetry:
		attempt := fmt.Sprintf("retrying in %s (attempt %d of %d).", humanWait(n.Wait), n.Attempt, n.MaxAttempts)
		switch n.Reason {
		case source.RetryRateLimited:
			return fmt.Sprintf("Rate limited by %s; %s", n.Source, attempt)
		case source.RetryServerError:
			return fmt.Sprintf("%s answered HTTP %d; %s", n.Source, n.Status, attempt)
		case source.RetryStalled:
			return fmt.Sprintf("The transfer from %s stalled; %s", n.Source, attempt)
		default:
			return fmt.Sprintf("Could not reach %s; %s", n.Source, attempt)
		}
	case source.NoticeSuspended:
		until := n.Until.Local()
		wait := until.Sub(now)
		// A clock time alone for today's resumption, the date as well for
		// one further off - "until 09:00" is wrong by a day otherwise.
		at := until.Format("15:04:05")
		if wait >= 12*time.Hour || wait <= -12*time.Hour {
			at = until.Format("2006-01-02 15:04:05")
		}
		return fmt.Sprintf("Not asking %s again until %s (in %s).", n.Source, at, humanWait(wait))
	case source.NoticeIndexBuilding:
		return fmt.Sprintf("Building the %s index for %s (one-time)...", n.Source, n.GameID)
	case source.NoticeIndexBuilt:
		return fmt.Sprintf("Indexed %d packages in %s.", n.Packages, n.Elapsed.Round(100*time.Millisecond))
	default:
		return strings.TrimSpace(fmt.Sprintf("%s: %s", n.Source, n.Kind))
	}
}

// humanWait renders a wait the way a person reads one: whole seconds from
// a second up, tenths below ten seconds, and milliseconds under a second.
func humanWait(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < 10*time.Second:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

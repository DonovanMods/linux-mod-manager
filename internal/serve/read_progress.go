package serve

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// Read progress (#583). Two of Mission Control's reads walk the profile's
// mods one at a time and can take seconds on a real setup: the update check
// (a source query per mod) and the full-tier verify behind the Health card
// (the same, for its version pass). They are reads, not mutations, so they
// stay plain GETs answering their frozen documents - turning them into jobs
// would put a plan/confirm/tray row in front of looking at a page.
//
// What a caller can do instead is name a progress TOKEN (?progress=, a short
// client-chosen string), and the read publishes read_progress frames
// carrying it on the session activity stream (GET /api/v1/events) the page
// already holds open. The token is what lets a page tell its OWN read's
// progress from another tab's read of the same profile; it means nothing to
// the server beyond that. No token, no frames: the CLI-shaped document and
// every existing caller are untouched.

// readUpdates and readHealth are a readProgressFrame's Read values: which
// of the two reads is reporting.
const (
	readUpdates = "updates"
	readHealth  = "health"
)

// progressTokenPattern bounds what a token may be. It is echoed back inside
// a JSON frame, never interpreted, so the bound is about keeping it a
// short opaque identifier rather than about escaping.
var progressTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// readProgressFrame is one read_progress frame: how far one in-flight read
// has got. Index/Total count mods across the whole read (for the update
// check, across every source it asked - core.UpdateCheckEvent's Global
// pair), and ModName is the mod just reached.
type readProgressFrame struct {
	Token   string `json:"token"`
	Read    string `json:"read"`
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	ModName string `json:"mod_name,omitempty"`
	Index   int    `json:"index"`
	Total   int    `json:"total"`
	// Notice is what the read is WAITING on, in core's sentence (#671): a
	// source waiting out a rate limit ("Rate limited by NexusMods; retrying
	// in 30s (attempt 2 of 3).") or refusing to be asked until a stated
	// time. A frame carrying one repeats the read's position; the next
	// tick, which carries none, ends it. omitempty.
	Notice string `json:"notice,omitempty"`
}

// readProgressGate caps how many frames one read publishes: a frame when the
// read's whole percent moves, plus its first and last tick - at most 101
// for any number of mods. A thousand-mod check publishing every tick could
// otherwise overrun a browser's activity buffer (activityWatcherBuffer) and
// get its whole session stream dropped over a progress bar.
type readProgressGate struct {
	seen bool
	last int
}

// admit reports whether tick index of total should be published.
func (g *readProgressGate) admit(index, total int) bool {
	if total <= 0 {
		return false
	}
	pct := index * 100 / total
	if g.seen && pct == g.last && index != total {
		return false
	}
	g.seen, g.last = true, pct
	return true
}

// progressToken reads ?progress=: ""/true when absent, the token/true when
// well-formed, or ""/false (the 400 envelope already written) when not.
func (s *Server) progressToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	token := r.URL.Query().Get("progress")
	if token == "" {
		return "", true
	}
	if !progressTokenPattern.MatchString(token) {
		s.writeAPIError(w, http.StatusBadRequest, fmt.Errorf(
			`invalid query parameter "progress": want 1-64 letters, digits, '-' or '_'`))
		return "", false
	}
	return token, true
}

// readReporter is one read's progress reporting when the caller named a
// token: the per-mod ticks, through the gate, and what a source says the
// read is waiting on (#671). Notices arrive on whichever goroutine is
// waiting, so the read's position is kept under a lock.
type readReporter struct {
	s     *Server
	token string
	read  string
	sel   selection

	mu    sync.Mutex
	gate  readProgressGate
	index int
	total int
	name  string
	// noticed reports that the last frame carried a notice: the next tick
	// is published whatever the gate says, so the notice is not left
	// standing for a read that has moved on.
	noticed bool
}

// newReadReporter returns the reporter for a read that named token, or nil
// when it named none - core's own "nobody is watching".
func (s *Server) newReadReporter(token, read string, sel selection) *readReporter {
	if token == "" {
		return nil
	}
	return &readReporter{s: s, token: token, read: read, sel: sel}
}

// sink is the core.EventSink the read hands its engine: it projects each
// per-mod tick onto a readProgressFrame and publishes it, through the gate,
// on the activity bus. Every other event the engine emits is ignored. nil
// for a nil reporter.
func (r *readReporter) sink() core.EventSink {
	if r == nil {
		return nil
	}
	return func(e core.Event) {
		switch ev := e.(type) {
		case core.UpdateCheckEvent:
			r.tick(ev.ModName, ev.GlobalIndex, ev.GlobalTotal)
		case core.VerifyEvent:
			if ev.Kind == core.VerifyEvProgress {
				r.tick(ev.ModName, ev.Index, ev.Total)
			}
		}
	}
}

// watch returns ctx with the read's source notices published as frames
// (#671): the Health card's verify and the update check are plain reads,
// with no job for a throttle to be said on. ctx unchanged for a nil
// reporter.
func (r *readReporter) watch(ctx context.Context) context.Context {
	if r == nil {
		return ctx
	}
	return core.WithSourceNotices(ctx, func(e core.Event) {
		if w, ok := e.(core.WarningEvent); ok {
			r.notice(w.Message)
		}
	})
}

func (r *readReporter) tick(name string, index, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name != "" {
		r.name = name
	}
	r.index, r.total = index, total
	if !r.gate.admit(index, total) && !r.noticed {
		return
	}
	r.noticed = false
	r.publishLocked("")
}

func (r *readReporter) notice(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.noticed = true
	r.publishLocked(text)
}

func (r *readReporter) publishLocked(notice string) {
	r.s.jobs.publish(activityEvent{Name: activityReadProgressEvent, Payload: readProgressFrame{
		Token: r.token, Read: r.read, GameID: r.sel.Game.ID, Profile: r.sel.Profile,
		ModName: r.name, Index: r.index, Total: r.total, Notice: notice,
	}})
}

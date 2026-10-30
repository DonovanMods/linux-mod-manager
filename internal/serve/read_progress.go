package serve

import (
	"fmt"
	"net/http"
	"regexp"

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

// readProgressSink is the core.EventSink a read hands its engine when the
// caller named a token: it projects each per-mod tick onto a
// readProgressFrame and publishes it, through the gate, on the activity
// bus. Every other event the engine emits is ignored. nil when token is
// empty, which is core's own "nobody is watching".
func (s *Server) readProgressSink(token, read string, sel selection) core.EventSink {
	if token == "" {
		return nil
	}
	var gate readProgressGate
	publish := func(name string, index, total int) {
		if !gate.admit(index, total) {
			return
		}
		s.jobs.publish(activityEvent{Name: activityReadProgressEvent, Payload: readProgressFrame{
			Token: token, Read: read, GameID: sel.Game.ID, Profile: sel.Profile,
			ModName: name, Index: index, Total: total,
		}})
	}
	return func(e core.Event) {
		switch ev := e.(type) {
		case core.UpdateCheckEvent:
			publish(ev.ModName, ev.GlobalIndex, ev.GlobalTotal)
		case core.VerifyEvent:
			if ev.Kind == core.VerifyEvProgress {
				publish(ev.ModName, ev.Index, ev.Total)
			}
		}
	}
}

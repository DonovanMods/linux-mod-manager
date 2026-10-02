package db

import (
	"strings"
	"time"
)

// storedTimeLayout is the one format lmm writes a Go time in: UTC, RFC 3339,
// with the fraction always nine digits wide. The fixed width is what makes the
// text sort as the instant does - RFC3339Nano trims trailing zeros, and
// "…:05Z" sorts after "…:05.5Z" ('Z' > '.'), so a whole second would land
// after the half second that follows it.
//
// It exists because the modernc.org/sqlite driver, handed a bare time.Time,
// binds time.Time.String() (its default write format): local-zone wall-clock
// text with the monotonic reading appended ("… -0400 EDT m=+1.598"), which
// neither sorts across zones or DST nor means anything once stored (#515).
// Binding the string below instead keeps the driver out of the format.
const storedTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// formatTime is the text bound for a time stored in a DATETIME column.
func formatTime(t time.Time) string {
	return t.UTC().Format(storedTimeLayout)
}

// legacyZonedLayout is time.Time.String() without its trailing zone
// abbreviation, which carries nothing the numeric offset before it does not
// (and which Go cannot resolve for most abbreviations anyway).
const legacyZonedLayout = "2006-01-02 15:04:05.999999999 -0700"

// storedTimeLayouts are the other forms a DATETIME column can hold, tried in
// order after the legacy String() form: the new layout (any fraction width),
// the modernc driver's own formats, and SQLite's CURRENT_TIMESTAMP.
var storedTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
}

// parseStoredTime reads a time out of a DATETIME column's text, in any form
// the column has held: the current one, time.Time.String() with or without
// its monotonic "m=±…" suffix (every row written before #515), the driver's
// formats, and CURRENT_TIMESTAMP's. The result is UTC. ok is false for text
// that is none of those.
func parseStoredTime(s string) (t time.Time, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// A monotonic suffix is " m=+1.598230846" or " m=-0.000123", always last.
	if i := strings.Index(s, " m="); i > 0 {
		s = s[:i]
	}
	if f := strings.Fields(s); len(f) >= 3 {
		if t, err := time.Parse(legacyZonedLayout, strings.Join(f[:3], " ")); err == nil {
			return t.UTC(), true
		}
	}
	for _, layout := range storedTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// clock is the time a write is stamped with.
func (d *DB) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

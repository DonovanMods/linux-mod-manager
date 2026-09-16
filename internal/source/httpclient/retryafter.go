// Package httpclient: this file reads a Retry-After header (#436).
package httpclient

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryAfter reads a Retry-After value in either RFC 9110 form -
// delay-seconds, or an HTTP-date judged against now - and returns the wait
// it names, never more than ceiling. Anything empty, unparseable, negative
// or already past is 0: the caller's own backoff decides.
//
// The ceiling applies BEFORE the value becomes a time.Duration (T3 review
// F8), in both forms: a delay-seconds past it - including one with more
// digits than an int64 holds, which strconv refuses rather than wraps - is
// the ceiling, not "no hint"; and an HTTP-date centuries away, whose
// distance time.Time.Sub saturates, is the ceiling too.
func RetryAfter(v string, now time.Time, ceiling time.Duration) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if allDigits(v) {
		secs, err := strconv.ParseInt(v, 10, 64)
		if err != nil || secs > int64(ceiling/time.Second) {
			return ceiling
		}
		return time.Duration(secs) * time.Second
	}
	at, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	return min(max(at.Sub(now), 0), ceiling)
}

// allDigits reports whether v is RFC 9110's delay-seconds: 1*DIGIT.
func allDigits(v string) bool {
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return v != ""
}

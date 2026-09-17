package main

import (
	"fmt"
	"time"
)

// cliNow is the clock displayAge measures against; a test pins it.
var cliNow = time.Now

// displayAge renders when a mod or file was last updated the way the search
// and install surfaces show it (#433) - the twin of the SPA's
// relativetime.js: an age while it is recent enough to read as one ("3h
// ago", "2d ago"), and past a week the plain date, because "23 days ago" is
// a number to decode and a date is a fact. The date is UTC, as every other
// date this CLI prints for a mod (workshopRevisionDate).
//
// Empty for the zero time - a source that reports no date shows none, and
// never 0001-01-01. A moment slightly in the future (clock skew between
// lmm and the source) reads "just now".
func displayAge(at, now time.Time) string {
	if at.IsZero() || at.Unix() <= 0 {
		return ""
	}
	elapsed := now.Sub(at)
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed/time.Minute))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed/time.Hour))
	case elapsed < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(elapsed/(24*time.Hour)))
	default:
		return at.UTC().Format("2006-01-02")
	}
}

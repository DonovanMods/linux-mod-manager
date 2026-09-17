package main

import (
	"testing"
	"time"
)

// TestDisplayAge pins the search/install surfaces' date rule (#433): an
// age while recent, the UTC date past a week, and nothing at all for a
// source that reports no date.
func TestDisplayAge(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"undated", time.Time{}, ""},
		{"unix epoch", time.Unix(0, 0), ""},
		{"clock skew", now.Add(30 * time.Second), "just now"},
		{"seconds", now.Add(-10 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5m ago"},
		{"hours", now.Add(-3 * time.Hour), "3h ago"},
		{"days", now.Add(-50 * time.Hour), "2d ago"},
		{"a week", now.Add(-7 * 24 * time.Hour), "2026-09-10"},
		{"old, in another zone", time.Date(2025, 1, 2, 23, 30, 0, 0, time.FixedZone("x", -5*3600)), "2025-01-03"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayAge(tc.at, now); got != tc.want {
				t.Errorf("displayAge(%v) = %q, want %q", tc.at, got, tc.want)
			}
		})
	}
}

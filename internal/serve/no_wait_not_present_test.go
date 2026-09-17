package serve_test

// The wait-spelling ratchet for the browser E2E suite (#439, #486).
//
// chromedp.WaitNotPresent tracks nodes through chromedp's own DOM mirror,
// and it does not reliably notice a removal that happens AFTER the action
// that preceded it - a modal closed once `await startJob(...)` resolves, a
// slide-over held for one exit animation, a card dropped by a re-read. In
// this SPA essentially every removal is that shape: Preact renders on a
// microtask at the earliest, and most closes wait on a network round trip.
// A missed removal does not fail fast; it sits out the harness's whole
// e2eTimeout, which is how two tests came to flake ~10% of the time. Every
// such wait is spelled waitGone (a timer-driven pollUntil on the page's own
// querySelector) instead, and this test keeps it that way.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var waitNotPresentCall = regexp.MustCompile(`\bchromedp\.WaitNotPresent\s*\(`)

// TestE2EWaitsForARemovalWithWaitGone fails on any chromedp.WaitNotPresent
// call in this package's Go files; comments that name it are fine.
func TestE2EWaitsForARemovalWithWaitGone(t *testing.T) {
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range matches {
		if filepath.Base(path) == "no_wait_not_present_test.go" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if waitNotPresentCall.MatchString(code) {
				t.Errorf("%s:%d: chromedp.WaitNotPresent misses an asynchronous removal and burns the whole e2eTimeout; use waitGone(sel)", path, i+1)
			}
		}
	}
}

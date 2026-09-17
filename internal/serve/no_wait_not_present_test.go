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
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// waitNotPresentLines returns the 1-based lines of src that use
// chromedp.WaitNotPresent: any identifier token of that name, so a call, an
// alias or a selector split across lines all count, while a comment or a
// string that merely names it is not a token at all.
func waitNotPresentLines(src []byte) []int {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0) // mode 0: comments are skipped
	var lines []int
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return lines
		}
		if tok == token.IDENT && lit == "WaitNotPresent" {
			lines = append(lines, fset.Position(pos).Line)
		}
	}
}

// TestE2EWaitsForARemovalWithWaitGone fails on any use of
// chromedp.WaitNotPresent in this package's Go files; comments and strings
// that name it are fine.
func TestE2EWaitsForARemovalWithWaitGone(t *testing.T) {
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range waitNotPresentLines(data) {
			t.Errorf("%s:%d: chromedp.WaitNotPresent misses an asynchronous removal and burns the whole e2eTimeout; use waitGone(sel)", path, line)
		}
	}
}

// TestWaitNotPresentLines_FindsEveryUse pins the ratchet's scanner: a use
// hidden behind a "//" inside a string, or behind an alias, is still a use;
// a mention in a comment or a string is not.
func TestWaitNotPresentLines_FindsEveryUse(t *testing.T) {
	const head = "package p\n\nimport \"github.com/chromedp/chromedp\"\n\n"
	cases := []struct {
		name, body string
		want       []int
	}{
		{"a call", "var a = chromedp.WaitNotPresent(`x`)\n", []int{5}},
		{"a call after a URL string", "var a, b = \"http://x\", chromedp.WaitNotPresent(`x`)\n", []int{5}},
		{"an alias", "var wait = chromedp.WaitNotPresent\n", []int{5}},
		{"a spaced selector", "var a = chromedp.\n\tWaitNotPresent(`x`)\n", []int{6}},
		{"a line comment", "// chromedp.WaitNotPresent(`x`) is banned\nvar a = 1\n", nil},
		{"a block comment", "/* chromedp.WaitNotPresent(`x`) */\nvar a = 1\n", nil},
		{"a string", "var a = \"chromedp.WaitNotPresent(x)\"\n", nil},
		{"a raw string", "var a = `chromedp.WaitNotPresent(x)`\n", nil},
		{"waitGone", "var a = waitGone(`x`)\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := waitNotPresentLines([]byte(head + c.body)); !slices.Equal(got, c.want) {
				t.Errorf("waitNotPresentLines = %v, want %v", got, c.want)
			}
		})
	}
}

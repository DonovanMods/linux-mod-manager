package main

import (
	"fmt"
	"sync/atomic"
)

// progressLineOpen reports that a "\r"-redrawn progress line is on stdout
// with no newline after it yet.
//
// A source notice goes to stderr, and on a terminal both streams share one
// screen: a notice printed while a download bar was open landed on the end
// of the bar's line (T3 review F12). printSourceNotice ends the line first.
var progressLineOpen atomic.Bool

// printProgressLine redraws a progress line on stdout, and records that it
// is open.
func printProgressLine(format string, args ...any) {
	fmt.Printf(format, args...)
	progressLineOpen.Store(true)
}

// finishProgressLine ends a progress line the way its caller always has -
// with a newline, whether or not a bar was drawn - and records that no line
// is open.
func finishProgressLine() {
	fmt.Println()
	progressLineOpen.Store(false)
}

// endProgressLine ends an open progress line, and prints nothing when none
// is open.
func endProgressLine() {
	if progressLineOpen.Swap(false) {
		fmt.Println()
	}
}

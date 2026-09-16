package main

import (
	"context"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// withSourceNotices returns ctx with every notice a source or a download
// raises under it printed to STDERR (#436): a throttled request and its
// wait, a host lmm is refusing to ask until a stated time, a one-time index
// build. They are what a command is WAITING on, and a command that waits
// silently reads as hung.
//
// Stderr, and under --json too: the document on stdout stays the only
// thing there (the `update --json` invariant), and a caller watching a
// scripted run still sees why it is slow.
//
// withServiceOpts installs it for every command, so a notice reaches the
// terminal from whichever command triggered it - `lmm import`'s scan-mode
// matching building a cold Thunderstore index gets the same line
// `lmm search` does (T1 review #8) without a line of its own. Execute
// installs it on the root context as well (T3 review F2), so a call made on
// cmd.Context() - which `lmm import`'s scan and `lmm verify --fix` both
// were - is not silent either.
func withSourceNotices(ctx context.Context) context.Context {
	return core.WithSourceNotices(ctx, printSourceNotice)
}

// printSourceNotice writes one notice event's sentence. os.Stderr is read
// at call time, so a test that swaps it captures the line.
func printSourceNotice(e core.Event) {
	endProgressLine()
	switch ev := e.(type) {
	case core.WarningEvent:
		fmt.Fprintln(os.Stderr, ev.Message)
	case core.StepEvent:
		fmt.Fprintln(os.Stderr, ev.Detail)
	}
}

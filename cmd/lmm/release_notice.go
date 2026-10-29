package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// release_notice.go - the one line after an interactive command that says a
// newer lmm exists (#568).
//
// The check runs BESIDE the command, not after it: PersistentPreRunE starts
// it in a goroutine and Execute collects it once the command is done,
// waiting at most releaseNoticeGrace for an answer still in flight. Almost
// every command outlasts the request, so the usual cost is nothing; the
// worst case, once a day, is releaseNoticeGrace. A check that has not
// answered by then is abandoned - core records the attempt before it asks,
// so an abandoned check still counts as the day's one request rather than
// being retried by every following command. The alternative, refreshing
// only from `lmm serve` and `lmm version --check`, would leave a CLI-only
// user never hearing about a release, which is the case the notice exists
// for.

// releaseCheckArmed is set by Execute alone, so the release check - the
// after-command one here and `lmm serve`'s background refresh - runs only
// in the real binary: a test drives commands through runRoot or rootCmd
// directly and never arms it, which keeps the suite off the network
// whatever its stderr is. A test that wants a checker builds one against
// an httptest server.
var releaseCheckArmed bool

// stderrIsTerminal reports whether stderr is a terminal. The notice is for
// a person at a prompt; a script or a pipe gets neither the line nor the
// request behind it.
var stderrIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

const (
	// releaseNoticeDeadline bounds the background request.
	releaseNoticeDeadline = 3 * time.Second
	// releaseNoticeGrace is the most a finished command waits for it.
	releaseNoticeGrace = 300 * time.Millisecond
)

// pendingReleaseCheck is a started after-command check.
type pendingReleaseCheck struct {
	rc         *core.ReleaseChecker
	configPath string
	done       chan struct{}
}

// pendingRelease is the check startReleaseCheck began, if any.
var pendingRelease *pendingReleaseCheck

// releaseNoticeSkip names the top-level commands that never get the notice:
// `lmm version` reports the same facts itself, `lmm serve`'s output is a
// server's log and its own banner covers the web UI, and the rest are
// shell plumbing or help.
var releaseNoticeSkip = map[string]bool{
	"version":          true,
	"serve":            true,
	"completion":       true,
	"help":             true,
	"gen-man":          true,
	"__complete":       true,
	"__completeNoDesc": true,
}

// releaseNoticeWanted reports whether cmd is one the notice may follow.
func releaseNoticeWanted(cmd *cobra.Command) bool {
	if !cmd.HasParent() {
		return false // the root itself: `lmm` alone prints help
	}
	top := cmd
	for top.Parent().HasParent() {
		top = top.Parent()
	}
	return !releaseNoticeSkip[top.Name()]
}

// startReleaseCheck begins the after-command check for cmd when everything
// says a person will read the result: armed, an interactive stderr, no
// --json, a command the notice may follow, a release build, and the check
// not turned off in config.yaml.
func startReleaseCheck(cmd *cobra.Command) {
	if !releaseCheckArmed || jsonOutput || !stderrIsTerminal() || isDevBuild() || !releaseNoticeWanted(cmd) {
		return
	}
	rc, configPath, err := newReleaseChecker()
	if err != nil || !rc.Enabled() {
		return
	}
	p := &pendingReleaseCheck{rc: rc, configPath: configPath, done: make(chan struct{})}
	pendingRelease = p
	go func() {
		defer close(p.done)
		ctx, cancel := context.WithTimeout(context.Background(), releaseNoticeDeadline)
		defer cancel()
		_ = rc.Refresh(ctx) // a failure is cached; a notice never fails a command
	}()
}

// finishReleaseCheck collects the check startReleaseCheck began and writes
// the notice to w: the one-time "lmm checks GitHub" line the first time,
// then the update line whenever the cache knows of a newer release.
func finishReleaseCheck(w io.Writer) {
	p := pendingRelease
	if p == nil {
		return
	}
	pendingRelease = nil
	select {
	case <-p.done:
	case <-time.After(releaseNoticeGrace):
	}
	if p.rc.FirstRunNoticeDue() {
		_, _ = fmt.Fprintf(w, "lmm checks GitHub for a new release once a day; to turn this off, set update_check: false in %s\n", p.configPath)
		_ = p.rc.RecordFirstRunNotice()
	}
	if st, ok := p.rc.PendingUpdate(context.Background()); ok {
		_, _ = fmt.Fprintln(w, releaseNoticeLine(st))
	}
}

// releaseNoticeLine is the update line: what is available, and how to get
// it.
func releaseNoticeLine(st core.ReleaseStatus) string {
	line := fmt.Sprintf("lmm %s is available (you have %s)", st.Latest, st.Current)
	switch {
	case st.Install.UpgradeCommand != "":
		return line + "; upgrade with: " + st.Install.UpgradeCommand
	case st.ReleaseURL != "":
		return line + ": " + st.ReleaseURL
	default:
		return line + "; run 'lmm version' for details"
	}
}

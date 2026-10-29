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
// waiting at most releaseNoticeGrace for an answer still in flight, and
// spending at most releaseNoticeBudget on the whole exit path - the wait
// and the notice's install-method lookup, which core normally answers from
// its cache without running a package manager (the background check
// detects it beside the command, so a slow first detection is cached by a
// command that outlasts it). Almost every command
// outlasts the request, so the usual cost is nothing; the worst case is
// releaseNoticeBudget. A check that has not answered by then is abandoned -
// core records the attempt before it asks, so an abandoned check holds off
// the following commands' attempts for a few minutes rather than each one
// asking. The alternative, refreshing only from `lmm serve` and `lmm
// version --check`, would leave a CLI-only user never hearing about a
// release, which is the case the notice exists for.

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
	// releaseNoticeBudget bounds the whole exit path: the grace, then the
	// notice itself.
	releaseNoticeBudget = 500 * time.Millisecond
)

// pendingReleaseCheck is a started after-command check.
type pendingReleaseCheck struct {
	rc     *core.ReleaseChecker
	done   chan struct{}
	cancel context.CancelFunc
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
	rc, err := newReleaseChecker()
	if err != nil || !rc.Enabled() {
		return
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, releaseNoticeDeadline)
	p := &pendingReleaseCheck{rc: rc, done: make(chan struct{}), cancel: cancel}
	pendingRelease = p
	go func() {
		defer close(p.done)
		_ = rc.Refresh(ctx) // a failure is cached; a notice never fails a command
		// Detect the install method here too, on this goroutine's deadline:
		// a cold dpkg -S or rpm -qf can outlast what is left of the exit
		// budget, and an answer cut short is never cached, so detection
		// only on the exit path might never finish. PendingUpdate asks a
		// package manager only when there is an update to word, and caches
		// what it learns for the exit path and every later run.
		_, _ = rc.PendingUpdate(ctx)
	}()
}

// finishReleaseCheck collects the check startReleaseCheck began and writes
// the notice to w: the one-time "lmm checks GitHub" line the first time,
// then the update line whenever the cache knows of a newer release. ctx is
// the command's, so an interrupt ends the wait; releaseNoticeBudget bounds
// it otherwise.
func finishReleaseCheck(ctx context.Context, w io.Writer) {
	p := pendingRelease
	if p == nil {
		return
	}
	pendingRelease = nil
	ctx, cancel := context.WithTimeout(ctx, releaseNoticeBudget)
	defer cancel()
	grace := time.NewTimer(releaseNoticeGrace)
	defer grace.Stop()
	select {
	case <-p.done:
	case <-grace.C:
	case <-ctx.Done():
	}
	p.cancel() // a request still out is abandoned, which core records as such
	if p.rc.FirstRunNoticeDue() {
		_, _ = fmt.Fprintln(w, p.rc.FirstRunNotice())
		_ = p.rc.RecordFirstRunNotice()
	}
	if st, ok := p.rc.PendingUpdate(ctx); ok {
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

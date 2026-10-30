package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
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
// spending at most releaseNoticeBudget on the whole exit path - the wait and
// the notice's install-method lookup, which core normally answers from its
// cache without running a package manager (the background check detects it
// beside the command, so a slow first detection is cached by a command that
// outlasts it). Almost every command outlasts the request, so the usual cost
// is nothing; the worst case is releaseNoticeBudget. A check that has not
// answered by then is abandoned - core records the attempt before it asks,
// so an abandoned check holds off the following commands' attempts for a few
// minutes rather than each one asking, and a run of them backs off to a day.
// The alternative, refreshing only from `lmm serve` and `lmm version
// --check`, would leave a CLI-only user never hearing about a release, which
// is the case the notice exists for.

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

// autoUpdateNoticeDue is set by startReleaseCheck for a command a person
// will read the after-command lines of, whether or not the release check
// itself runs: lmm-update.timer updates lmm with update_check off too, and
// its update is announced the same way (#586).
var autoUpdateNoticeDue bool

// releaseNoticeSkip names the top-level commands that never get the notice:
// `lmm version` and `lmm self-update` report the same facts themselves (and
// self-update may just have installed the release), `lmm serve`'s output is a
// server's log and its own banner covers the web UI, and the rest are
// shell plumbing or help.
var releaseNoticeSkip = map[string]bool{
	"version":          true,
	"self-update":      true,
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
//
// An unattended run - a script, a pipe, cron - gets none of it, and so
// never starts an automatic update either (#586): replacing lmm under a
// script that runs several commands in a row is a surprise nobody asked
// for, and lmm-update.timer is the unattended machine's way to update.
func startReleaseCheck(cmd *cobra.Command) {
	if !releaseCheckArmed || jsonOutput || !stderrIsTerminal() || !releaseNoticeWanted(cmd) {
		return
	}
	autoUpdateNoticeDue = true
	if isDevBuild() {
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
//
// Before either, it announces an automatic update made since the last
// command (#586); and when the cache knows of a newer release that may be
// installed automatically, it starts that update, detached, instead of
// telling the user how to.
func finishReleaseCheck(ctx context.Context, w io.Writer) {
	if autoUpdateNoticeDue {
		autoUpdateNoticeDue = false
		printAutoUpdateNotice(w)
	}
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
		if startAutoUpdate(st) {
			_, _ = fmt.Fprintf(w, "lmm %s is available (you have %s); lmm is installing it in the background\n", st.Latest, st.Current)
			return
		}
		_, _ = fmt.Fprintln(w, releaseNoticeLine(st))
	}
}

// printAutoUpdateNotice writes the line for an automatic update not yet
// announced, once: the first one says how to turn them off.
func printAutoUpdateNotice(w io.Writer) {
	cfg, err := getServiceConfig()
	if err != nil {
		return
	}
	rec, ok := cfg.PendingAutoUpdateNotice()
	if !ok {
		return
	}
	_, _ = fmt.Fprintln(w, rec.Notice())
	_ = cfg.MarkAutoUpdateNoticed()
}

// spawnAutoUpdate starts `exe args...` detached from this process and its
// terminal, its output appended to logPath, and does not wait for it. A
// var: TestMain replaces it, so no test ever starts a real updater.
var spawnAutoUpdate = startDetached

// startAutoUpdate starts `lmm self-update --auto` for st's release when
// it may install it - a self-managed install, auto_update on, and the
// release neither installed nor backed off nor already being installed
// (core.SelfUpdater.AutoUpdateDue) - and reports whether it did. The run
// is detached: this command exits as it would have, and the run takes the
// self-update lock and verifies the release exactly as `lmm self-update`
// does. A self-managed answer that detection only guessed is no risk:
// the run asks again, definitively, and steps aside for a package.
func startAutoUpdate(st core.ReleaseStatus) bool {
	if st.Install.Method != core.InstallSelfManaged {
		return false
	}
	cfg, err := getServiceConfig()
	if err != nil {
		return false
	}
	u, err := newSelfUpdater()
	if err != nil || !u.AutoUpdateDue(st.Latest) {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	_ = u.RecordAutoUpdateAttempt()
	args := []string{"self-update", "--auto", "--config", cfg.ConfigDir, "--data", cfg.DataDir}
	return spawnAutoUpdate(exe, args, filepath.Join(cfg.DataDir, core.AutoUpdateLogFile)) == nil
}

// autoUpdateLogMax is the size past which the automatic-update log starts
// over rather than growing: it holds the last few runs, which is what a
// person reading it after a failure needs.
const autoUpdateLogMax = 256 << 10

// startDetached is spawnAutoUpdate's real implementation: a new session
// (setsid), so a closed terminal or a Ctrl-C at the prompt does not reach
// it; stdin from /dev/null; stdout and stderr to the log.
func startDetached(exe string, args []string, logPath string) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if info, err := os.Stat(logPath); err == nil && info.Size() > autoUpdateLogMax {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, flags, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer func() { _ = devNull.Close() }()
	_, _ = fmt.Fprintf(logFile, "%s: lmm %s starting an automatic update\n", time.Now().Format(time.RFC3339), version)
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
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

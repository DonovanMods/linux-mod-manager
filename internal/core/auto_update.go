package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// auto_update.go - lmm updating itself (#586). auto_update is on by
// default, and the daily release check is the trigger: the CLI starts a
// detached `lmm self-update --auto` after a command whose check knows of a
// newer release, `lmm serve`'s background refresh runs RunAuto in-process,
// and lmm-update.timer (`lmm service install --auto-update`) remains for a
// machine where lmm rarely runs. Every one of them goes through RunAuto, so
// every automatic update takes the self-update lock, installs only through
// Plan and Apply's verified path, and leaves its outcome in
// <DataDir>/auto-update.json: the update the frontends announce once, and
// the failure that holds the same release off for a day.

// autoUpdateFile is the state file, under the data dir.
const autoUpdateFile = "auto-update.json"

// AutoUpdateLogFile is the file, under the data dir, a CLI-started
// automatic update writes its output to: it runs detached from the
// terminal, so nothing else would show why it failed.
const AutoUpdateLogFile = "auto-update.log"

// autoUpdateFailureBackoff is how long a release whose automatic update
// failed is left alone: a release that does not verify, or a binary in a
// directory this user cannot write, fails the same way on every command.
// A release check that fails for a reason that will not pass by itself - an
// install lmm cannot settle, a tag it refuses - waits as long.
const autoUpdateFailureBackoff = 24 * time.Hour

// autoUpdateCheckBackoff is the first wait after a release check that
// failed the way a network does - offline, GitHub erroring, a rate limit -
// doubling with each failure in a row up to autoUpdateFailureBackoff.
const autoUpdateCheckBackoff = time.Hour

// autoUpdateRestartWait is how long an automatic update waits for a running
// mutation before restarting lmm-serve.service, when the caller sets no
// AutoRunOptions.RestartWait: long enough for a large deploy, and once it
// passes the restart is left to the person (the web UI's banner asks).
const autoUpdateRestartWait = 10 * time.Minute

// autoUpdateRestartPoll is how often that wait looks again.
const autoUpdateRestartPoll = time.Second

// autoUpdateAttemptHold is how long a started automatic update holds off
// the next start, whatever became of it: one that is still downloading, or
// that died without recording anything, is not started again on every
// command.
const autoUpdateAttemptHold = time.Hour

// AutoUpdateRecord is an automatic update lmm made.
type AutoUpdateRecord struct {
	// From is the version it replaced.
	From string `json:"from"`
	// To is the version it installed.
	To string `json:"to"`
	// At is when.
	At time.Time `json:"at"`
	// First is true for the first automatic update from this data dir,
	// whose notice says how to turn them off.
	First bool `json:"first"`
}

// Notice is the line a frontend shows once after the update: the first
// says how to turn automatic updates off, later ones only what happened.
func (r AutoUpdateRecord) Notice() string {
	line := "lmm updated itself to v" + r.To + "."
	if r.First {
		line += " To turn this off: auto_update: false in config.yaml, or Setup > Updates."
	}
	return line
}

// AutoUpdateFailure is the last automatic update that failed.
type AutoUpdateFailure struct {
	// Version is the release it tried to install; empty when it failed
	// before it knew of one - the release check itself failed.
	Version string `json:"version,omitempty"`
	// At is when it failed.
	At time.Time `json:"at"`
	// Error is why.
	Error string `json:"error"`
	// RetryAt is when the next automatic update may run: for Version, or
	// for any release when Version is empty.
	RetryAt time.Time `json:"retry_at"`
}

// AutoUpdateStatus is what the web UI's Setup > Updates shows of automatic
// updates: GET /api/v1/updates/lmm's auto_update.
type AutoUpdateStatus struct {
	// Enabled is config.yaml's auto_update: true unless it says false.
	Enabled bool `json:"enabled"`
	// Applicable is false for an install lmm never updates itself: a
	// package's binary, or a development build.
	Applicable bool `json:"applicable"`
	// Reason says why it is not applicable; empty when it is.
	Reason string `json:"reason,omitempty"`
	// Last is the most recent automatic update, nil when there has been
	// none.
	Last *AutoUpdateRecord `json:"last,omitempty"`
	// Failure is the last automatic update that failed since the last that
	// succeeded; nil when there is none.
	Failure *AutoUpdateFailure `json:"failure,omitempty"`
	// Noticed is true once a frontend - the CLI's after-command line, or
	// the web UI's banner - has announced Last, so neither announces it
	// again.
	Noticed bool `json:"noticed"`
}

// AutoUpdateOutcome is what RunAuto did: an update, or the reason it
// stepped aside - turned off, a package's binary, another run or an lmm
// mutation under way, already up to date, a release backed off after a
// failure. A step aside is not an error: lmm-update.timer must not read one
// as a failure to retry.
type AutoUpdateOutcome struct {
	// Result is the update, Updated false when there was none.
	Result SelfUpdateResult
	// Skipped is why nothing was installed; empty when RunAuto got as far
	// as Apply.
	Skipped string
}

// autoUpdateState is the state file's document.
type autoUpdateState struct {
	// AttemptedAt is the last automatic update started, finished or not.
	AttemptedAt time.Time          `json:"attempted_at,omitzero"`
	Failure     *AutoUpdateFailure `json:"failure,omitempty"`
	Last        *AutoUpdateRecord  `json:"last,omitempty"`
	// Updates counts the automatic updates made, which makes the first
	// one First.
	Updates int `json:"updates,omitempty"`
	// Noticed is true once a frontend has shown Last's notice.
	Noticed bool `json:"noticed,omitempty"`
	// CheckFailures counts the release checks that failed in a row, which
	// doubles the next one's back-off.
	CheckFailures int `json:"check_failures,omitempty"`
}

// AutoRunOptions is what a frontend adds to RunAuto.
type AutoRunOptions struct {
	// Quiesce is `lmm serve`'s: it holds off new jobs in this process and
	// reports whether none is running, and resume lets them start again.
	// RunAuto asks it before it installs anything, resuming at once, and
	// before it restarts lmm-serve.service, resuming only when nothing was
	// restarted. nil (the CLI, the timer) is a process with no jobs of its
	// own; the cross-process mutation lock covers the rest.
	Quiesce func() (resume func(), idle bool)
	// RestartWait bounds how long the restart waits for running mutations;
	// zero is autoUpdateRestartWait.
	RestartWait time.Duration
}

// loadAutoUpdateState reads dataDir's state; a missing or unreadable file
// is an empty state.
func loadAutoUpdateState(dataDir string) autoUpdateState {
	var st autoUpdateState
	data, err := os.ReadFile(filepath.Join(dataDir, autoUpdateFile))
	if err != nil {
		return autoUpdateState{}
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return autoUpdateState{}
	}
	return st
}

// updateAutoUpdateState applies change to dataDir's state as it is now and
// writes it through a temp file and a rename, so a CLI command and `lmm
// serve` never read each other's half-written file.
func updateAutoUpdateState(dataDir string, change func(*autoUpdateState)) error {
	st := loadAutoUpdateState(dataDir)
	change(&st)
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, autoUpdateFile+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dataDir, autoUpdateFile)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// autoUpdateEnabled reads configDir's auto_update. A config.yaml lmm
// cannot read answers false: an unattended update needs a clear yes.
func autoUpdateEnabled(configDir string) bool {
	cfg, err := config.Load(configDir)
	return err == nil && cfg.AutoUpdateEnabled()
}

// now is the configured clock.
func (u *SelfUpdater) now() time.Time {
	if u.opts.Now != nil {
		return u.opts.Now()
	}
	return time.Now()
}

// AutoUpdateEnabled reports config.yaml's auto_update, which every
// automatic update obeys: on unless it says `auto_update: false`.
func (u *SelfUpdater) AutoUpdateEnabled() bool {
	return autoUpdateEnabled(u.opts.ConfigDir)
}

// AutoUpdateDue reports whether an automatic update to latest should start
// now - the question the CLI asks before it starts one, and `lmm serve`
// before it runs one: auto_update is on, latest is a release lmm has not
// already updated itself to, no automatic update to it failed in the last
// day, and none was started in the last hour. It asks nothing of GitHub
// and does not look at the install method; RunAuto refuses a binary it
// may not replace.
func (u *SelfUpdater) AutoUpdateDue(latest string) bool {
	if latest == "" || !u.AutoUpdateEnabled() {
		return false
	}
	st := loadAutoUpdateState(u.opts.DataDir)
	if within(st.AttemptedAt, u.now(), autoUpdateAttemptHold) {
		return false
	}
	return u.autoUpdateBlocked(st, latest) == ""
}

// autoUpdateBlocked is why an automatic update to latest must not run, by
// the state alone; "" when it may.
func (u *SelfUpdater) autoUpdateBlocked(st autoUpdateState, latest string) string {
	if st.Last != nil && st.Last.To == latest {
		return "lmm already updated itself to " + latest
	}
	if why := u.checkBackedOff(st); why != "" {
		return why
	}
	if f := st.Failure; f != nil && f.Version == latest && u.now().Before(f.RetryAt) {
		return fmt.Sprintf("the automatic update to %s failed at %s (%s); lmm tries it again after %s",
			latest, f.At.Local().Format("2006-01-02 15:04"), f.Error, f.RetryAt.Local().Format("2006-01-02 15:04"))
	}
	return ""
}

// checkBackedOff is why no automatic update may ask GitHub yet: the last
// release check failed and its back-off has not passed; "" when it may.
func (u *SelfUpdater) checkBackedOff(st autoUpdateState) string {
	if f := st.Failure; f != nil && f.Version == "" && u.now().Before(f.RetryAt) {
		return fmt.Sprintf("the automatic update's release check failed at %s (%s); lmm tries again after %s",
			f.At.Local().Format("2006-01-02 15:04"), f.Error, f.RetryAt.Local().Format("2006-01-02 15:04"))
	}
	return ""
}

// recordCheckFailure records a release check (Plan) that failed: a day's
// back-off for a failure that will not pass by itself, otherwise one that
// starts at an hour and doubles with each failure in a row.
func (u *SelfUpdater) recordCheckFailure(err error) {
	at := u.now()
	var unknown *InstallMethodUnknownError
	var tagErr *ReleaseTagError
	permanent := errors.As(err, &unknown) || errors.As(err, &tagErr)
	_ = updateAutoUpdateState(u.opts.DataDir, func(s *autoUpdateState) {
		s.CheckFailures++
		wait := autoUpdateFailureBackoff
		if !permanent {
			wait = autoUpdateCheckBackoff
			for i := 1; i < s.CheckFailures && wait < autoUpdateFailureBackoff; i++ {
				wait *= 2
			}
			wait = min(wait, autoUpdateFailureBackoff)
		}
		s.Failure = &AutoUpdateFailure{At: at, Error: err.Error(), RetryAt: at.Add(wait)}
	})
}

// mutationsIdle takes the cross-process mutation lock and asks
// opts.Quiesce, and reports whether both say no lmm mutation is running;
// release lets both go - the lock always, and Quiesce's hold unless
// keepHold says this process is about to be restarted. A lock that cannot
// even be opened answers not idle: nothing is installed or restarted on a
// guess.
func (u *SelfUpdater) mutationsIdle(ctx context.Context, opts AutoRunOptions) (release func(keepHold bool), idle bool) {
	var lock *opLock
	if u.opts.OpLockPath != "" {
		l, err := acquireOpLock(ctx, u.opts.OpLockPath, 0)
		if err != nil {
			return func(bool) {}, false
		}
		lock = l
	}
	resume := func() {}
	if opts.Quiesce != nil {
		r, ok := opts.Quiesce()
		if r != nil {
			resume = r
		}
		if !ok {
			resume()
			lock.release()
			return func(bool) {}, false
		}
	}
	return func(keepHold bool) {
		lock.release()
		if !keepHold {
			resume()
		}
	}, true
}

// errMutationRunning is the step aside, and the restart left undone, while
// an lmm mutation runs.
var errMutationRunning = fmt.Errorf("%w; the automatic update waits for it to finish", ErrOperationInProgress)

// restartServe restarts lmm-serve.service once no lmm mutation is running,
// waiting up to opts.RestartWait for one to finish and holding new ones off
// while it asks; past the wait it restarts nothing, and says so in res.
func (u *SelfUpdater) restartServe(ctx context.Context, opts AutoRunOptions, res *SelfUpdateResult) {
	wait := opts.RestartWait
	if wait <= 0 {
		wait = autoUpdateRestartWait
	}
	poll := min(autoUpdateRestartPoll, wait/4)
	deadline := time.Now().Add(wait)
	for {
		release, idle := u.mutationsIdle(ctx, opts)
		if idle {
			restarted, err := u.opts.Restarter.RestartServeIfActive(ctx)
			res.ServeRestarted = restarted && err == nil
			if err != nil {
				res.ServeRestartError = err.Error()
			}
			release(res.ServeRestarted)
			return
		}
		if !time.Now().Before(deadline) {
			res.ServeRestartError = fmt.Sprintf("an lmm operation was still running after %s, so lmm-serve.service was not restarted under it", wait)
			return
		}
		select {
		case <-ctx.Done():
			res.ServeRestartError = ctx.Err().Error()
			return
		case <-time.After(poll):
		}
	}
}

// RecordAutoUpdateAttempt records that an automatic update is starting,
// which holds off the next for an hour (AutoUpdateDue). The CLI records it
// before it starts the detached run, so the commands that follow do not
// each start one.
func (u *SelfUpdater) RecordAutoUpdateAttempt() error {
	now := u.now()
	return updateAutoUpdateState(u.opts.DataDir, func(s *autoUpdateState) { s.AttemptedAt = now })
}

// RunAuto is one automatic update: `lmm self-update --auto` (started by
// the CLI, or by lmm-update.timer) and `lmm serve`'s background run. It
// steps aside, with no error, when auto_update is off, another self-update
// holds the lock, an lmm mutation is running (the cross-process mutation
// lock, and opts.Quiesce in `lmm serve`), the binary is a package's or a
// development build, lmm is up to date, or the release is one it already
// installed or that is backed off after a failure. Otherwise it installs
// the release through the same verified path as `lmm self-update --yes`,
// records the update the frontends announce once, and only then restarts
// lmm-serve.service - which may end the very process running this - once
// no mutation is running (restartServe). A failure, of the install or of
// the release check before it, is recorded with a back-off; a run its
// context cancelled records none.
func (u *SelfUpdater) RunAuto(ctx context.Context, opts AutoRunOptions) (AutoUpdateOutcome, error) {
	out := AutoUpdateOutcome{Result: SelfUpdateResult{Previous: u.opts.Version, Version: u.opts.Version}}
	if !u.AutoUpdateEnabled() {
		out.Skipped = "auto_update is off in config.yaml"
		return out, nil
	}
	unlock, err := u.Lock()
	if errors.Is(err, ErrSelfUpdateRunning) {
		out.Skipped = err.Error()
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer unlock()
	if why := u.checkBackedOff(loadAutoUpdateState(u.opts.DataDir)); why != "" {
		out.Skipped = why
		return out, nil
	}
	release, idle := u.mutationsIdle(ctx, opts)
	release(false)
	if !idle {
		out.Skipped = errMutationRunning.Error()
		return out, nil
	}
	_ = u.RecordAutoUpdateAttempt()

	plan, err := u.Plan(ctx)
	var pkgErr *PackageManagedError
	if errors.As(err, &pkgErr) || errors.Is(err, ErrDevBuild) {
		out.Skipped = err.Error()
		return out, nil
	}
	if err != nil {
		if ctx.Err() == nil {
			u.recordCheckFailure(err)
		}
		return out, err
	}
	_ = updateAutoUpdateState(u.opts.DataDir, func(s *autoUpdateState) {
		s.CheckFailures = 0
		if s.Failure != nil && s.Failure.Version == "" {
			s.Failure = nil
		}
	})
	out.Result.Executable = plan.Executable
	if plan.UpToDate {
		out.Skipped = "lmm " + plan.Current + " is up to date"
		return out, nil
	}
	if why := u.autoUpdateBlocked(loadAutoUpdateState(u.opts.DataDir), plan.Latest); why != "" {
		out.Skipped = why
		return out, nil
	}

	res, err := u.install(ctx, plan)
	out.Result = res
	if err != nil {
		if ctx.Err() == nil {
			at := u.now()
			_ = updateAutoUpdateState(u.opts.DataDir, func(s *autoUpdateState) {
				s.Failure = &AutoUpdateFailure{Version: plan.Latest, At: at, Error: err.Error(), RetryAt: at.Add(autoUpdateFailureBackoff)}
			})
		}
		return out, err
	}
	at := u.now()
	_ = updateAutoUpdateState(u.opts.DataDir, func(s *autoUpdateState) {
		s.Last = &AutoUpdateRecord{From: res.Previous, To: res.Version, At: at, First: s.Updates == 0}
		s.Updates++
		s.Noticed = false
		s.Failure = nil
	})
	if u.opts.Restarter != nil {
		u.restartServe(ctx, opts, &out.Result)
	}
	return out, nil
}

// PendingAutoUpdateNotice is the automatic update the CLI has yet to
// announce, and false when there is none.
func (c ServiceConfig) PendingAutoUpdateNotice() (AutoUpdateRecord, bool) {
	st := loadAutoUpdateState(c.DataDir)
	if st.Last == nil || st.Noticed {
		return AutoUpdateRecord{}, false
	}
	return *st.Last, true
}

// MarkAutoUpdateNoticed records that a frontend announced the last
// automatic update, which no frontend announces again.
func (c ServiceConfig) MarkAutoUpdateNoticed() error {
	return updateAutoUpdateState(c.DataDir, func(s *autoUpdateState) { s.Noticed = true })
}

// AutoUpdateStatus reports automatic updates for a binary installed as
// install: whether they are on, whether they apply to it, and the last
// update and failure.
func (c ServiceConfig) AutoUpdateStatus(install InstallMethod) AutoUpdateStatus {
	st := loadAutoUpdateState(c.DataDir)
	out := AutoUpdateStatus{
		Enabled: autoUpdateEnabled(c.ConfigDir),
		Last:    st.Last,
		Failure: st.Failure,
		Noticed: st.Noticed,
	}
	switch install.Method {
	case InstallSelfManaged:
		out.Applicable = true
	case InstallPackage:
		out.Reason = fmt.Sprintf("lmm was installed by the %s package (%s), so your package manager updates it, never lmm itself: %s",
			install.Package, install.Manager, install.UpgradeCommand)
	case InstallDev:
		out.Reason = "this is a development build of lmm, which has no release to update to; rebuild it from source, or install a release"
	default:
		out.Reason = "lmm has not yet determined how it was installed"
	}
	return out
}

// SetAutoUpdate writes config.yaml's auto_update, keeping every other
// setting as it is.
func (c ServiceConfig) SetAutoUpdate(ctx context.Context, enabled bool) error {
	return saveAutoUpdate(c.ConfigDir, enabled)
}

// AutoUpdateStatus is ServiceConfig.AutoUpdateStatus for the checker's
// directories - `lmm serve`'s GET /api/v1/updates/lmm, beside the release
// status it reads from the same data dir.
func (r *ReleaseChecker) AutoUpdateStatus(install InstallMethod) AutoUpdateStatus {
	return ServiceConfig{ConfigDir: r.opts.ConfigDir, DataDir: r.opts.DataDir}.AutoUpdateStatus(install)
}

// MarkAutoUpdateNoticed is ServiceConfig.MarkAutoUpdateNoticed for the
// checker's data dir: the web UI showed the notice (POST
// /api/v1/updates/lmm/notice), so the CLI does not repeat it.
func (r *ReleaseChecker) MarkAutoUpdateNoticed() error {
	return ServiceConfig{ConfigDir: r.opts.ConfigDir, DataDir: r.opts.DataDir}.MarkAutoUpdateNoticed()
}

// SetAutoUpdate writes config.yaml's auto_update - the web UI's "Update
// automatically" switch. It is a config.yaml write, not a mutation of
// anything a deploy touches, so it takes no mutation slot and never waits
// behind a running job; configWriteMu keeps it from losing a concurrent
// config.yaml write in this process.
func (s *Service) SetAutoUpdate(ctx context.Context, enabled bool) error {
	return saveAutoUpdate(s.configDir, enabled)
}

// configWriteMu serializes this process's read-modify-writes of
// config.yaml, which are not all behind the mutation slot.
var configWriteMu sync.Mutex

// saveAutoUpdate writes auto_update into configDir's config.yaml.
func saveAutoUpdate(configDir string, enabled bool) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	cfg, err := config.Load(configDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	cfg.AutoUpdate = &enabled
	if err := cfg.Save(configDir); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	return nil
}

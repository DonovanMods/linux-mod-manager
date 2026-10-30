package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
const autoUpdateFailureBackoff = 24 * time.Hour

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
	// Version is the release it tried to install.
	Version string `json:"version"`
	// At is when it failed.
	At time.Time `json:"at"`
	// Error is why.
	Error string `json:"error"`
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
}

// AutoUpdateOutcome is what RunAuto did: an update, or the reason it
// stepped aside - turned off, a package's binary, another run under way,
// already up to date, a release backed off after a failure. A step aside
// is not an error: lmm-update.timer must not read one as a failure to
// retry.
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
	// Noticed is true once the CLI has shown Last's notice.
	Noticed bool `json:"noticed,omitempty"`
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
	if f := st.Failure; f != nil && f.Version == latest && within(f.At, u.now(), autoUpdateFailureBackoff) {
		return fmt.Sprintf("the automatic update to %s failed at %s (%s); lmm tries it again a day later",
			latest, f.At.Local().Format("2006-01-02 15:04"), f.Error)
	}
	return ""
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
// holds the lock, the binary is a package's or a development build, lmm is
// up to date, or the release is one it already installed or that failed in
// the last day. Otherwise it installs the release exactly as `lmm
// self-update --yes` does and records the outcome: an update the
// frontends announce once, or a failure that backs the release off. A run
// its context cancelled records no failure.
func (u *SelfUpdater) RunAuto(ctx context.Context) (AutoUpdateOutcome, error) {
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
	_ = u.RecordAutoUpdateAttempt()

	plan, err := u.Plan(ctx)
	var pkgErr *PackageManagedError
	if errors.As(err, &pkgErr) || errors.Is(err, ErrDevBuild) {
		out.Skipped = err.Error()
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Result.Executable = plan.Executable
	if plan.UpToDate {
		out.Skipped = "lmm " + plan.Current + " is up to date"
		return out, nil
	}
	if why := u.autoUpdateBlocked(loadAutoUpdateState(u.opts.DataDir), plan.Latest); why != "" {
		out.Skipped = why
		return out, nil
	}

	res, err := u.Apply(ctx, plan)
	out.Result = res
	if err != nil {
		if ctx.Err() == nil {
			at := u.now()
			_ = updateAutoUpdateState(u.opts.DataDir, func(s *autoUpdateState) {
				s.Failure = &AutoUpdateFailure{Version: plan.Latest, At: at, Error: err.Error()}
			})
		}
		return out, err
	}
	if res.Updated {
		at := u.now()
		_ = updateAutoUpdateState(u.opts.DataDir, func(s *autoUpdateState) {
			s.Last = &AutoUpdateRecord{From: res.Previous, To: res.Version, At: at, First: s.Updates == 0}
			s.Updates++
			s.Noticed = false
			s.Failure = nil
		})
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

// MarkAutoUpdateNoticed records that the CLI announced the last automatic
// update.
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

// SetAutoUpdate writes config.yaml's auto_update - the web UI's "Update
// automatically" switch - serialized with every other mutation.
func (s *Service) SetAutoUpdate(ctx context.Context, enabled bool) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return saveAutoUpdate(s.configDir, enabled)
}

// saveAutoUpdate writes auto_update into configDir's config.yaml.
func saveAutoUpdate(configDir string, enabled bool) error {
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

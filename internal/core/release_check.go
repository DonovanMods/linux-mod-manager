package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// release_check.go - "is there a newer lmm?" (#568). Both frontends read
// the answer from here: `lmm version`, the CLI's after-command notice, and
// `lmm serve`'s GET /api/v1/updates/lmm and its banner.
//
// The check reads GitHub's public "latest release" document, at most once
// a day, sending nothing but an lmm/<version> User-Agent. Its answer - and
// a failure, which is an answer too - is cached in the data dir, so the
// 24-hour rule holds across processes and a broken network is asked once a
// day rather than on every command. An attempt that never finished (its
// caller exited, or gave up) is no answer: it holds off another for only
// releaseRetryAfter, backing off to a day as such attempts repeat (a
// network that drops the request rather than refusing it). A user turns it off with `update_check: false` in
// config.yaml.

// GitHubAPIBase is the production ReleaseCheckOptions.APIBase. It is not a
// default: NewReleaseChecker refuses an empty APIBase, so nothing - a test
// least of all - reaches GitHub without naming it.
const GitHubAPIBase = "https://api.github.com"

// ReleaseCheckInterval is how long a check's answer stands before the next
// unforced check asks GitHub again.
const ReleaseCheckInterval = 24 * time.Hour

// releaseRetryAfter is how long an attempt that did not finish - abandoned
// by a CLI command that exited first, or still in flight - holds off the
// next: long enough that a burst of commands asks once, short enough that a
// user whose commands are all quick still hears about a release that day.
const releaseRetryAfter = 10 * time.Minute

// releaseRetryBackoff is how long an unfinished attempt holds off the next
// when it is the nth in a row without an answer: releaseRetryAfter for the
// first, an hour for the second, then a day. A network that drops the
// request (a blackhole, a captive portal that hangs) never answers and
// never fails, so without it every attempt would cost a CLI user the exit
// wait, every releaseRetryAfter, for good.
func releaseRetryBackoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return releaseRetryAfter
	case attempts == 2:
		return time.Hour
	default:
		return ReleaseCheckInterval
	}
}

// releaseCheckTimeout bounds one request when the caller's context sets no
// tighter deadline. The CLI's after-command check sets a much tighter one.
const releaseCheckTimeout = 10 * time.Second

// releaseCheckMaxBytes caps the release document: it carries the release
// notes and the asset list, tens of KiB, never megabytes.
const releaseCheckMaxBytes = 1 << 20

// releaseLatestPath is GitHub's newest published release of lmm. GitHub
// never answers it with a draft or a prerelease; the checker refuses one
// anyway (see fetch), so a change on GitHub's side cannot surface one.
const releaseLatestPath = "/repos/DonovanMods/linux-mod-manager/releases/latest"

// releaseCheckFile is the cache, under the data dir.
const releaseCheckFile = "release-check.json"

// ReleaseStatus is what lmm knows about its own releases: `lmm version
// --json`'s document and GET /api/v1/updates/lmm's.
type ReleaseStatus struct {
	// Current is the running version, without any dev provenance.
	Current string `json:"current"`
	// Latest is the newest stable release's version ("2.1.0", no "v"),
	// empty until a check has found one.
	Latest string `json:"latest,omitempty"`
	// UpdateAvailable is Latest newer than Current, by semver. Never true
	// for a dev build.
	UpdateAvailable bool `json:"update_available"`
	// ReleaseURL is Latest's release page (its notes and downloads),
	// limited to http(s) by domain.SafeWebURL - when it is fetched, and
	// again when it is read back from the cache.
	ReleaseURL string `json:"release_url,omitempty"`
	// CheckedAt is when lmm last asked GitHub and heard back, an answer or
	// a failure; a check abandoned before either does not move it. Zero
	// when it never has.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// CheckError is why the last check failed; empty when it succeeded.
	// Latest and ReleaseURL keep what the last success learned.
	CheckError string `json:"check_error,omitempty"`
	// CheckEnabled is config.yaml's update_check: false when the user
	// turned the automatic check off.
	CheckEnabled bool `json:"check_enabled"`
	// Dev is a development build, which never reports an update.
	Dev bool `json:"dev"`
	// Install is how this binary was installed, which is how to upgrade it.
	// It is self-managed - the release page, never a wrong place to send
	// someone - when detection did not finish in the caller's time.
	Install InstallMethod `json:"install"`
	// Installed is the version of the lmm binary now on disk when it is
	// newer than Current: `lmm serve` sets it (InstalledNewer) once its
	// binary has been replaced under it, and is running the old one until
	// it restarts (#569). Empty otherwise, and always from the CLI.
	Installed string `json:"installed,omitempty"`
}

// ReleaseCheckOptions configures a ReleaseChecker.
type ReleaseCheckOptions struct {
	// ConfigDir holds config.yaml, whose update_check is read on each call.
	ConfigDir string
	// DataDir holds the cache. Required.
	DataDir string
	// Version is the running version, the bare release version
	// (cmd/lmm's `version`). Required.
	Version string
	// Dev marks a development build.
	Dev bool
	// APIBase is GitHub's API root - GitHubAPIBase in production, an
	// httptest server in a test. Required.
	APIBase string
	// HTTPClient carries the request; nil uses one bounded by
	// releaseCheckTimeout.
	HTTPClient *http.Client
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Probe answers how the binary was installed; nil is
	// DefaultInstallProbe.
	Probe *InstallProbe
}

// ReleaseChecker checks for, and remembers, newer lmm releases. Safe for
// concurrent use; the cache file is the state, so any number of checkers
// (one per CLI run, one per `lmm serve`) share it.
type ReleaseChecker struct {
	opts   ReleaseCheckOptions
	client *httpclient.Client

	mu sync.Mutex // serialises this process's reads and writes of the cache

	installMu    sync.Mutex    // guards the three fields below
	install      InstallMethod // the detected answer, once installKnown
	installKnown bool
	installDone  chan struct{} // closed when the detection under way ends; nil when none is

	diskMu      sync.Mutex // guards the fields below
	startedAs   time.Time  // the executable's mod time at RememberRunningBinary; zero when unknown
	diskModTime time.Time  // the mod time diskVersion was read at
	diskVersion string     // InstalledNewer's answer for diskModTime
	diskFailMod time.Time  // the mod time whose `--version` last failed
	diskFailAt  time.Time  // when it failed, which holds off another run for installedVersionBackoff
	diskRun     *diskRun   // the `--version` run under way; nil when none is
}

// diskRun is one `<exe> --version` run InstalledNewer's callers share.
type diskRun struct {
	modTime time.Time
	done    chan struct{} // closed once version is set
	version string
}

// releaseCheckState is the cache file's document.
type releaseCheckState struct {
	// CheckedAt is the last check that finished, with an answer or a
	// failure; Latest, URL and Error are what it learned.
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
	URL       string    `json:"url,omitempty"`
	Error     string    `json:"error,omitempty"`
	// AttemptedAt is a check that started and has not finished - in
	// flight, or abandoned - which holds off another for
	// releaseRetryBackoff(Attempts).
	AttemptedAt time.Time `json:"attempted_at,omitzero"`
	// Attempts counts the checks started since the last that finished,
	// AttemptedAt's among them. It is counted when a check starts, not
	// when it is abandoned: a CLI run may exit before it could record that.
	Attempts    int                `json:"attempts,omitempty"`
	NoticeShown bool               `json:"first_run_notice_shown,omitempty"`
	Install     *installCacheEntry `json:"install,omitempty"`
}

// installCacheEntry is a detected install method and the binary it is
// about: the resolved executable path and its modification time, so a
// binary replaced in place (an upgrade, a `make install`) is asked about
// afresh.
type installCacheEntry struct {
	Executable string        `json:"executable"`
	ModTime    time.Time     `json:"mod_time"`
	Method     InstallMethod `json:"method"`
}

// releaseAnswer is one finished check's answer: the latest stable release,
// or none, or skip - a "latest" that is not a stable release this check
// compares, which says nothing about the one lmm already knows.
type releaseAnswer struct {
	latest, url string
	skip        bool
}

// errNoRelease is GitHub's 404 for a repository with no published release.
var errNoRelease = errors.New("no published release")

// NewReleaseChecker returns a checker for opts. It panics on an empty
// APIBase, DataDir or Version - a programming error, and the APIBase rule
// is what keeps a test that forgot to set one off the network.
func NewReleaseChecker(opts ReleaseCheckOptions) *ReleaseChecker {
	if opts.APIBase == "" {
		panic("core.NewReleaseChecker: APIBase is required")
	}
	if opts.DataDir == "" || opts.Version == "" {
		panic("core.NewReleaseChecker: DataDir and Version are required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: releaseCheckTimeout}
	}
	r := &ReleaseChecker{
		opts: opts,
		client: httpclient.New(httpclient.Options{
			HTTPClient:       hc,
			BaseURL:          strings.TrimSuffix(opts.APIBase, "/"),
			Anonymous:        true,
			AuthLabel:        "GitHub",
			UserAgent:        "lmm/" + opts.Version,
			MaxResponseBytes: releaseCheckMaxBytes,
			ErrorMapper: func(status int, _ []byte, _ string) error {
				if status == http.StatusNotFound {
					return errNoRelease
				}
				return fmt.Errorf("GitHub answered %d %s", status, http.StatusText(status))
			},
		}),
	}
	return r
}

// RememberRunningBinary records the running executable's modification time,
// which InstalledNewer compares against. `lmm serve` calls it as it starts;
// a checker that never does answers InstalledNewer with "". Nothing else
// pays for it: the CLI's checks never touch the binary.
func (r *ReleaseChecker) RememberRunningBinary() {
	if r.opts.Dev {
		return
	}
	t, _ := r.executableModTime()
	r.diskMu.Lock()
	r.startedAs = t
	r.diskMu.Unlock()
}

// executableModTime is the running executable's path, symlinks resolved,
// and the modification time of the file there now.
func (r *ReleaseChecker) executableModTime() (time.Time, string) {
	probe := DefaultInstallProbe()
	if r.opts.Probe != nil {
		probe = *r.opts.Probe
	}
	exe, err := probe.Executable()
	if err != nil || exe == "" {
		return time.Time{}, ""
	}
	modTime := probe.ModTime
	if modTime == nil {
		modTime = fileModTime
	}
	t, err := modTime(exe)
	if err != nil {
		return time.Time{}, ""
	}
	return t, exe
}

// installedVersionTimeout bounds `<exe> --version`.
const installedVersionTimeout = 5 * time.Second

// installedVersionBackoff is how long a failed `<exe> --version` is
// remembered for the file it was about, so a binary that hangs or fails is
// not run again on every page load.
const installedVersionBackoff = 5 * time.Minute

// InstalledNewer is the version of the lmm binary now at the running
// executable's path when it is newer than the running one, and "" otherwise
// - what tells a `lmm serve` started by hand that `lmm self-update` (or a
// package upgrade, or `make install`) has replaced its binary (#569). It
// runs `<exe> --version` only once the file's modification time differs
// from RememberRunningBinary's, and once per such change; until then it
// runs nothing. A dev build, on either side, never answers.
//
// The run holds no lock: concurrent callers share the one under way, each
// returning "" once its own ctx ends, and the run itself is bounded only by
// installedVersionTimeout. A run that fails is not repeated for the same
// file until installedVersionBackoff has passed.
//
// The file is asked rather than a marker `lmm self-update` leaves behind,
// because every way of replacing the binary changes the file and only one
// of them would write the marker.
func (r *ReleaseChecker) InstalledNewer(ctx context.Context) string {
	if r.opts.Dev {
		return ""
	}
	mtime, exe := r.executableModTime()
	r.diskMu.Lock()
	if r.startedAs.IsZero() || mtime.IsZero() || mtime.Equal(r.startedAs) {
		r.diskMu.Unlock()
		return ""
	}
	if mtime.Equal(r.diskModTime) {
		v := r.diskVersion
		r.diskMu.Unlock()
		return v
	}
	if mtime.Equal(r.diskFailMod) && r.opts.Now().Sub(r.diskFailAt) < installedVersionBackoff {
		r.diskMu.Unlock()
		return ""
	}
	run := r.diskRun
	if run == nil || !run.modTime.Equal(mtime) {
		run = &diskRun{modTime: mtime, done: make(chan struct{})}
		r.diskRun = run
		go r.runInstalledVersion(ctx, run, exe)
	}
	r.diskMu.Unlock()

	select {
	case <-run.done:
		return run.version
	case <-ctx.Done():
		return ""
	}
}

// runInstalledVersion runs `<exe> --version` for run and records its
// answer, or its failure. It is detached from the caller's ctx that
// started it - the other callers waiting on it have their own - and is
// bounded by installedVersionTimeout.
func (r *ReleaseChecker) runInstalledVersion(ctx context.Context, run *diskRun, exe string) {
	probe := DefaultInstallProbe()
	if r.opts.Probe != nil {
		probe = *r.opts.Probe
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), installedVersionTimeout)
	defer cancel()
	out, err := probe.Run(ctx, exe, "--version")

	r.diskMu.Lock()
	defer r.diskMu.Unlock()
	if err != nil {
		r.diskFailMod, r.diskFailAt = run.modTime, r.opts.Now()
	} else {
		run.version = newerVersionLine(string(out), r.opts.Version)
		r.diskModTime, r.diskVersion = run.modTime, run.version
	}
	if r.diskRun == run {
		r.diskRun = nil
	}
	close(run.done)
}

// newerVersionLine reads `lmm --version`'s "lmm version X" and returns X
// when it is a release version newer than current; "" for anything else,
// a dev build's "(dev: ...)" line included.
func newerVersionLine(out, current string) string {
	fields := strings.Fields(out)
	if len(fields) != 3 || fields[0] != "lmm" || fields[1] != "version" {
		return ""
	}
	got, ok := parseSemver(fields[2])
	cur, okCur := parseSemver(current)
	if !ok || !okCur || got.pre != "" || !cur.less(got) {
		return ""
	}
	return strings.TrimPrefix(fields[2], "v")
}

// Enabled reports config.yaml's update_check. A config.yaml lmm cannot read
// answers false: a check is a courtesy, and the command that reads the
// config properly reports what is wrong with it.
func (r *ReleaseChecker) Enabled() bool {
	cfg, err := config.Load(r.opts.ConfigDir)
	if err != nil {
		return false
	}
	return cfg.UpdateCheckEnabled()
}

// Status is the cached answer, with no request.
func (r *ReleaseChecker) Status(ctx context.Context) ReleaseStatus {
	r.mu.Lock()
	st := r.load()
	r.mu.Unlock()
	return r.status(ctx, st)
}

// Check asks GitHub for the latest release and caches the answer. Unforced,
// it asks only when the automatic check is enabled, the build is not a dev
// build, and the cached answer is at least ReleaseCheckInterval old -
// otherwise it returns the cache. Forced (`lmm version --check`), it always
// asks. The returned error is the request's own failure, also recorded in
// the status's CheckError; a caller that only shows a notice ignores it.
func (r *ReleaseChecker) Check(ctx context.Context, force bool) (ReleaseStatus, error) {
	st, err := r.refresh(ctx, force)
	return r.status(ctx, st), err
}

// Refresh is the unforced Check for a caller that wants only the cache
// brought up to date - a background refresh, which has no use for the
// status and should not pay for detecting the install method.
func (r *ReleaseChecker) Refresh(ctx context.Context) error {
	_, err := r.refresh(ctx, false)
	return err
}

// PendingUpdate is the cached status when it reports an update, and false
// otherwise - the CLI's after-command notice, which runs after every
// interactive command and so asks a package manager about the binary only
// on the runs that have an update to word, and then normally answers from
// the cached install method. ctx bounds the detection when it must run.
func (r *ReleaseChecker) PendingUpdate(ctx context.Context) (ReleaseStatus, bool) {
	r.mu.Lock()
	st := r.load()
	r.mu.Unlock()
	if !r.updateAvailable(st) {
		return ReleaseStatus{}, false
	}
	return r.status(ctx, st), true
}

// RefreshDue reports whether an unforced check would ask GitHub now: the
// check is on, this is a release build, and no answer (nor an attempt
// still standing) is recent enough to keep.
func (r *ReleaseChecker) RefreshDue() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.due(r.load(), r.opts.Now())
}

// due is RefreshDue against a loaded state.
func (r *ReleaseChecker) due(st releaseCheckState, now time.Time) bool {
	if r.opts.Dev || !r.Enabled() {
		return false
	}
	return !within(st.CheckedAt, now, ReleaseCheckInterval) && !within(st.AttemptedAt, now, releaseRetryBackoff(st.Attempts))
}

// within reports whether t is set and less than d before now.
func within(t, now time.Time, d time.Duration) bool {
	age := now.Sub(t)
	return !t.IsZero() && age >= 0 && age < d
}

// refresh is Check without the status.
func (r *ReleaseChecker) refresh(ctx context.Context, force bool) (releaseCheckState, error) {
	r.mu.Lock()
	st := r.load()
	now := r.opts.Now()
	if !force && !r.due(st, now) {
		r.mu.Unlock()
		return st, nil
	}
	// The attempt is recorded BEFORE the request: a CLI run that exits
	// before the answer arrives still holds off the next command's attempt
	// for a while, so a slow network is not asked on every one.
	_, _ = r.update(func(s *releaseCheckState) { s.AttemptedAt, s.Attempts = now, s.Attempts+1 })
	r.mu.Unlock()

	// Unlocked while the request is out: a Status read (a page load in
	// `lmm serve`) answers from the cache instead of waiting on GitHub.
	ans, err := r.fetch(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil && ctx.Err() != nil {
		// The caller gave up - a CLI command finishing, Ctrl-C, a server
		// shutting down - which says nothing about GitHub: AttemptedAt and
		// Attempts stand, and the last real answer stays the answer.
		return r.load(), err
	}
	st, _ = r.update(func(s *releaseCheckState) {
		s.CheckedAt, s.AttemptedAt, s.Attempts = now, time.Time{}, 0
		switch {
		case err != nil:
			s.Error = err.Error()
		case ans.skip:
			s.Error = ""
		default:
			s.Latest, s.URL, s.Error = ans.latest, ans.url, ""
		}
	})
	return st, err
}

// FirstRunNoticeDue reports whether the one-time "lmm checks GitHub for
// releases" notice has yet to be shown from this data dir.
func (r *ReleaseChecker) FirstRunNoticeDue() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.load().NoticeShown
}

// RecordFirstRunNotice records that the first-run notice was shown.
func (r *ReleaseChecker) RecordFirstRunNotice() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.update(func(s *releaseCheckState) { s.NoticeShown = true })
	return err
}

// FirstRunNotice is the one-time notice's text, which both frontends show
// before their first check: that lmm asks GitHub, and how to stop it.
func (r *ReleaseChecker) FirstRunNotice() string {
	return "lmm checks GitHub for a new release once a day; to turn this off, set update_check: false in " +
		filepath.Join(r.opts.ConfigDir, "config.yaml")
}

// fetch reads the latest release: its version and page, "" for both when
// none is published, or skip when "latest" is not a stable release.
func (r *ReleaseChecker) fetch(ctx context.Context) (releaseAnswer, error) {
	var doc struct {
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := r.client.DoJSON(ctx, http.MethodGet, releaseLatestPath, &doc); err != nil {
		if errors.Is(err, errNoRelease) {
			return releaseAnswer{}, nil
		}
		return releaseAnswer{}, err
	}
	v, ok := parseSemver(doc.TagName)
	if !ok || doc.Draft || doc.Prerelease || v.pre != "" {
		return releaseAnswer{skip: true}, nil
	}
	return releaseAnswer{
		latest: strings.TrimPrefix(strings.TrimPrefix(doc.TagName, "v"), "V"),
		url:    domain.SafeWebURL(doc.HTMLURL),
	}, nil
}

// status renders a cache state as the wire document.
func (r *ReleaseChecker) status(ctx context.Context, st releaseCheckState) ReleaseStatus {
	out := ReleaseStatus{
		Current:      r.opts.Version,
		Latest:       st.Latest,
		ReleaseURL:   domain.SafeWebURL(st.URL),
		CheckedAt:    st.CheckedAt,
		CheckError:   st.Error,
		CheckEnabled: r.Enabled(),
		Dev:          r.opts.Dev,
		Install:      r.installMethod(ctx),
	}
	out.UpdateAvailable = r.updateAvailable(st)
	return out
}

// updateAvailable is st's latest release newer than the running version,
// by semver; never for a dev build.
func (r *ReleaseChecker) updateAvailable(st releaseCheckState) bool {
	if r.opts.Dev || st.Latest == "" {
		return false
	}
	cur, okCur := parseSemver(r.opts.Version)
	lat, okLat := parseSemver(st.Latest)
	return okCur && okLat && cur.less(lat)
}

// PrimeInstallMethod detects the install method now, so a later status
// read finds it ready - `lmm serve` calls it at start, in the background,
// so its first GET /api/v1/updates/lmm does not wait on a package manager.
func (r *ReleaseChecker) PrimeInstallMethod(ctx context.Context) {
	r.installMethod(ctx)
}

// installMethod is how the binary was installed, known once per checker:
// the binary does not change owner while it runs. One caller detects at a
// time and the rest wait on it, each for no longer than its own ctx
// allows; a caller out of time gets self-managed, which is not remembered,
// so the next caller with time to spare detects properly.
func (r *ReleaseChecker) installMethod(ctx context.Context) InstallMethod {
	if r.opts.Dev {
		return InstallMethod{Method: InstallDev}
	}
	for {
		r.installMu.Lock()
		if r.installKnown {
			m := r.install
			r.installMu.Unlock()
			return m
		}
		if wait := r.installDone; wait != nil {
			r.installMu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return InstallMethod{Method: InstallSelfManaged}
			}
		}
		done := make(chan struct{})
		r.installDone = done
		r.installMu.Unlock()

		m, ok := r.detectInstall(ctx)

		r.installMu.Lock()
		if ok {
			r.install, r.installKnown = m, true
		}
		r.installDone = nil
		close(done)
		r.installMu.Unlock()
		return m
	}
}

// detectInstall answers from the cache file when it is about this very
// binary, and otherwise asks the package managers and caches a complete
// answer. The bool is false for an answer ctx cut short.
func (r *ReleaseChecker) detectInstall(ctx context.Context) (InstallMethod, bool) {
	probe := DefaultInstallProbe()
	if r.opts.Probe != nil {
		probe = *r.opts.Probe
	}
	exe, err := probe.Executable()
	if err != nil || exe == "" {
		return InstallMethod{Method: InstallSelfManaged}, true
	}
	modTime := probe.ModTime
	if modTime == nil {
		modTime = fileModTime
	}
	mtime, statErr := modTime(exe)

	if statErr == nil {
		r.mu.Lock()
		c := r.load().Install
		r.mu.Unlock()
		if c != nil && c.Executable == exe && c.ModTime.Equal(mtime) {
			return c.Method, true
		}
	}
	m, ok := detectInstallMethodAt(ctx, probe, exe)
	if ok && statErr == nil {
		r.mu.Lock()
		_, _ = r.update(func(s *releaseCheckState) {
			s.Install = &installCacheEntry{Executable: exe, ModTime: mtime, Method: m}
		})
		r.mu.Unlock()
	}
	return m, ok
}

// load reads the cache. A missing or unreadable file is an empty state -
// never an error: it only means the next check asks.
func (r *ReleaseChecker) load() releaseCheckState {
	var st releaseCheckState
	data, err := os.ReadFile(filepath.Join(r.opts.DataDir, releaseCheckFile))
	if err != nil {
		return releaseCheckState{}
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return releaseCheckState{}
	}
	return st
}

// update applies change to the cache file as it is NOW - re-read just
// before the write, so the fields another process wrote since this one
// last read it survive (a CLI run beside `lmm serve`) - and saves it. The
// caller holds r.mu. Two processes can still race inside the instant
// between the read and the rename; the cost is one field one of them
// re-learns, never a damaged file.
func (r *ReleaseChecker) update(change func(*releaseCheckState)) (releaseCheckState, error) {
	st := r.load()
	change(&st)
	return st, r.save(st)
}

// save writes the cache through a temp file and a rename, so a CLI run and
// a running `lmm serve` never read each other's half-written file.
func (r *ReleaseChecker) save(st releaseCheckState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.opts.DataDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.opts.DataDir, releaseCheckFile+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(r.opts.DataDir, releaseCheckFile)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// semver is a parsed MAJOR.MINOR.PATCH[-PRERELEASE] version. Build
// metadata ("+...") is dropped, as semver says it must be for precedence.
type semver struct {
	major, minor, patch int
	pre                 string
}

// parseSemver parses s, with an optional leading "v". Anything that is not
// exactly three numeric parts is not a version this check compares.
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "V")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var nums [3]int
	for i, p := range parts {
		if !allDigits(p) {
			return semver{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return semver{}, false
		}
		nums[i] = n
	}
	return semver{major: nums[0], minor: nums[1], patch: nums[2], pre: pre}, true
}

// less reports whether v has lower precedence than w. Prerelease
// identifiers order only as "a prerelease precedes its release", which is
// all a check that never reports a prerelease needs.
func (v semver) less(w semver) bool {
	if v.major != w.major {
		return v.major < w.major
	}
	if v.minor != w.minor {
		return v.minor < w.minor
	}
	if v.patch != w.patch {
		return v.patch < w.patch
	}
	return v.pre != "" && w.pre == ""
}

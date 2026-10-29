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
// day rather than on every command. A user turns it off with
// `update_check: false` in config.yaml.

// GitHubAPIBase is the production ReleaseCheckOptions.APIBase. It is not a
// default: NewReleaseChecker refuses an empty APIBase, so nothing - a test
// least of all - reaches GitHub without naming it.
const GitHubAPIBase = "https://api.github.com"

// ReleaseCheckInterval is how long a check's answer stands before the next
// unforced check asks GitHub again.
const ReleaseCheckInterval = 24 * time.Hour

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
	// limited to http(s) by domain.SafeWebURL.
	ReleaseURL string `json:"release_url,omitempty"`
	// CheckedAt is when lmm last asked GitHub, successfully or not. Zero
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
	Install InstallMethod `json:"install"`
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

	mu sync.Mutex // serialises a check's read-modify-write of the cache

	installOnce sync.Once
	install     InstallMethod
}

// releaseCheckState is the cache file's document.
type releaseCheckState struct {
	CheckedAt   time.Time `json:"checked_at"`
	Latest      string    `json:"latest,omitempty"`
	URL         string    `json:"url,omitempty"`
	Error       string    `json:"error,omitempty"`
	NoticeShown bool      `json:"first_run_notice_shown,omitempty"`
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
	return &ReleaseChecker{
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
// on the runs that have an update to word.
func (r *ReleaseChecker) PendingUpdate(ctx context.Context) (ReleaseStatus, bool) {
	r.mu.Lock()
	st := r.load()
	r.mu.Unlock()
	if !r.updateAvailable(st) {
		return ReleaseStatus{}, false
	}
	return r.status(ctx, st), true
}

// refresh is Check without the status.
func (r *ReleaseChecker) refresh(ctx context.Context, force bool) (releaseCheckState, error) {
	r.mu.Lock()
	st := r.load()
	now := r.opts.Now()
	if !force {
		if age := now.Sub(st.CheckedAt); r.opts.Dev || !r.Enabled() ||
			(!st.CheckedAt.IsZero() && age >= 0 && age < ReleaseCheckInterval) {
			r.mu.Unlock()
			return st, nil
		}
	}
	// The attempt is recorded BEFORE the request, as a failure: a CLI run
	// that exits before the answer arrives still counts as today's one
	// request, so a slow network is not asked again on every command.
	st.CheckedAt = now
	st.Error = "the check did not finish"
	_ = r.save(st)
	r.mu.Unlock()

	// Unlocked while the request is out: a Status read (a page load in
	// `lmm serve`) answers from the cache instead of waiting on GitHub.
	latest, url, err := r.fetch(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()
	st = r.load()
	st.CheckedAt = now
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Latest, st.URL, st.Error = latest, url, ""
	}
	_ = r.save(st)
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
	st := r.load()
	st.NoticeShown = true
	return r.save(st)
}

// fetch reads the latest release: its version and page, or "" for both
// when there is no stable release to report.
func (r *ReleaseChecker) fetch(ctx context.Context) (string, string, error) {
	var doc struct {
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := r.client.DoJSON(ctx, http.MethodGet, releaseLatestPath, &doc); err != nil {
		if errors.Is(err, errNoRelease) {
			return "", "", nil
		}
		return "", "", err
	}
	v, ok := parseSemver(doc.TagName)
	if !ok || doc.Draft || doc.Prerelease || v.pre != "" {
		return "", "", nil
	}
	return strings.TrimPrefix(strings.TrimPrefix(doc.TagName, "v"), "V"), domain.SafeWebURL(doc.HTMLURL), nil
}

// status renders a cache state as the wire document.
func (r *ReleaseChecker) status(ctx context.Context, st releaseCheckState) ReleaseStatus {
	out := ReleaseStatus{
		Current:      r.opts.Version,
		Latest:       st.Latest,
		ReleaseURL:   st.URL,
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

// installMethod is DetectInstallMethod's answer, asked once per checker: the
// binary does not change owner while it runs.
func (r *ReleaseChecker) installMethod(ctx context.Context) InstallMethod {
	r.installOnce.Do(func() {
		probe := DefaultInstallProbe()
		if r.opts.Probe != nil {
			probe = *r.opts.Probe
		}
		r.install = DetectInstallMethod(ctx, probe, r.opts.Dev)
	})
	return r.install
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

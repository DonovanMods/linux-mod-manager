package core

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/releasesig"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

// self_update.go - `lmm self-update` (#569): replace a self-managed lmm
// binary with the latest release, verified first.
//
// The order is the point. The release's checksums.txt is verified against
// the ed25519 keys compiled in below (internal/releasesig), and only then is
// the tarball checked against its line in that file - so a tarball is
// trusted because the release key vouched for its hash, never because
// GitHub served it. The line is looked up by the asset's name, which carries
// the version, so a genuinely signed checksums.txt from an older release
// replayed beside a newer tarball lists no line for it and is refused. The
// binary is then extracted into a temp file beside the installed one and
// renamed over it: a crash at any point leaves the old binary or the new
// one, never half of either.

// trustedReleaseKeys are the base64 ed25519 public keys a release's
// checksums.txt.sig must verify under, any one of them. A list so a key
// rotation can overlap: docs/release-signing.md has the procedure.
var trustedReleaseKeys = []string{
	"NFjV1hUuRPm3P/3iQ+Ohyfqmzty2G27mLpaCdsdJGkg=",
}

// TrustedReleaseKeys returns a copy of the public keys compiled into lmm
// that a release signature may verify under - for tools/releasekey's
// `trusted`, which the signing-key check workflow reads.
func TrustedReleaseKeys() []string {
	return slices.Clone(trustedReleaseKeys)
}

// GitHubDownloadBase is the production SelfUpdateOptions.DownloadBase: the
// host a release's assets download from. Like GitHubAPIBase it is not a
// default, so a test that forgot to set it never reaches GitHub.
const GitHubDownloadBase = "https://github.com"

// releaseDownloadPath is the path, under DownloadBase, of a release's
// assets: <path><tag>/<asset>.
const releaseDownloadPath = "/DonovanMods/linux-mod-manager/releases/download/"

// Release asset names and their size caps. A cap is a ceiling, generous
// against what a release holds, so a hostile or broken server cannot fill
// the disk: checksums.txt lists a dozen files, a signature is 88 bytes, and
// the tarball is a statically linked binary and its docs.
const (
	checksumsAsset        = "checksums.txt"
	signatureAsset        = "checksums.txt.sig"
	checksumsMaxBytes     = 64 << 10
	signatureMaxBytes     = 4 << 10
	releaseTarballMaxSize = 256 << 20
	// releaseBinaryMaxSize caps the extracted lmm binary.
	releaseBinaryMaxSize = 256 << 20
)

// selfUpdateTimeout bounds the release document request; the downloads are
// bounded by the caller's context, their size caps and selfUpdateStall.
const selfUpdateTimeout = 30 * time.Second

// selfUpdateStall fails a download that goes this long without a byte, so
// an unattended run (lmm-update.timer) cannot hang on a dead connection.
const selfUpdateStall = time.Minute

// ErrDevBuild refuses `lmm self-update` for a development build, which has
// no release to update to.
var ErrDevBuild = errors.New("this is a development build of lmm, which self-update does not replace; rebuild it from source, or install a release")

// ErrReleaseNotVerified matches every refusal of a release that did not
// verify: a *ReleaseSignatureError and a *ReleaseChecksumError.
var ErrReleaseNotVerified = errors.New("the release did not verify")

// PackageManagedError refuses to replace a binary a package manager owns:
// writing over it would desync the package database. Its message names
// that manager's upgrade command, which lmm never runs itself.
type PackageManagedError struct {
	// Install is the detected install method, InstallPackage.
	Install InstallMethod
}

// Error names the package and its upgrade command.
func (e *PackageManagedError) Error() string {
	return fmt.Sprintf("lmm was installed by the %s package (%s), so lmm self-update will not replace it; upgrade it with: %s",
		e.Install.Package, e.Install.Manager, e.Install.UpgradeCommand)
}

// InstallMethodUnknownError refuses an update when lmm could not tell
// whether a package manager owns its binary: an owner query failed some
// other way than "not mine" - a locked database, one mid-upgrade, a query
// that timed out - or the caller's context ended first. Reading that as
// self-managed could overwrite a package's file, so nothing is replaced.
type InstallMethodUnknownError struct {
	// Executable is the binary lmm asked about.
	Executable string
}

// Error says what could not be settled, and to try again.
func (e *InstallMethodUnknownError) Error() string {
	return fmt.Sprintf("could not determine whether a package manager owns lmm (%s): a package database query failed or timed out, perhaps while it is in use; try again", e.Executable)
}

// CheckSelfUpdatable refuses an install method lmm self-update cannot
// update: a *PackageManagedError for a package's binary, ErrDevBuild for a
// development build.
func CheckSelfUpdatable(m InstallMethod) error {
	switch m.Method {
	case InstallPackage:
		return &PackageManagedError{Install: m}
	case InstallDev:
		return ErrDevBuild
	}
	return nil
}

// CheckSelfUpdateInstall asks how the running binary was installed and
// refuses one lmm self-update cannot replace, as CheckSelfUpdatable does -
// and, unlike DetectInstallMethod's never-failing answer, also refuses one
// it could not determine, with an *InstallMethodUnknownError. SelfUpdater's
// Plan and `lmm service install --auto-update` both ask it, so neither
// replaces a binary nor installs a timer on a guess.
func CheckSelfUpdateInstall(ctx context.Context, probe InstallProbe, dev bool) error {
	if dev {
		return ErrDevBuild
	}
	exe, err := probe.Executable()
	if err != nil {
		return fmt.Errorf("finding the lmm binary: %w", err)
	}
	if exe == "" {
		return errors.New("finding the lmm binary: its path is unknown")
	}
	m, definitive := detectInstallMethodAt(ctx, probe, exe)
	if err := CheckSelfUpdatable(m); err != nil {
		return err
	}
	if !definitive {
		return &InstallMethodUnknownError{Executable: exe}
	}
	return nil
}

// releaseTagPattern is the only release tag self-update acts on: a plain
// vMAJOR.MINOR.PATCH, ASCII digits, nothing before or after it.
var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// ReleaseTagError refuses a latest release whose tag_name is not a plain
// vX.Y.Z - build metadata, a prerelease suffix, a path, a control byte -
// before the tag reaches a download URL, a file name or the terminal.
type ReleaseTagError struct {
	// Tag is the tag as GitHub sent it.
	Tag string
}

// releaseTagShown caps how much of a refused tag the error quotes.
const releaseTagShown = 64

// Error quotes the tag, escaped and shortened, never raw.
func (e *ReleaseTagError) Error() string {
	tag := e.Tag
	if len(tag) > releaseTagShown {
		tag = tag[:releaseTagShown] + "..."
	}
	return fmt.Sprintf("the latest lmm release's tag %q is not a plain vX.Y.Z version, so self-update will not use it; nothing was installed", tag)
}

// checkReleaseTag returns tag's version without its "v", or a
// *ReleaseTagError for a tag that is not exactly vX.Y.Z.
func checkReleaseTag(tag string) (semver, error) {
	if !releaseTagPattern.MatchString(tag) {
		return semver{}, &ReleaseTagError{Tag: tag}
	}
	v, ok := parseSemver(tag)
	if !ok {
		return semver{}, &ReleaseTagError{Tag: tag}
	}
	return v, nil
}

// ErrSelfUpdateRunning is SelfUpdater.Lock's refusal: another lmm
// self-update holds the lock on the same data directory.
var ErrSelfUpdateRunning = errors.New("another self-update is running")

// selfUpdateLockFile is the lock file, in the data directory, that keeps
// two self-updates from running at once.
const selfUpdateLockFile = "self-update.lock"

// ReleaseSignatureError refuses a release whose checksums.txt is not signed
// by a key compiled into lmm: the signature is missing, or it does not
// verify under any trusted key.
type ReleaseSignatureError struct {
	// Version is the release's version.
	Version string
	// Missing is true when the release has no checksums.txt.sig.
	Missing bool
	// Err is why it did not verify; nil when Missing.
	Err error
}

// Error says which, and that nothing was installed.
func (e *ReleaseSignatureError) Error() string {
	if e.Missing {
		return fmt.Sprintf("lmm %s has no signature (%s), so it cannot be verified; nothing was installed", e.Version, signatureAsset)
	}
	return fmt.Sprintf("lmm %s's signature does not verify (%v); nothing was installed", e.Version, e.Err)
}

// Unwrap returns the verification failure.
func (e *ReleaseSignatureError) Unwrap() error { return e.Err }

// Is reports whether target is ErrReleaseNotVerified.
func (e *ReleaseSignatureError) Is(target error) bool { return target == ErrReleaseNotVerified }

// ReleaseChecksumError refuses a tarball the signed checksums.txt does not
// vouch for: its hash differs from its line, or it has no line.
type ReleaseChecksumError struct {
	// Asset is the tarball's name.
	Asset string
	// Unlisted is true when checksums.txt has no line for Asset.
	Unlisted bool
	// Want and Got are the listed and the computed sha256, hex.
	Want, Got string
}

// Error names the asset and what did not match.
func (e *ReleaseChecksumError) Error() string {
	if e.Unlisted {
		return fmt.Sprintf("the release's signed %s does not list %s; nothing was installed", checksumsAsset, e.Asset)
	}
	return fmt.Sprintf("%s does not match its signed checksum (want sha256 %s, got %s); nothing was installed", e.Asset, e.Want, e.Got)
}

// Is reports whether target is ErrReleaseNotVerified.
func (e *ReleaseChecksumError) Is(target error) bool { return target == ErrReleaseNotVerified }

// ReleaseArchiveError refuses a verified tarball lmm still will not take
// the binary from: it is not a gzipped tar, it has no lmm, its lmm is not a
// regular file, or a member's path tries to leave the archive.
type ReleaseArchiveError struct {
	// Asset is the tarball's name.
	Asset string
	// Reason says what is wrong with it.
	Reason string
}

// Error names the asset and the reason.
func (e *ReleaseArchiveError) Error() string {
	return fmt.Sprintf("%s: %s; nothing was installed", e.Asset, e.Reason)
}

// ReleaseAssetError is a release that lacks a file self-update needs: the
// tarball for this machine's architecture, or checksums.txt.
type ReleaseAssetError struct {
	// Version is the release's version.
	Version string
	// Asset is the missing file's name.
	Asset string
}

// Error names the release and the missing file.
func (e *ReleaseAssetError) Error() string {
	return fmt.Sprintf("lmm %s has no %s to download; nothing was installed", e.Version, e.Asset)
}

// ExecutableNotWritableError refuses an update lmm could not install: the
// directory holding the binary is not writable by this user, and the
// rename that replaces it needs it to be. lmm never escalates; the message
// says how to own the binary instead.
type ExecutableNotWritableError struct {
	// Executable is the binary that would have been replaced.
	Executable string
	// Dir is its directory.
	Dir string
	// Err is the failure that showed it.
	Err error
}

// Error names the directory and the two ways forward.
func (e *ExecutableNotWritableError) Error() string {
	return fmt.Sprintf("lmm cannot replace %s: you cannot write to %s (%v). lmm self-update only replaces a copy of lmm you own: move lmm into a directory of yours, such as ~/.local/bin, or download the new release yourself",
		e.Executable, e.Dir, e.Err)
}

// Unwrap returns the underlying failure.
func (e *ExecutableNotWritableError) Unwrap() error { return e.Err }

// ServeRestarter restarts lmm-serve.service when it is running, so the
// service runs the binary an update just installed. *UserUnits is the real
// one.
type ServeRestarter interface {
	// RestartServeIfActive restarts the service if it is active, and
	// reports whether it did.
	RestartServeIfActive(ctx context.Context) (bool, error)
}

// SelfUpdateOptions configures a SelfUpdater.
type SelfUpdateOptions struct {
	// ConfigDir holds config.yaml, whose auto_update AutoUpdateEnabled reads.
	ConfigDir string
	// DataDir holds the staging directory the release downloads into, the
	// self-update lock and the automatic-update state (auto_update.go).
	// Required.
	DataDir string
	// Version is the running version, the bare release version. Required.
	Version string
	// Dev marks a development build, which is refused.
	Dev bool
	// APIBase is GitHub's API root (GitHubAPIBase), and DownloadBase the
	// host releases download from (GitHubDownloadBase); an httptest server
	// for both in a test. Required.
	APIBase, DownloadBase string
	// HTTPClient carries the requests; nil uses http.DefaultTransport,
	// failing a transfer that stalls for selfUpdateStall, with no overall
	// timeout (the caller's context bounds a download).
	HTTPClient *http.Client
	// Probe answers how lmm was installed and where its binary is; nil is
	// DefaultInstallProbe.
	Probe *InstallProbe
	// TrustedKeys are the public keys a release signature may verify
	// under; nil is TrustedReleaseKeys. A test passes its own.
	TrustedKeys []string
	// Arch is the release architecture to install; "" is runtime.GOARCH.
	Arch string
	// Restarter restarts lmm-serve.service for RestartServeWhenIdle: only
	// `lmm serve` running as that service sets it (#586). nil restarts
	// nothing.
	Restarter ServeRestarter
	// OpLockPath is the cross-process mutation lock every lmm mutation
	// takes (ServiceConfig.OpLockPath): an automatic update does not
	// install while another lmm holds it (#586). Empty skips that check.
	OpLockPath string
	// Now is the clock automatic updates record their outcome by; nil is
	// time.Now.
	Now func() time.Time
}

// SelfUpdater updates the running lmm binary to the latest release (#569).
// It needs no Service: it touches no database, only the binary and its
// staging directory. Plan reads the latest release and refuses an install
// it cannot update; Apply downloads, verifies and replaces.
type SelfUpdater struct {
	opts       SelfUpdateOptions
	httpClient *http.Client // carries both clients' requests, with releaseRedirectPolicy
	api        *httpclient.Client
	assets     *httpclient.Client
}

// SelfUpdatePlan is what `lmm self-update --check` (and --json without
// --yes) reports: the running version, the latest release, and the binary
// an update would replace.
type SelfUpdatePlan struct {
	// Current is the running version.
	Current string `json:"current"`
	// Latest is the newest stable release's version; empty when none is
	// published.
	Latest string `json:"latest,omitempty"`
	// UpToDate is true when Latest is not newer than Current: Apply then
	// does nothing.
	UpToDate bool `json:"up_to_date"`
	// Executable is the binary an update replaces.
	Executable string `json:"executable"`
	// Asset is the release tarball an update downloads; empty when
	// UpToDate.
	Asset string `json:"asset,omitempty"`
	// ReleaseURL is Latest's release page.
	ReleaseURL string `json:"release_url,omitempty"`
}

// SelfUpdateResult is what `lmm self-update --yes` did.
type SelfUpdateResult struct {
	// Updated is true when a new binary was installed; false for an lmm
	// already up to date.
	Updated bool `json:"updated"`
	// Previous is the version that was running.
	Previous string `json:"previous"`
	// Version is the version now installed.
	Version string `json:"version"`
	// Executable is the binary that was replaced.
	Executable string `json:"executable"`
	// ServeRestarted is true when lmm-serve.service was running and was
	// restarted onto the new binary. Always false since #586: an update
	// replaces the binary and stops, and lmm serve restarts itself once no
	// job runs. Kept for the --json contract #569 shipped.
	ServeRestarted bool `json:"serve_restarted"`
	// ServeRestartError is why restarting it failed: the update itself
	// stands, and the service runs the old binary until it restarts.
	// Always empty since #586, as ServeRestarted.
	ServeRestartError string `json:"serve_restart_error,omitempty"`
}

// NewSelfUpdater returns an updater for opts. It panics on an empty
// APIBase, DownloadBase, DataDir or Version - a programming error, and the
// base rule keeps a test that forgot one off the network.
func NewSelfUpdater(opts SelfUpdateOptions) *SelfUpdater {
	if opts.APIBase == "" || opts.DownloadBase == "" {
		panic("core.NewSelfUpdater: APIBase and DownloadBase are required")
	}
	if opts.DataDir == "" || opts.Version == "" {
		panic("core.NewSelfUpdater: DataDir and Version are required")
	}
	if opts.TrustedKeys == nil {
		opts.TrustedKeys = TrustedReleaseKeys()
	}
	if opts.Arch == "" {
		opts.Arch = runtime.GOARCH
	}
	hc := &http.Client{Transport: &httpclient.IdleTimeout{Timeout: selfUpdateStall}}
	if opts.HTTPClient != nil {
		c := *opts.HTTPClient
		hc = &c
	}
	hc.CheckRedirect = releaseRedirectPolicy
	client := func(base string, maxBytes int64) *httpclient.Client {
		return httpclient.New(httpclient.Options{
			HTTPClient:       hc,
			BaseURL:          strings.TrimSuffix(base, "/"),
			Anonymous:        true,
			AuthLabel:        "GitHub",
			UserAgent:        "lmm/" + opts.Version,
			MaxResponseBytes: maxBytes,
			ErrorMapper: func(status int, _ []byte, _ string) error {
				if status == http.StatusNotFound {
					return errNoRelease
				}
				return fmt.Errorf("GitHub answered %d %s", status, http.StatusText(status))
			},
		})
	}
	return &SelfUpdater{
		opts:       opts,
		httpClient: hc,
		api:        client(opts.APIBase, releaseCheckMaxBytes),
		assets:     client(opts.DownloadBase, 0),
	}
}

// releaseRedirectMax is how many redirects a request follows, as
// net/http's default policy.
const releaseRedirectMax = 10

// releaseRedirectPolicy is the self-update client's CheckRedirect. A
// release download redirects from github.com to GitHub's object storage;
// it follows that, and any redirect, only over https to github.com,
// *.github.com or *.githubusercontent.com on the default port, and refuses
// everything else - an https->http downgrade included. The signature
// check makes a tarball from elsewhere useless anyway; this keeps lmm from
// fetching it at all.
func releaseRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= releaseRedirectMax {
		return fmt.Errorf("refusing a redirect: stopped after %d redirects", releaseRedirectMax)
	}
	host := strings.ToLower(req.URL.Hostname())
	gitHub := host == "github.com" || strings.HasSuffix(host, ".github.com") || strings.HasSuffix(host, ".githubusercontent.com")
	port := req.URL.Port()
	if req.URL.Scheme != "https" || !gitHub || (port != "" && port != "443") {
		return fmt.Errorf("refusing a redirect to %q: lmm downloads releases only from GitHub, over https", req.URL.Scheme+"://"+req.URL.Host)
	}
	return nil
}

// Lock takes the self-update lock, <DataDir>/self-update.lock, which a run
// holds from its Plan through its Apply so two runs - the timer's and a
// person's - never replace the binary at once. It does not wait: a lock
// another process holds is ErrSelfUpdateRunning. The lock is a flock, so a
// run that dies drops it; unlock is idempotent.
func (u *SelfUpdater) Lock() (unlock func(), err error) {
	if err := os.MkdirAll(u.opts.DataDir, 0o755); err != nil {
		return nil, err
	}
	l, err := acquireOpLock(context.Background(), filepath.Join(u.opts.DataDir, selfUpdateLockFile), 0)
	if err != nil {
		var held *OperationInProgressError
		if errors.As(err, &held) {
			if held.PID != 0 {
				return nil, fmt.Errorf("%w (pid %d)", ErrSelfUpdateRunning, held.PID)
			}
			return nil, ErrSelfUpdateRunning
		}
		return nil, err
	}
	return l.release, nil
}

// probe is the configured InstallProbe, or the real one.
func (u *SelfUpdater) probe() InstallProbe {
	if u.opts.Probe != nil {
		return *u.opts.Probe
	}
	return DefaultInstallProbe()
}

// Plan refuses a binary self-update cannot replace - a *PackageManagedError,
// ErrDevBuild, or an *InstallMethodUnknownError when that could not be
// settled - and otherwise reads the latest release and compares it with the
// running version. A latest release whose tag is not a plain vX.Y.Z is a
// *ReleaseTagError. Up to date is a plan, not an error.
func (u *SelfUpdater) Plan(ctx context.Context) (SelfUpdatePlan, error) {
	probe := u.probe()
	if err := CheckSelfUpdateInstall(ctx, probe, u.opts.Dev); err != nil {
		return SelfUpdatePlan{}, err
	}
	exe, err := probe.Executable()
	if err != nil {
		return SelfUpdatePlan{}, fmt.Errorf("finding the lmm binary: %w", err)
	}
	plan := SelfUpdatePlan{Current: u.opts.Version, Executable: exe, UpToDate: true}

	reqCtx, cancel := context.WithTimeout(ctx, selfUpdateTimeout)
	defer cancel()
	var doc struct {
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := u.api.DoJSON(reqCtx, http.MethodGet, releaseLatestPath, &doc); err != nil {
		if errors.Is(err, errNoRelease) {
			return plan, nil
		}
		return SelfUpdatePlan{}, fmt.Errorf("asking GitHub for the latest lmm release: %w", err)
	}
	latest, err := checkReleaseTag(doc.TagName)
	if err != nil {
		return SelfUpdatePlan{}, err
	}
	if doc.Draft || doc.Prerelease {
		return plan, nil
	}
	plan.Latest = strings.TrimPrefix(doc.TagName, "v")
	plan.ReleaseURL = domain.SafeWebURL(doc.HTMLURL)
	cur, ok := parseSemver(u.opts.Version)
	if ok && !cur.less(latest) {
		return plan, nil
	}
	plan.UpToDate = false
	plan.Asset = fmt.Sprintf("lmm_%s_linux_%s.tar.gz", plan.Latest, u.opts.Arch)
	return plan, nil
}

// Apply installs plan's release: it checks the binary's directory is
// writable, downloads checksums.txt, its signature and the tarball into a
// staging directory, verifies the signature and then the checksum, extracts
// the binary beside the installed one and renames it over it. A refusal at
// any step leaves the installed binary as it was. It does not restart
// lmm-serve.service (#586 review): this process cannot see lmm serve's
// jobs, so a running lmm serve restarts itself onto the new binary once
// none runs (RestartServeWhenIdle), or its web UI asks for the restart.
func (u *SelfUpdater) Apply(ctx context.Context, plan SelfUpdatePlan) (SelfUpdateResult, error) {
	return u.install(ctx, plan)
}

// install is Apply, which RunAuto shares: everything up to the renamed
// binary.
func (u *SelfUpdater) install(ctx context.Context, plan SelfUpdatePlan) (SelfUpdateResult, error) {
	res := SelfUpdateResult{Previous: plan.Current, Version: plan.Current, Executable: plan.Executable}
	if plan.UpToDate {
		return res, nil
	}
	if plan.Latest == "" || plan.Asset == "" || plan.Executable == "" {
		return res, errors.New("self-update plan is incomplete")
	}
	tag := "v" + plan.Latest
	if _, err := checkReleaseTag(tag); err != nil {
		return res, err
	}
	if plan.Asset != fmt.Sprintf("lmm_%s_linux_%s.tar.gz", plan.Latest, u.opts.Arch) {
		return res, fmt.Errorf("self-update plan names asset %q, not this release's tarball for %s", plan.Asset, u.opts.Arch)
	}
	dir := filepath.Dir(plan.Executable)
	if err := checkWritableDir(dir); err != nil {
		return res, &ExecutableNotWritableError{Executable: plan.Executable, Dir: dir, Err: err}
	}

	staging, err := newStagingDir(filepath.Join(u.opts.DataDir, stagingDirName), "self-update-*")
	if err != nil {
		return res, err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	checksums, err := u.download(ctx, tag, checksumsAsset, checksumsMaxBytes, nil)
	if errors.Is(err, errNoRelease) {
		return res, &ReleaseAssetError{Version: plan.Latest, Asset: checksumsAsset}
	}
	if err != nil {
		return res, err
	}
	sig, err := u.download(ctx, tag, signatureAsset, signatureMaxBytes, nil)
	if errors.Is(err, errNoRelease) {
		return res, &ReleaseSignatureError{Version: plan.Latest, Missing: true}
	}
	if err != nil {
		return res, err
	}
	if err := u.verifySignature(checksums, string(sig)); err != nil {
		return res, &ReleaseSignatureError{Version: plan.Latest, Err: err}
	}
	want, ok := checksumFor(checksums, plan.Asset)
	if !ok {
		return res, &ReleaseChecksumError{Asset: plan.Asset, Unlisted: true}
	}

	tarPath := filepath.Join(staging, plan.Asset)
	f, err := os.OpenFile(tarPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return res, err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := u.download(ctx, tag, plan.Asset, releaseTarballMaxSize, io.MultiWriter(f, hash)); err != nil {
		if errors.Is(err, errNoRelease) {
			return res, &ReleaseAssetError{Version: plan.Latest, Asset: plan.Asset}
		}
		return res, err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, want) {
		return res, &ReleaseChecksumError{Asset: plan.Asset, Want: want, Got: got}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return res, err
	}
	if err := replaceExecutable(f, plan.Asset, plan.Executable); err != nil {
		return res, err
	}
	res.Updated, res.Version = true, plan.Latest
	return res, nil
}

// download fetches a release asset. With w nil it returns the body, which
// must fit maxBytes; otherwise it streams the body into w, and fails once
// it passes maxBytes. A missing asset is errNoRelease.
func (u *SelfUpdater) download(ctx context.Context, tag, asset string, maxBytes int64, w io.Writer) ([]byte, error) {
	resp, err := u.assets.DoStream(ctx, http.MethodGet, releaseDownloadPath+tag+"/"+asset, nil)
	if err != nil {
		if errors.Is(err, errNoRelease) {
			return nil, errNoRelease
		}
		return nil, fmt.Errorf("downloading %s: %w", asset, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: GitHub answered %d", asset, resp.StatusCode)
	}
	var buf bytes.Buffer
	dst := w
	if dst == nil {
		dst = &buf
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", asset, err)
	}
	if n > maxBytes {
		return nil, fmt.Errorf("downloading %s: it is larger than the %d bytes lmm accepts", asset, maxBytes)
	}
	return buf.Bytes(), nil
}

// verifySignature checks sig over data under each trusted key, and passes
// when one verifies.
func (u *SelfUpdater) verifySignature(data []byte, sig string) error {
	if len(u.opts.TrustedKeys) == 0 {
		return errors.New("lmm has no release key to verify with")
	}
	var errs []error
	for _, key := range u.opts.TrustedKeys {
		err := releasesig.Verify(key, data, sig)
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	// Every key gives the same answer for a malformed signature; a
	// mismatch is the one worth naming either way.
	for _, err := range errs {
		if !errors.Is(err, releasesig.ErrBadSignature) {
			return err
		}
	}
	return releasesig.ErrBadSignature
}

// checksumFor finds asset's sha256 in a checksums.txt: "<hex>  <name>" per
// line, as sha256sum and GoReleaser write it (a "*" before the name marks
// binary mode).
func checksumFor(checksums []byte, asset string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(string(checksums)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == asset && len(fields[0]) == sha256.Size*2 {
			if _, err := hex.DecodeString(fields[0]); err == nil {
				return fields[0], true
			}
		}
	}
	return "", false
}

// checkWritableDir reports whether a file can be created in dir, by
// creating and removing one: the only answer that accounts for every
// reason it might not (permissions, a read-only mount, an immutable flag).
func checkWritableDir(dir string) error {
	f, err := os.CreateTemp(dir, ".lmm-write-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// replaceExecutable extracts the lmm binary from the gzipped tar in r and
// renames it over exe: written to a temp file in exe's directory with
// exe's mode, synced, then renamed, and the directory synced after. Any
// failure removes the temp file and leaves exe alone.
func replaceExecutable(r io.Reader, asset, exe string) (err error) {
	info, err := os.Stat(exe)
	if err != nil {
		return fmt.Errorf("reading the installed lmm: %w", err)
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".lmm-update-*")
	if err != nil {
		return &ExecutableNotWritableError{Executable: exe, Dir: dir, Err: err}
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := extractBinary(r, asset, tmp); err != nil {
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return fmt.Errorf("installing the new lmm: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("installing the new lmm: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("installing the new lmm: %w", err)
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return fmt.Errorf("installing the new lmm: %w", err)
	}
	// The rename is durable once the directory is: best effort, since the
	// new binary is in place either way.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// releaseBinaryName is the binary's path inside a release tarball.
const releaseBinaryName = "lmm"

// extractBinary copies the tarball's lmm into w. Every member before it is
// checked for a path that is absolute or climbs out with "..", which
// refuses the archive; lmm itself must be a regular, non-empty file no
// larger than releaseBinaryMaxSize.
func extractBinary(r io.Reader, asset string, w io.Writer) error {
	bad := func(reason string) error { return &ReleaseArchiveError{Asset: asset, Reason: reason} }
	gz, err := gzip.NewReader(r)
	if err != nil {
		return bad("it is not a gzip file")
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return bad("it holds no " + releaseBinaryName + " binary")
		}
		if err != nil {
			return bad("it is not a readable tar archive (" + err.Error() + ")")
		}
		name := h.Name
		if path.IsAbs(name) || slices.Contains(strings.Split(name, "/"), "..") {
			return bad(fmt.Sprintf("a member's path %q leaves the archive", name))
		}
		if path.Clean(name) != releaseBinaryName {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return bad(releaseBinaryName + " in it is not a regular file")
		}
		if h.Size <= 0 || h.Size > releaseBinaryMaxSize {
			return bad(fmt.Sprintf("%s in it is %d bytes", releaseBinaryName, h.Size))
		}
		out := &trackedWriter{w: w}
		n, err := io.Copy(out, io.LimitReader(tr, h.Size))
		if out.err != nil {
			return fmt.Errorf("installing the new lmm: %w", out.err)
		}
		if err != nil {
			return bad("reading " + releaseBinaryName + " from it failed (" + err.Error() + ")")
		}
		if n != h.Size {
			return bad(releaseBinaryName + " in it is truncated")
		}
		return nil
	}
}

// trackedWriter remembers its writer's error, so a copy that fails can be
// told apart from one whose reader did.
type trackedWriter struct {
	w   io.Writer
	err error
}

func (t *trackedWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if err != nil {
		t.err = err
	}
	return n, err
}

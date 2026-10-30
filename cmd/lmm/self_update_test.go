package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/releasesig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliSelfUpdate is `lmm self-update`'s world for one test (#569): an
// httptest GitHub with release v2.2.0 signed by a key the test generated,
// a self-managed "installed" lmm 2.1.0 in a temp dir, and the fake
// systemd from service_test.go.
type cliSelfUpdate struct {
	exe     string
	systemd *cliSystemd

	mu        sync.Mutex
	downloads int
}

func newCLISelfUpdate(t *testing.T) *cliSelfUpdate {
	t.Helper()
	priv, pub, err := releasesig.Generate()
	require.NoError(t, err)

	var tarball bytes.Buffer
	gz := gzip.NewWriter(&tarball)
	tw := tar.NewWriter(gz)
	body := []byte("new lmm 2.2.0")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "lmm", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}))
	_, err = tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	asset := fmt.Sprintf("lmm_2.2.0_linux_%s.tar.gz", runtime.GOARCH)
	sum := sha256.Sum256(tarball.Bytes())
	checksums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	sig, err := releasesig.Sign(priv, []byte(checksums))
	require.NoError(t, err)
	assets := map[string][]byte{asset: tarball.Bytes(), "checksums.txt": []byte(checksums), "checksums.txt.sig": []byte(sig)}

	f := &cliSelfUpdate{exe: filepath.Join(t.TempDir(), "lmm"), systemd: &cliSystemd{}}
	require.NoError(t, os.WriteFile(f.exe, []byte("old lmm"), 0o755))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/DonovanMods/linux-mod-manager/releases/latest" {
			_, _ = w.Write([]byte(`{"tag_name":"v2.2.0","html_url":"https://github.com/DonovanMods/linux-mod-manager/releases/tag/v2.2.0"}`))
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, "/DonovanMods/linux-mod-manager/releases/download/v2.2.0/")
		if b, found := assets[name]; ok && found {
			f.mu.Lock()
			f.downloads++
			f.mu.Unlock()
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	useTestUserUnits(t, f.systemd)
	oldAPI, oldDL, oldProbe, oldKeys, oldVersion, oldDescribe := releaseAPIBase, releaseDownloadBase, releaseInstallProbe, releaseTrustedKeys, version, buildDescribe
	oldTTY, oldFlags := selfUpdateStdinIsTerminal, selfUpdate
	releaseAPIBase, releaseDownloadBase, releaseTrustedKeys = srv.URL, srv.URL, []string{pub}
	releaseInstallProbe = &core.InstallProbe{
		Executable: func() (string, error) { return f.exe, nil },
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
	}
	version, buildDescribe = "2.1.0", ""
	selfUpdateStdinIsTerminal = func() bool { return false }
	selfUpdate = selfUpdateFlags{}
	cfg, data, oldJSON := configDir, dataDir, jsonOutput
	configDir, dataDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		releaseAPIBase, releaseDownloadBase, releaseInstallProbe, releaseTrustedKeys, version, buildDescribe = oldAPI, oldDL, oldProbe, oldKeys, oldVersion, oldDescribe
		selfUpdateStdinIsTerminal, selfUpdate = oldTTY, oldFlags
		configDir, dataDir, jsonOutput = cfg, data, oldJSON
	})
	return f
}

func (f *cliSelfUpdate) installed(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.exe)
	require.NoError(t, err)
	return string(data)
}

// A manual self-update replaces the binary and leaves lmm-serve.service
// alone (#586 review): this process cannot see lmm serve's jobs, so a
// restart from here could cut one short. lmm serve restarts itself once
// idle, or its banner asks.
func TestSelfUpdateCmd_YesInstallsAndLeavesServeToRestartItself(t *testing.T) {
	f := newCLISelfUpdate(t)
	f.systemd.enabled, f.systemd.active = true, true
	selfUpdate.yes = true

	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Equal(t, "new lmm 2.2.0", f.installed(t))
	assert.Contains(t, out, "2.2.0")
	assert.Contains(t, out, f.exe)
	assert.Contains(t, out, "lmm serve restarts itself")
	for _, call := range f.systemd.calls {
		assert.NotContains(t, call, "restart", "lmm-serve.service is not restarted from here")
	}
}

// The timer's run and the CLI's detached one never restart
// lmm-serve.service either, run under systemd or not.
func TestSelfUpdateCmd_AutoNeverRestartsServe(t *testing.T) {
	f := newCLISelfUpdate(t)
	f.systemd.enabled, f.systemd.active = true, true
	selfUpdate.auto = true
	t.Setenv("INVOCATION_ID", "lmm-update.service")

	captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Equal(t, "new lmm 2.2.0", f.installed(t))
	for _, call := range f.systemd.calls {
		assert.NotContains(t, call, "restart")
	}
}

// lmm serve restarts itself through systemd only when systemd runs it:
// one started by hand has no unit to restart, and its banner asks.
func TestServeAutoUpdater_RestartsServeOnlyUnderItsUnit(t *testing.T) {
	f := newCLISelfUpdate(t)
	f.systemd.enabled, f.systemd.active = true, true
	old := releaseCheckArmed
	releaseCheckArmed = true
	t.Cleanup(func() { releaseCheckArmed = old })
	idle := core.AutoRunOptions{Quiesce: func() (func(), bool) { return func() {}, true }}

	t.Setenv("INVOCATION_ID", "")
	u := serveAutoUpdater()
	require.NotNil(t, u)
	restarted, err := u.RestartServeWhenIdle(context.Background(), idle)
	require.NoError(t, err)
	assert.False(t, restarted)
	assert.NotContains(t, strings.Join(f.systemd.calls, "\n"), "restart", "started by hand: nothing to restart")

	t.Setenv("INVOCATION_ID", "lmm-serve.service")
	u = serveAutoUpdater()
	require.NotNil(t, u)
	restarted, err = u.RestartServeWhenIdle(context.Background(), idle)
	require.NoError(t, err)
	assert.True(t, restarted)
	assert.Contains(t, strings.Join(f.systemd.calls, "\n"), "systemctl --user try-restart lmm-serve.service")
}

func TestSelfUpdateCmd_CheckReportsOnly(t *testing.T) {
	f := newCLISelfUpdate(t)
	selfUpdate.check, selfUpdate.yes = true, true
	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Contains(t, out, "2.2.0")
	assert.Equal(t, "old lmm", f.installed(t))
	assert.Zero(t, f.downloads)
}

func TestSelfUpdateCmd_JSONWithoutYesReturnsThePlan(t *testing.T) {
	f := newCLISelfUpdate(t)
	withJSONOutput(t)
	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	var plan core.SelfUpdatePlan
	require.NoError(t, json.Unmarshal([]byte(out), &plan), out)
	assert.Equal(t, "2.2.0", plan.Latest)
	assert.False(t, plan.UpToDate)
	assert.Equal(t, "old lmm", f.installed(t), "--json never prompts, and needs --yes to apply")
	assert.Zero(t, f.downloads)
}

func TestSelfUpdateCmd_JSONWithYesReturnsTheResult(t *testing.T) {
	f := newCLISelfUpdate(t)
	withJSONOutput(t)
	selfUpdate.yes = true
	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	var res core.SelfUpdateResult
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	assert.True(t, res.Updated)
	assert.Equal(t, "2.2.0", res.Version)
	assert.Equal(t, "new lmm 2.2.0", f.installed(t))
}

func TestSelfUpdateCmd_WithoutYesOffATerminalRefuses(t *testing.T) {
	f := newCLISelfUpdate(t)
	var err error
	captureStdout(t, func() error { err = runSelfUpdate(context.Background()); return nil })
	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	assert.Equal(t, "old lmm", f.installed(t))
}

func TestSelfUpdateCmd_AutoInstallsUnlessAutoUpdateIsOff(t *testing.T) {
	f := newCLISelfUpdate(t)
	selfUpdate.auto = true
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("auto_update: false\n"), 0o644))
	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Contains(t, out, "auto_update is off")
	assert.Equal(t, "old lmm", f.installed(t))
	assert.Zero(t, f.downloads)

	require.NoError(t, os.Remove(filepath.Join(configDir, "config.yaml")))
	out = captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Equal(t, "new lmm 2.2.0", f.installed(t), "on by default (#586), and --auto needs no --yes")
	assert.Contains(t, out, "Updated lmm 2.1.0 to")
	cfg, err := getServiceConfig()
	require.NoError(t, err)
	rec, ok := cfg.PendingAutoUpdateNotice()
	require.True(t, ok, "the next command announces it")
	assert.Equal(t, "2.2.0", rec.To)
}

// A package's lmm is never auto-updated, and that is no failure for
// lmm-update.timer to retry.
func TestSelfUpdateCmd_AutoStepsAsideForAPackage(t *testing.T) {
	f := newCLISelfUpdate(t)
	selfUpdate.auto = true
	releaseInstallProbe.LookPath = func(name string) (string, error) {
		if name == "dpkg" {
			return "/usr/bin/dpkg", nil
		}
		return "", os.ErrNotExist
	}
	releaseInstallProbe.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("lmm:amd64: " + f.exe + "\n"), nil
	}
	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Contains(t, out, "not updating")
	assert.Equal(t, "old lmm", f.installed(t))
}

func TestSelfUpdateCmd_RefusesAPackageOwnedBinary(t *testing.T) {
	f := newCLISelfUpdate(t)
	selfUpdate.yes = true
	releaseInstallProbe.LookPath = func(name string) (string, error) {
		if name == "dpkg" {
			return "/usr/bin/dpkg", nil
		}
		return "", os.ErrNotExist
	}
	releaseInstallProbe.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("lmm:amd64: " + f.exe + "\n"), nil
	}
	err := runSelfUpdate(context.Background())
	var pkgErr *core.PackageManagedError
	require.ErrorAs(t, err, &pkgErr)
	assert.Contains(t, err.Error(), "sudo apt install --only-upgrade lmm")
}

func TestSelfUpdateCmd_UpToDate(t *testing.T) {
	f := newCLISelfUpdate(t)
	version = "2.2.0"
	selfUpdate.yes = true
	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Contains(t, out, "up to date")
	assert.Zero(t, f.downloads)
}

// `lmm service install --auto-update` installs the timer, and refuses for
// a binary self-update could never replace.
func TestServiceInstall_AutoUpdate(t *testing.T) {
	f := newCLISelfUpdate(t)
	unitDir := filepath.Dir(useTestUserUnits(t, f.systemd))
	out := captureStdout(t, func() error {
		return doServiceInstallWith(context.Background(), serviceCmd, "", true)
	})
	assert.FileExists(t, filepath.Join(unitDir, "lmm-update.timer"))
	assert.FileExists(t, filepath.Join(unitDir, "lmm-update.service"))
	assert.Contains(t, out, "lmm-update.timer")
	assert.Contains(t, out, "auto_update is on", "on by default (#586)")

	releaseInstallProbe.LookPath = func(name string) (string, error) {
		if name == "pacman" {
			return "/usr/bin/pacman", nil
		}
		return "", os.ErrNotExist
	}
	releaseInstallProbe.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte("lmm-bin\n"), nil }
	err := doServiceInstallWith(context.Background(), serviceCmd, "", true)
	var pkgErr *core.PackageManagedError
	require.ErrorAs(t, err, &pkgErr)
}

func TestJSONGolden_SelfUpdateCheck(t *testing.T) {
	f := newCLISelfUpdate(t)
	withJSONOutput(t)
	selfUpdate.check = true
	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	out = strings.ReplaceAll(out, "_"+runtime.GOARCH+".tar.gz", "_<ARCH>.tar.gz")
	assertJSONCLIGolden(t, "self_update_check", out, f.exe, "<LMM-BINARY>")
}

// A second self-update while one runs is refused, and replaces nothing;
// the timer's --auto run, which a person's update can overlap, steps aside
// quietly and exits 0.
func TestSelfUpdateCmd_AnotherRunHoldsTheLock(t *testing.T) {
	f := newCLISelfUpdate(t)
	holder, err := newSelfUpdater()
	require.NoError(t, err)
	unlock, err := holder.Lock()
	require.NoError(t, err)
	defer unlock()

	selfUpdate.yes = true
	err = runSelfUpdate(context.Background())
	require.ErrorIs(t, err, core.ErrSelfUpdateRunning)
	assert.Equal(t, "old lmm", f.installed(t))
	assert.Zero(t, f.downloads)

	selfUpdate = selfUpdateFlags{auto: true}
	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Contains(t, out, "another self-update is running")
	assert.Equal(t, "old lmm", f.installed(t))
	assert.Zero(t, f.downloads)

	// --check only reads, so it never waits on the lock.
	selfUpdate = selfUpdateFlags{check: true}
	out = captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Contains(t, out, "2.2.0")
}

// A package database lmm could not get an answer from (locked mid-upgrade,
// or timed out) is no ground to install a timer that might later replace a
// package's binary.
func TestServiceInstall_AutoUpdateRefusesAnUndeterminedInstallMethod(t *testing.T) {
	f := newCLISelfUpdate(t)
	unitDir := filepath.Dir(useTestUserUnits(t, f.systemd))
	releaseInstallProbe.LookPath = func(name string) (string, error) {
		if name == "dpkg" {
			return "/usr/bin/dpkg", nil
		}
		return "", os.ErrNotExist
	}
	releaseInstallProbe.Run = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("dpkg: database is locked")
	}
	err := doServiceInstallWith(context.Background(), serviceCmd, "", true)
	var unknown *core.InstallMethodUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.NoFileExists(t, filepath.Join(unitDir, "lmm-update.timer"))
}

// The detached run and the timer's both step aside while another lmm - a
// CLI command, or `lmm serve`'s job - holds the mutation lock: lmm is not
// replaced, nor lmm-serve.service restarted, under a running deploy, and
// that is no failure to back off from (#586 review).
func TestSelfUpdateCmd_AutoStepsAsideWhileAnLmmMutationRuns(t *testing.T) {
	f := newCLISelfUpdate(t)
	selfUpdate.auto = true
	p, err := app.ResolvePaths(app.Options{ConfigDir: configDir, DataDir: dataDir})
	require.NoError(t, err)
	lock, err := os.OpenFile(app.OpLockPath(p), os.O_RDWR|os.O_CREATE, 0o600)
	require.NoError(t, err)
	defer func() { _ = lock.Close() }()
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))

	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Contains(t, out, "another lmm operation is in progress")
	assert.Contains(t, out, "not updating")
	assert.Equal(t, "old lmm", f.installed(t))
	assert.Zero(t, f.downloads)
	for _, call := range f.systemd.calls {
		assert.NotContains(t, call, "restart", "nor restarted")
	}
	cfg, err := getServiceConfig()
	require.NoError(t, err)
	assert.Nil(t, cfg.AutoUpdateStatus(core.InstallMethod{Method: core.InstallSelfManaged}).Failure)

	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
	captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Equal(t, "new lmm 2.2.0", f.installed(t))
}

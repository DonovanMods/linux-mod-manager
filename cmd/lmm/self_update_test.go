package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

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

func TestSelfUpdateCmd_YesInstallsAndRestartsServe(t *testing.T) {
	f := newCLISelfUpdate(t)
	f.systemd.enabled, f.systemd.active = true, true
	selfUpdate.yes = true

	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Equal(t, "new lmm 2.2.0", f.installed(t))
	assert.Contains(t, out, "2.2.0")
	assert.Contains(t, out, f.exe)
	assert.Contains(t, out, "lmm-serve.service")
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

func TestSelfUpdateCmd_AutoDoesNothingUnlessAutoUpdateIsOn(t *testing.T) {
	f := newCLISelfUpdate(t)
	selfUpdate.auto = true
	out := captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Contains(t, out, "auto_update")
	assert.Equal(t, "old lmm", f.installed(t))
	assert.Zero(t, f.downloads)

	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("auto_update: true\n"), 0o644))
	captureStdout(t, func() error { return runSelfUpdate(context.Background()) })
	assert.Equal(t, "new lmm 2.2.0", f.installed(t), "--auto needs no --yes")
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
	assert.Contains(t, out, "auto_update: true", "it says how to switch updates on")

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

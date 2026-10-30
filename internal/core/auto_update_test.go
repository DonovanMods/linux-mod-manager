package core_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auto_update_test.go - #586's automatic updates: on by default, run by
// the CLI's detached `lmm self-update --auto` and by `lmm serve`, one at a
// time, with a back-off after a failure and a notice after a success. The
// fake GitHub and the key pair are self_update_test.go's.

// clock is a settable time source.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// autoFixture is a self-update fixture with a clock.
func autoFixture(t *testing.T) (*selfUpdateFixture, *clock) {
	t.Helper()
	f := newSelfUpdateFixture(t)
	c := &clock{now: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	f.opts.Now = c.Now
	return f, c
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644))
}

func TestSelfUpdate_AutoUpdateIsOnUnlessConfigSaysOff(t *testing.T) {
	f := newSelfUpdateFixture(t)
	u := core.NewSelfUpdater(withBases(f.opts))
	assert.True(t, u.AutoUpdateEnabled(), "on by default")
	writeConfig(t, f.opts.ConfigDir, "auto_update: false\n")
	assert.False(t, u.AutoUpdateEnabled())
	writeConfig(t, f.opts.ConfigDir, "auto_update: [not a bool\n")
	assert.False(t, u.AutoUpdateEnabled(), "an unreadable config.yaml is no clear yes")
}

func TestRunAuto_InstallsAndRecordsTheFirstUpdate(t *testing.T) {
	f, c := autoFixture(t)
	f.restarts.restarted = true
	u := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t))

	out, err := u.RunAuto(context.Background())
	require.NoError(t, err)
	assert.Empty(t, out.Skipped)
	assert.True(t, out.Result.Updated)
	assert.Equal(t, "2.2.0", out.Result.Version)
	assert.True(t, out.Result.ServeRestarted, "the same verified path as lmm self-update, serve restart included")
	data, err := os.ReadFile(f.exe)
	require.NoError(t, err)
	assert.Equal(t, "new lmm 2.2.0", string(data))

	cfg := core.ServiceConfig{ConfigDir: f.opts.ConfigDir, DataDir: f.opts.DataDir}
	rec, ok := cfg.PendingAutoUpdateNotice()
	require.True(t, ok, "a successful automatic update is waiting to be told")
	assert.Equal(t, core.AutoUpdateRecord{From: "2.1.0", To: "2.2.0", At: c.now, First: true}, rec)
	assert.Equal(t, "lmm updated itself to v2.2.0. To turn this off: auto_update: false in config.yaml, or Setup > Updates.", rec.Notice())
	require.NoError(t, cfg.MarkAutoUpdateNoticed())
	_, ok = cfg.PendingAutoUpdateNotice()
	assert.False(t, ok, "told once")

	assert.False(t, u.AutoUpdateDue("2.2.0"), "never the same version twice: lmm already updated itself to it")
}

func TestRunAuto_ALaterUpdateGetsTheOneLineNotice(t *testing.T) {
	f, c := autoFixture(t)
	u := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t))
	_, err := u.RunAuto(context.Background())
	require.NoError(t, err)
	cfg := core.ServiceConfig{ConfigDir: f.opts.ConfigDir, DataDir: f.opts.DataDir}
	require.NoError(t, cfg.MarkAutoUpdateNoticed())

	// The next release, from the binary the first one installed.
	c.now = c.now.Add(48 * time.Hour)
	f.opts.Version = "2.2.0"
	u = f.updater(newSignedRelease(t, "2.3.0", f.priv, buildTarball(t, tarEntry{name: "lmm", body: "new lmm 2.3.0"})).serve(t))
	out, err := u.RunAuto(context.Background())
	require.NoError(t, err)
	require.True(t, out.Result.Updated)
	rec, ok := cfg.PendingAutoUpdateNotice()
	require.True(t, ok)
	assert.False(t, rec.First)
	assert.Equal(t, "lmm updated itself to v2.3.0.", rec.Notice())
}

func TestRunAuto_DoesNothingWhenTurnedOff(t *testing.T) {
	f, _ := autoFixture(t)
	writeConfig(t, f.opts.ConfigDir, "auto_update: false\n")
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	u := f.updater(rel.serve(t))
	out, err := u.RunAuto(context.Background())
	require.NoError(t, err)
	assert.Contains(t, out.Skipped, "auto_update is off")
	assert.False(t, out.Result.Updated)
	assert.Empty(t, rel.requests, "nothing is asked of GitHub")
	assert.False(t, u.AutoUpdateDue("2.2.0"))
}

func TestRunAuto_StepsAsideForAPackageOrADevBuild(t *testing.T) {
	t.Run("package", func(t *testing.T) {
		f, _ := autoFixture(t)
		f.opts.Probe.LookPath = func(name string) (string, error) {
			if name == "pacman" {
				return "/usr/bin/pacman", nil
			}
			return "", os.ErrNotExist
		}
		f.opts.Probe.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte("lmm-bin\n"), nil }
		out, err := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t)).RunAuto(context.Background())
		require.NoError(t, err, "a package's lmm is never auto-updated, and that is no failure")
		assert.Contains(t, out.Skipped, "pacman")
	})
	t.Run("dev", func(t *testing.T) {
		f, _ := autoFixture(t)
		f.opts.Dev = true
		out, err := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t)).RunAuto(context.Background())
		require.NoError(t, err)
		assert.Contains(t, out.Skipped, "development build")
	})
}

func TestRunAuto_StepsAsideForARunAlreadyUnderWay(t *testing.T) {
	f, _ := autoFixture(t)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	u := f.updater(rel.serve(t))
	unlock, err := u.Lock()
	require.NoError(t, err)
	defer unlock()

	out, err := f.updater(rel.serve(t)).RunAuto(context.Background())
	require.NoError(t, err)
	assert.Contains(t, out.Skipped, core.ErrSelfUpdateRunning.Error())
	data, _ := os.ReadFile(f.exe)
	assert.Equal(t, "old lmm", string(data))
}

func TestRunAuto_BacksOffAFailingVersionForADay(t *testing.T) {
	f, c := autoFixture(t)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	delete(rel.assets, "checksums.txt.sig") // unsigned: refused
	u := f.updater(rel.serve(t))

	_, err := u.RunAuto(context.Background())
	var sigErr *core.ReleaseSignatureError
	require.ErrorAs(t, err, &sigErr)

	cfg := core.ServiceConfig{ConfigDir: f.opts.ConfigDir, DataDir: f.opts.DataDir}
	st := cfg.AutoUpdateStatus(core.InstallMethod{Method: core.InstallSelfManaged})
	require.NotNil(t, st.Failure)
	assert.Equal(t, "2.2.0", st.Failure.Version)
	assert.Equal(t, c.now, st.Failure.At)
	assert.Contains(t, st.Failure.Error, "no signature")

	c.now = c.now.Add(2 * time.Hour)
	assert.False(t, u.AutoUpdateDue("2.2.0"), "the failing version waits a day")
	assert.True(t, u.AutoUpdateDue("2.2.1"), "a newer release is tried at once")
	out, err := u.RunAuto(context.Background())
	require.NoError(t, err, "a backed-off run is no new failure")
	assert.Contains(t, out.Skipped, "2.2.0")
	_, ok := cfg.PendingAutoUpdateNotice()
	assert.False(t, ok)

	c.now = c.now.Add(23 * time.Hour)
	assert.True(t, u.AutoUpdateDue("2.2.0"), "after a day it is tried again")
}

func TestAutoUpdateDue_AnAttemptHoldsOffTheNextForAnHour(t *testing.T) {
	f, c := autoFixture(t)
	u := core.NewSelfUpdater(withBases(f.opts))
	assert.True(t, u.AutoUpdateDue("2.2.0"))
	assert.False(t, u.AutoUpdateDue(""), "no release known, nothing to do")
	require.NoError(t, u.RecordAutoUpdateAttempt())
	assert.False(t, u.AutoUpdateDue("2.2.0"), "a CLI command that just started one does not start another")
	c.now = c.now.Add(61 * time.Minute)
	assert.True(t, u.AutoUpdateDue("2.2.0"), "an attempt that never reported (it died, the network hung) is retried")
}

func TestRunAuto_AnAttemptRecordedByTheCLIDoesNotStopTheRunItStarted(t *testing.T) {
	f, _ := autoFixture(t)
	u := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t))
	require.NoError(t, u.RecordAutoUpdateAttempt())
	out, err := u.RunAuto(context.Background())
	require.NoError(t, err)
	assert.True(t, out.Result.Updated)
}

func TestRunAuto_ACancelledRunRecordsNoFailure(t *testing.T) {
	f, _ := autoFixture(t)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	inner := rel.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	// The download is where a shutting-down server cancels a run.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, ".tar.gz") {
			cancel()
			<-req.Context().Done()
			return
		}
		resp, err := http.Get(inner.URL + req.URL.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	_, err := f.updater(srv).RunAuto(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
	st := core.ServiceConfig{DataDir: f.opts.DataDir, ConfigDir: f.opts.ConfigDir}.AutoUpdateStatus(core.InstallMethod{Method: core.InstallSelfManaged})
	assert.Nil(t, st.Failure, "a server shutting down says nothing about the release")
	data, _ := os.ReadFile(f.exe)
	assert.Equal(t, "old lmm", string(data))
}

func TestAutoUpdateStatus_SaysWhereItApplies(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir()}

	st := cfg.AutoUpdateStatus(core.InstallMethod{Method: core.InstallSelfManaged})
	assert.True(t, st.Enabled)
	assert.True(t, st.Applicable)
	assert.Empty(t, st.Reason)
	assert.Nil(t, st.Last)
	assert.Nil(t, st.Failure)

	st = cfg.AutoUpdateStatus(core.InstallMethod{Method: core.InstallPackage, Manager: "dpkg", Package: "lmm", UpgradeCommand: "sudo apt install --only-upgrade lmm"})
	assert.False(t, st.Applicable)
	assert.Contains(t, st.Reason, "dpkg")
	assert.Contains(t, st.Reason, "sudo apt install --only-upgrade lmm")

	st = cfg.AutoUpdateStatus(core.InstallMethod{Method: core.InstallDev})
	assert.False(t, st.Applicable)
	assert.Contains(t, st.Reason, "development build")

	writeConfig(t, cfg.ConfigDir, "auto_update: false\n")
	assert.False(t, cfg.AutoUpdateStatus(core.InstallMethod{Method: core.InstallSelfManaged}).Enabled)
}

func TestSetAutoUpdate_WritesTheSettingAndKeepsTheRest(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir()}
	writeConfig(t, cfg.ConfigDir, "default_game: skyrim\nupdate_check: false\n")

	require.NoError(t, cfg.SetAutoUpdate(context.Background(), false))
	data, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "auto_update: false")
	assert.Contains(t, string(data), "default_game: skyrim")
	assert.Contains(t, string(data), "update_check: false")
	assert.False(t, cfg.AutoUpdateStatus(core.InstallMethod{Method: core.InstallSelfManaged}).Enabled)

	require.NoError(t, cfg.SetAutoUpdate(context.Background(), true))
	assert.True(t, cfg.AutoUpdateStatus(core.InstallMethod{Method: core.InstallSelfManaged}).Enabled)
}

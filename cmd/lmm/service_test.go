package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliSystemd is a minimal stand-in for systemctl and loginctl (#567): it
// keeps the unit's enabled/active state the way the user manager would,
// so `lmm service` never touches the real one in a test.
type cliSystemd struct {
	enabled, active, missing bool
	calls                    []string
}

func (f *cliSystemd) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "loginctl" {
		return []byte("Linger=no\n"), nil
	}
	if f.missing {
		return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	switch args[1] {
	case "enable":
		f.enabled, f.active = true, f.active || (len(args) > 2 && args[2] == "--now")
	case "disable":
		f.enabled, f.active = false, false
	case "restart":
		f.active = true
	case "is-enabled":
		if f.enabled {
			return []byte("enabled\n"), nil
		}
		return []byte("disabled\n"), &exec.ExitError{}
	case "is-active":
		if f.active {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), &exec.ExitError{}
	}
	return nil, nil
}

// useTestUserUnits points `lmm service` at a temp home and fake for the
// rest of the test, and returns the unit file it would write. A test that
// wants to change the UserUnits further passes edit.
func useTestUserUnits(t *testing.T, fake *cliSystemd, edit ...func(*core.UserUnits)) string {
	t.Helper()
	home := t.TempDir()
	old := newUserUnits
	newUserUnits = func() (*core.UserUnits, error) {
		u := &core.UserUnits{
			Home:         home,
			PackagedDirs: []string{filepath.Join(home, "no-package")},
			Executable:   func() (string, error) { return "/opt/lmm/bin/lmm", nil },
			Run:          fake.run,
			User:         "u",
		}
		for _, e := range edit {
			e(u)
		}
		return u, nil
	}
	// The --config/--data globals reach the unit; another test may have
	// left them set.
	oldConfig, oldData := configDir, dataDir
	configDir, dataDir = "", ""
	t.Cleanup(func() { newUserUnits, configDir, dataDir = old, oldConfig, oldData })
	// With --config/--data cleared, the real auth-status read would open
	// the developer's own installation; a test that wants warnings sets
	// its own.
	useEnvOnlyCredentials(t)
	return filepath.Join(home, ".config", "systemd", "user", "lmm-serve.service")
}

func TestServiceInstall_WritesUnitAndPrintsLingerHint(t *testing.T) {
	fake := &cliSystemd{}
	unitPath := useTestUserUnits(t, fake)

	out := captureStdout(t, func() error {
		return doServiceInstall(context.Background(), serviceCmd, "127.0.0.1:8080")
	})
	raw, err := os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ExecStart=/opt/lmm/bin/lmm serve --no-open --addr 127.0.0.1:8080\n")
	assert.Contains(t, out, unitPath)
	assert.Contains(t, out, "loginctl enable-linger", "the boot-time hint is printed, never run")
	for _, c := range fake.calls {
		assert.NotContains(t, c, "enable-linger")
	}
}

func TestServiceStatus_Human(t *testing.T) {
	fake := &cliSystemd{}
	useTestUserUnits(t, fake)

	out := captureStdout(t, func() error { return doServiceStatus(context.Background()) })
	assert.Contains(t, out, "lmm-serve.service")
	assert.Contains(t, out, "not installed")
	assert.Contains(t, out, "lmm service install")
}

func TestServiceUninstall_Human(t *testing.T) {
	fake := &cliSystemd{}
	unitPath := useTestUserUnits(t, fake)
	captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "") })

	out := captureStdout(t, func() error { return doServiceUninstall(context.Background()) })
	assert.NoFileExists(t, unitPath)
	assert.Contains(t, out, "Removed")

	out = captureStdout(t, func() error { return doServiceUninstall(context.Background()) })
	assert.Contains(t, out, "not installed")
}

func TestJSONGolden_ServiceStatus(t *testing.T) {
	fake := &cliSystemd{}
	unitPath := useTestUserUnits(t, fake)
	captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "") })
	withJSONOutput(t)

	out := captureStdout(t, func() error { return doServiceStatus(context.Background()) })
	assertJSONCLIGolden(t, "service_status", out, unitPath, "<UNIT-PATH>")
}

// No systemctl is a clear error, not a crash.
func TestServiceStatus_NoSystemd(t *testing.T) {
	useTestUserUnits(t, &cliSystemd{missing: true})
	err := doServiceStatus(context.Background())
	require.ErrorIs(t, err, core.ErrSystemdUnavailable)
	assert.Contains(t, err.Error(), "systemctl is not installed")
}

// A service that exits straight after it starts is not reported as running
// at its address.
func TestServiceInstall_ReportsAServiceThatDidNotStayUp(t *testing.T) {
	fake := &cliSystemd{}
	useTestUserUnits(t, fake, func(u *core.UserUnits) {
		u.Sleep = func(context.Context, time.Duration) { fake.active = false }
	})

	out := captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "") })
	assert.Contains(t, out, "started but is not running")
	assert.Contains(t, out, "journalctl --user -u lmm-serve")
	assert.NotContains(t, out, "running at")
}

// A bad --addr is the typed error, and no unit is written.
func TestServiceInstall_RefusesABadAddr(t *testing.T) {
	unitPath := useTestUserUnits(t, &cliSystemd{})
	err := doServiceInstall(context.Background(), serviceCmd, "127.0.0.1:99999")
	var bad *core.InvalidAddrError
	require.ErrorAs(t, err, &bad)
	assert.NoFileExists(t, unitPath)
}

// The --config and --data globals reach the service as serve flags, made
// absolute, since the service does not start in this directory.
func TestServiceInstall_PassesConfigAndDataDirs(t *testing.T) {
	unitPath := useTestUserUnits(t, &cliSystemd{})
	configDir, dataDir = "rel/cfg", "/abs/data"
	wd, err := os.Getwd()
	require.NoError(t, err)

	captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "") })
	raw, err := os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ExecStart=/opt/lmm/bin/lmm serve --no-open --config "+filepath.Join(wd, "rel/cfg")+" --data /abs/data\n")
}

// Disabling removes the enablement link a packaged lmm-serve.service
// shares, and both the help and the output say so.
func TestServiceUninstall_SaysAPackagedUnitIsDisabledToo(t *testing.T) {
	assert.Contains(t, serviceUninstallCmd.Long, "packaged")
	fake := &cliSystemd{}
	var packaged string
	unitPath := useTestUserUnits(t, fake, func(u *core.UserUnits) { packaged = u.PackagedDirs[0] })
	captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "") })
	require.NoError(t, os.MkdirAll(packaged, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(packaged, "lmm-serve.service"), []byte("[Unit]\n"), 0o644))

	out := captureStdout(t, func() error { return doServiceUninstall(context.Background()) })
	assert.NoFileExists(t, unitPath)
	assert.Contains(t, out, "Removed")
	assert.Contains(t, out, "systemctl --user enable --now lmm-serve", "how to turn the packaged one back on")
}

// useEnvOnlyCredentials stands in for the auth-status read `lmm service`
// makes (#582), so the test never opens a real installation.
func useEnvOnlyCredentials(t *testing.T, creds ...core.EnvOnlyCredential) {
	t.Helper()
	old := serviceEnvOnlyCredentials
	serviceEnvOnlyCredentials = func(context.Context) []core.EnvOnlyCredential { return creds }
	t.Cleanup(func() { serviceEnvOnlyCredentials = old })
}

var curseforgeEnvOnly = core.EnvOnlyCredential{SourceID: "curseforge", Name: "CurseForge", EnvVar: "CURSEFORGE_API_KEY"}

// TestServiceInstall_WarnsAboutAKeyOnlyInTheEnvironment (#582): a systemd
// user service does not inherit the login shell, so a key exported there
// is a key lmm-serve will not have. Install says so, once per source, with
// the fix - and never writes the key into the unit.
func TestServiceInstall_WarnsAboutAKeyOnlyInTheEnvironment(t *testing.T) {
	fake := &cliSystemd{}
	unitPath := useTestUserUnits(t, fake)
	useEnvOnlyCredentials(t, curseforgeEnvOnly)
	t.Setenv("CURSEFORGE_API_KEY", "secret-key-value")

	out := captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "") })
	assert.Equal(t, 1, strings.Count(out, "CURSEFORGE_API_KEY"), out)
	assert.Contains(t, out, "CurseForge's key comes only from $CURSEFORGE_API_KEY")
	assert.Contains(t, out, "lmm auth login curseforge")
	assert.Contains(t, out, "Setup > Authentication")

	raw, err := os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "secret-key-value", "a key is never written into the unit")
	assert.NotContains(t, string(raw), "CURSEFORGE_API_KEY")
}

func TestServiceStatus_WarnsAboutAKeyOnlyInTheEnvironment(t *testing.T) {
	useTestUserUnits(t, &cliSystemd{})
	useEnvOnlyCredentials(t, curseforgeEnvOnly)

	out := captureStdout(t, func() error { return doServiceStatus(context.Background()) })
	assert.Contains(t, out, "lmm auth login curseforge")
}

func TestServiceInstall_NoWarningWithoutAnEnvOnlyKey(t *testing.T) {
	useTestUserUnits(t, &cliSystemd{})
	useEnvOnlyCredentials(t)

	out := captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "") })
	assert.NotContains(t, out, "lmm auth login")
}

func TestJSONGolden_ServiceStatusEnvOnlyCredentials(t *testing.T) {
	fake := &cliSystemd{}
	unitPath := useTestUserUnits(t, fake)
	useEnvOnlyCredentials(t, curseforgeEnvOnly)
	withJSONOutput(t)

	out := captureStdout(t, func() error { return doServiceStatus(context.Background()) })
	assertJSONCLIGolden(t, "service_status_env_only_credentials", out, unitPath, "<UNIT-PATH>")
}

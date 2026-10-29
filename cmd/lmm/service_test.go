package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

// useTestUserUnits points `lmm service` at a temp config home and fake
// for the rest of the test, and returns the unit file it would write.
func useTestUserUnits(t *testing.T, fake *cliSystemd) string {
	t.Helper()
	configHome := t.TempDir()
	old := newUserUnits
	newUserUnits = func() (*core.UserUnits, error) {
		return &core.UserUnits{
			ConfigHome:   configHome,
			PackagedDirs: []string{filepath.Join(configHome, "no-package")},
			Executable:   func() (string, error) { return "/opt/lmm/bin/lmm", nil },
			Run:          fake.run,
			User:         "u",
		}, nil
	}
	t.Cleanup(func() { newUserUnits = old })
	return filepath.Join(configHome, "systemd", "user", "lmm-serve.service")
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

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serviceBinaries is two lmm builds for the #649 tests: the one a unit was
// installed from and the one now running - a mise Go upgrade's two
// install directories.
type serviceBinaries struct {
	root, old, cur string
}

// newServiceBinaries writes both as empty files under a temp root.
func newServiceBinaries(t *testing.T) serviceBinaries {
	t.Helper()
	root := t.TempDir()
	b := serviceBinaries{root: root,
		old: filepath.Join(root, "go", "1.27.3", "bin", "lmm"),
		cur: filepath.Join(root, "go", "1.27.4", "bin", "lmm"),
	}
	for _, p := range []string{b.old, b.cur} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, nil, 0o755))
	}
	return b
}

// oldBinaryVersion is the version the unit's binary reports.
const oldBinaryVersion = "2.2.0 (dev: v2.2.0-39-gdef)"

// useBinaries makes the running binary exe and answers --version for the
// old build; any other path fails, as a binary that will not run does.
func (b serviceBinaries) useBinaries(exe *string) func(*core.UserUnits) {
	return func(u *core.UserUnits) {
		u.Executable = func() (string, error) { return *exe, nil }
		u.BinaryVersion = func(_ context.Context, path string) (string, error) {
			if path == b.old {
				return oldBinaryVersion, nil
			}
			if path == b.cur {
				return computeDisplayVersion(version, buildDescribe), nil
			}
			return "", errors.New("no such binary")
		}
	}
}

// scrub replaces the temp paths and this build's version in JSON output.
func (b serviceBinaries) scrub(unitPath string) []string {
	return []string{unitPath, "<UNIT-PATH>", b.root, "<BIN>",
		`"` + computeDisplayVersion(version, buildDescribe) + `"`, `"<VERSION>"`}
}

// installedOnOld installs lmm-serve.service from the old build, then makes
// the new build the running one.
func installedOnOld(t *testing.T, fake *cliSystemd) (serviceBinaries, string) {
	t.Helper()
	b := newServiceBinaries(t)
	exe := b.old
	unitPath := useTestUserUnits(t, fake, b.useBinaries(&exe))
	captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "127.0.0.1:8080") })
	exe = b.cur
	return b, unitPath
}

// #649: status names the binary the unit runs and warns, with the remedy,
// when it is not the running one.
func TestServiceStatus_WarnsAboutAnotherBinary(t *testing.T) {
	b, _ := installedOnOld(t, &cliSystemd{})

	out := captureStdout(t, func() error { return doServiceStatus(context.Background()) })
	assert.Contains(t, out, "binary:     "+b.old+" ("+oldBinaryVersion+")")
	assert.Contains(t, out, "lmm-serve.service runs "+b.old+" ("+oldBinaryVersion+"); you are running "+b.cur+" ("+computeDisplayVersion(version, buildDescribe)+").")
	assert.Contains(t, out, "lmm service restart --switch")
}

func TestServiceStatus_NoWarningOnTheSameBinary(t *testing.T) {
	b := newServiceBinaries(t)
	exe := b.cur
	useTestUserUnits(t, &cliSystemd{}, b.useBinaries(&exe))
	captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "") })

	out := captureStdout(t, func() error { return doServiceStatus(context.Background()) })
	assert.Contains(t, out, "binary:     "+b.cur)
	assert.NotContains(t, out, "you are running")
}

// A package's unit is never rewritten, so its warning names the ways to run
// this lmm instead.
func TestServiceStatus_PackagedUnitRunningAnotherBinary(t *testing.T) {
	b := newServiceBinaries(t)
	exe := b.cur
	packaged := t.TempDir()
	useTestUserUnits(t, &cliSystemd{}, b.useBinaries(&exe), func(u *core.UserUnits) { u.PackagedDirs = []string{packaged} })
	require.NoError(t, os.WriteFile(filepath.Join(packaged, "lmm-serve.service"), []byte("[Service]\nExecStart="+b.old+" serve --no-open\n"), 0o644))

	out := captureStdout(t, func() error { return doServiceStatus(context.Background()) })
	assert.Contains(t, out, "lmm-serve.service runs "+b.old)
	assert.Contains(t, out, "systemctl --user edit lmm-serve")
	assert.NotContains(t, out, "--switch")
}

func TestJSONGolden_ServiceStatusBinaryMismatch(t *testing.T) {
	b, unitPath := installedOnOld(t, &cliSystemd{})
	withJSONOutput(t)
	out := captureStdout(t, func() error { return doServiceStatus(context.Background()) })
	assertJSONCLIGolden(t, "service_status_binary_mismatch", out, b.scrub(unitPath)...)
}

// Restart warns and restarts the unit as it is; it rewrites nothing.
func TestServiceRestart_WarnsAndRewritesNothing(t *testing.T) {
	fake := &cliSystemd{}
	b, unitPath := installedOnOld(t, fake)
	before, err := os.ReadFile(unitPath)
	require.NoError(t, err)

	out := captureStdout(t, func() error { return doServiceRestart(context.Background()) })
	assert.Contains(t, out, "Restarted")
	assert.Contains(t, out, "lmm-serve.service runs "+b.old)
	assert.Contains(t, out, "lmm service restart --switch")
	after, err := os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestJSONGolden_ServiceRestartBinaryMismatch(t *testing.T) {
	b, unitPath := installedOnOld(t, &cliSystemd{})
	withJSONOutput(t)
	out := captureStdout(t, func() error { return doServiceRestart(context.Background()) })
	assertJSONCLIGolden(t, "service_restart_binary_mismatch", out, b.scrub(unitPath)...)
}

// --switch points the unit at this binary, keeping its --addr, then
// restarts it; the flag is registered on the command.
func TestServiceRestartSwitch(t *testing.T) {
	require.NotNil(t, serviceRestartCmd.Flags().Lookup("switch"))
	fake := &cliSystemd{}
	b, unitPath := installedOnOld(t, fake)
	fake.calls = nil

	out := captureStdout(t, func() error { return doServiceRestartSwitch(context.Background()) })
	assert.Contains(t, out, "Pointed lmm-serve.service at "+b.cur+" (was "+b.old+").")
	assert.Contains(t, out, "Restarted")
	assert.NotContains(t, out, "you are running")
	raw, err := os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ExecStart="+b.cur+" serve --no-open --addr 127.0.0.1:8080\n")
	assert.Contains(t, fake.calls, "systemctl --user daemon-reload")
	assert.Contains(t, fake.calls, "systemctl --user restart lmm-serve.service")
}

func TestJSONGolden_ServiceRestartSwitch(t *testing.T) {
	b, unitPath := installedOnOld(t, &cliSystemd{})
	withJSONOutput(t)
	out := captureStdout(t, func() error { return doServiceRestartSwitch(context.Background()) })
	assertJSONCLIGolden(t, "service_restart_switch", out, b.scrub(unitPath)...)
}

// Install over a unit on another binary says what it changed; over one
// that needs nothing, it still says "Already installed".
func TestServiceInstall_SaysWhatChanged(t *testing.T) {
	fake := &cliSystemd{}
	b, unitPath := installedOnOld(t, fake)

	out := captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "127.0.0.1:8080") })
	assert.Contains(t, out, "Updated lmm-serve.service to run "+b.cur+" (was "+b.old+"); restarted.")
	assert.NotContains(t, out, "Already installed")

	out = captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "127.0.0.1:8080") })
	assert.Contains(t, out, "Already installed lmm-serve.service at "+unitPath)
	assert.NotContains(t, out, "Updated")

	out = captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "127.0.0.1:9000") })
	assert.Contains(t, out, "Updated lmm-serve.service at "+unitPath+"; restarted.")
}

func TestJSONGolden_ServiceInstallUpdated(t *testing.T) {
	b, unitPath := installedOnOld(t, &cliSystemd{})
	withJSONOutput(t)
	out := captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "127.0.0.1:8080") })
	assertJSONCLIGolden(t, "service_install_updated", out, b.scrub(unitPath)...)
}

// An install that changed nothing emits the document it always did: no
// change, no binaries.
func TestJSONGolden_ServiceInstallUnchanged(t *testing.T) {
	b := newServiceBinaries(t)
	exe := b.cur
	unitPath := useTestUserUnits(t, &cliSystemd{}, b.useBinaries(&exe))
	captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "") })
	withJSONOutput(t)
	out := captureStdout(t, func() error { return doServiceInstall(context.Background(), serviceCmd, "") })
	assert.False(t, strings.Contains(out, "binary"), out)
	assertJSONCLIGolden(t, "service_install_unchanged", out, b.scrub(unitPath)...)
}

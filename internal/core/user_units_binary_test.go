package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/packaging/systemd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBinary writes an empty executable at dir/rel and returns its path.
func fakeBinary(t *testing.T, dir, rel string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, nil, 0o755))
	return path
}

// versionsByPath is a BinaryVersion seam that answers from a table and
// fails for any path it does not know, as a binary that will not run does.
func versionsByPath(versions map[string]string) func(context.Context, string) (string, error) {
	return func(_ context.Context, path string) (string, error) {
		if v, ok := versions[path]; ok {
			return v, nil
		}
		return "", errors.New("exec: no such file")
	}
}

// writeOwnServeUnit writes the unit `lmm service install` renders for exe
// at unitPath.
func writeOwnServeUnit(t *testing.T, unitPath, exe string, opts core.ServiceInstallOptions) []byte {
	t.Helper()
	raw, err := core.RenderServeUnit(exe, opts)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(unitPath), 0o755))
	require.NoError(t, os.WriteFile(unitPath, raw, 0o644))
	return raw
}

// #649: status names the binary the unit runs and its version, beside the
// running binary, and flags a unit that runs a different file - the mise
// Go upgrade that left lmm-serve.service on the 1.27.3 build.
func TestUserUnits_StatusReportsTheUnitsBinary(t *testing.T) {
	bins := t.TempDir()
	old := fakeBinary(t, bins, "go/1.27.3/bin/lmm")
	cur := fakeBinary(t, bins, "go/1.27.4/bin/lmm")
	fake := &fakeSystemd{enabled: true, active: true}
	units, unitPath := newTestUserUnits(t, fake, cur)
	units.Version = "2.3.0 (dev: v2.3.0-484-gabc)"
	units.BinaryVersion = versionsByPath(map[string]string{old: "2.3.0 (dev: v2.3.0-39-gdef)", cur: "2.3.0 (dev: v2.3.0-484-gabc)"})
	writeOwnServeUnit(t, unitPath, old, core.ServiceInstallOptions{Addr: "127.0.0.1:8080"})

	report, err := units.Status(context.Background())
	require.NoError(t, err)
	require.Len(t, report.Units, 1)
	assert.Equal(t, &core.LmmBinary{Path: old, Version: "2.3.0 (dev: v2.3.0-39-gdef)"}, report.Units[0].Binary)
	assert.True(t, report.Units[0].BinaryMismatch)
	assert.Equal(t, &core.LmmBinary{Path: cur, Version: "2.3.0 (dev: v2.3.0-484-gabc)"}, report.Running)
	assert.False(t, report.Changed)
}

// The same file reached through a symlink is the same binary: no mismatch.
func TestUserUnits_StatusSymlinkedBinaryIsNoMismatch(t *testing.T) {
	bins := t.TempDir()
	real := fakeBinary(t, bins, "real/lmm")
	link := filepath.Join(bins, "bin", "lmm")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(real, link))
	units, unitPath := newTestUserUnits(t, &fakeSystemd{}, real)
	units.Version = "2.3.0"
	units.BinaryVersion = versionsByPath(map[string]string{link: "2.3.0"})
	writeOwnServeUnit(t, unitPath, link, core.ServiceInstallOptions{})

	report, err := units.Status(context.Background())
	require.NoError(t, err)
	require.NotNil(t, report.Units[0].Binary)
	assert.Equal(t, link, report.Units[0].Binary.Path, "as the unit names it")
	assert.False(t, report.Units[0].BinaryMismatch)
}

// A package's unit runs the packaged path: no warning when that is the
// running binary, a warning for a development build run from elsewhere.
func TestUserUnits_StatusPackagedUnit(t *testing.T) {
	bins := t.TempDir()
	packagedBin := fakeBinary(t, bins, "usr/bin/lmm")
	devBuild := fakeBinary(t, bins, "home/u/go/bin/lmm")
	unit := strings.Replace(string(systemd.ServeUnit), "ExecStart=/usr/bin/lmm ", "ExecStart="+packagedBin+" ", 1)

	for name, tc := range map[string]struct {
		running  string
		mismatch bool
	}{
		"the packaged binary":        {packagedBin, false},
		"a dev build from elsewhere": {devBuild, true},
	} {
		t.Run(name, func(t *testing.T) {
			units, _ := newTestUserUnits(t, &fakeSystemd{}, tc.running)
			units.Version = "2.3.0"
			units.BinaryVersion = versionsByPath(map[string]string{packagedBin: "2.3.0"})
			require.NoError(t, os.WriteFile(filepath.Join(units.PackagedDirs[0], systemd.ServeUnitName), []byte(unit), 0o644))

			report, err := units.Status(context.Background())
			require.NoError(t, err)
			s := report.Units[0]
			assert.Equal(t, core.UnitOriginPackage, s.Origin)
			assert.Equal(t, &core.LmmBinary{Path: packagedBin, Version: "2.3.0"}, s.Binary)
			assert.Equal(t, tc.mismatch, s.BinaryMismatch)
		})
	}
}

// A binary that will not say its version - gone, hung, not lmm - is
// "unknown", never a failed status; a binary that is gone is a mismatch.
func TestUserUnits_StatusUnknownVersion(t *testing.T) {
	cur := fakeBinary(t, t.TempDir(), "lmm")
	units, unitPath := newTestUserUnits(t, &fakeSystemd{}, cur)
	units.BinaryVersion = versionsByPath(nil)
	writeOwnServeUnit(t, unitPath, "/gone/bin/lmm", core.ServiceInstallOptions{})

	report, err := units.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, &core.LmmBinary{Path: "/gone/bin/lmm", Version: core.UnknownBinaryVersion}, report.Units[0].Binary)
	assert.True(t, report.Units[0].BinaryMismatch)
	assert.Equal(t, &core.LmmBinary{Path: cur, Version: core.UnknownBinaryVersion}, report.Running, "no Version set")
}

// What systemd itself will run - a drop-in's ExecStart included - wins
// over the unit file's line when the user manager answers.
func TestUserUnits_StatusPrefersSystemdsExecStart(t *testing.T) {
	bins := t.TempDir()
	cur := fakeBinary(t, bins, "new/lmm")
	override := fakeBinary(t, bins, "override/lmm")
	fake := &fakeSystemd{execStart: map[string]string{
		systemd.ServeUnitName: "{ path=" + override + " ; argv[]=" + override + " serve --no-open ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }",
	}}
	units, unitPath := newTestUserUnits(t, fake, cur)
	units.BinaryVersion = versionsByPath(map[string]string{override: "2.2.0"})
	writeOwnServeUnit(t, unitPath, cur, core.ServiceInstallOptions{})

	report, err := units.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, &core.LmmBinary{Path: override, Version: "2.2.0"}, report.Units[0].Binary)
	assert.True(t, report.Units[0].BinaryMismatch)
}

// No BinaryVersion seam reports no binaries at all: the document stays
// what it was before #649 for a caller that does not ask.
func TestUserUnits_StatusWithoutTheSeamReportsNoBinaries(t *testing.T) {
	units, unitPath := newTestUserUnits(t, &fakeSystemd{}, "/new/lmm")
	writeOwnServeUnit(t, unitPath, "/old/lmm", core.ServiceInstallOptions{})
	report, err := units.Status(context.Background())
	require.NoError(t, err)
	assert.Nil(t, report.Running)
	assert.Nil(t, report.Units[0].Binary)
	assert.False(t, report.Units[0].BinaryMismatch)
}

// The auto-update service runs a binary too, and is reported the same way;
// the timer runs none.
func TestUserUnits_StatusReportsTheUpdateServicesBinary(t *testing.T) {
	bins := t.TempDir()
	old := fakeBinary(t, bins, "old/lmm")
	cur := fakeBinary(t, bins, "new/lmm")
	fake := &fakeSystemd{}
	units, _ := newTestUserUnits(t, fake, old)
	_, err := units.Install(context.Background(), core.ServiceInstallOptions{AutoUpdate: true})
	require.NoError(t, err)
	units.Executable = func() (string, error) { return cur, nil }
	units.BinaryVersion = versionsByPath(map[string]string{old: "2.2.0", cur: "2.3.0"})

	report, err := units.Status(context.Background())
	require.NoError(t, err)
	require.Len(t, report.Units, 3)
	for _, s := range report.Units {
		if s.Name == core.UpdateTimerName {
			assert.Nil(t, s.Binary, "a timer runs no binary")
			continue
		}
		assert.Equal(t, &core.LmmBinary{Path: old, Version: "2.2.0"}, s.Binary, s.Name)
		assert.True(t, s.BinaryMismatch, s.Name)
	}
}

// Restart reports the mismatch and rewrites nothing: switching binaries
// stays an explicit choice.
func TestUserUnits_RestartReportsAMismatchAndRewritesNothing(t *testing.T) {
	bins := t.TempDir()
	old := fakeBinary(t, bins, "old/lmm")
	cur := fakeBinary(t, bins, "new/lmm")
	fake := &fakeSystemd{enabled: true, active: true}
	units, unitPath := newTestUserUnits(t, fake, cur)
	units.BinaryVersion = versionsByPath(map[string]string{old: "2.2.0"})
	raw := writeOwnServeUnit(t, unitPath, old, core.ServiceInstallOptions{})

	report, err := units.Restart(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"restart " + systemd.ServeUnitName}, fake.verbs())
	assert.True(t, report.Units[0].BinaryMismatch)
	assert.Equal(t, old, report.Units[0].Binary.Path)
	got, err := os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.Equal(t, raw, got)
}

// RestartSwitch points lmm's own units at the running binary, keeping
// every other argument and setting, then reloads and restarts.
func TestUserUnits_RestartSwitchRewritesTheBinaryOnly(t *testing.T) {
	bins := t.TempDir()
	old := fakeBinary(t, bins, "go/1.27.3/bin/lmm")
	cur := fakeBinary(t, bins, "go/1.27.4 x/bin/lmm")
	fake := &fakeSystemd{enabled: true, active: true}
	units, unitPath := newTestUserUnits(t, fake, old)
	ctx := context.Background()
	opts := core.ServiceInstallOptions{Addr: "127.0.0.1:8080", ConfigDir: "/cfg", Environment: []string{"XDG_DATA_HOME=/d"}, AutoUpdate: true}
	_, err := units.Install(ctx, opts)
	require.NoError(t, err)

	units.Executable = func() (string, error) { return cur, nil }
	units.Version = "2.3.0"
	units.BinaryVersion = versionsByPath(map[string]string{old: "2.2.0", cur: "2.3.0"})
	fake.calls = nil
	report, err := units.RestartSwitch(ctx)
	require.NoError(t, err)

	want, err := core.RenderServeUnit(cur, opts)
	require.NoError(t, err)
	got, err := os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "the unit install would write for this binary")
	upd, err := os.ReadFile(filepath.Join(filepath.Dir(unitPath), core.UpdateServiceName))
	require.NoError(t, err)
	assert.Contains(t, string(upd), "ExecStart=\""+cur+"\" self-update --auto --config /cfg\n")

	assert.Equal(t, []string{"daemon-reload", "restart " + systemd.ServeUnitName}, fake.verbs())
	assert.True(t, report.Changed)
	byName := map[string]core.UnitStatus{}
	for _, s := range report.Units {
		byName[s.Name] = s
	}
	serve := byName[systemd.ServeUnitName]
	assert.Equal(t, core.UnitUpdated, serve.Change)
	assert.Equal(t, old, serve.PreviousBinary)
	assert.Equal(t, &core.LmmBinary{Path: cur, Version: "2.3.0"}, serve.Binary)
	assert.False(t, serve.BinaryMismatch)
	assert.Equal(t, old, byName[core.UpdateServiceName].PreviousBinary)
	assert.Empty(t, byName[core.UpdateTimerName].Change, "the timer runs no binary")

	// Already on this binary: a plain restart, nothing written.
	fake.calls = nil
	report, err = units.RestartSwitch(ctx)
	require.NoError(t, err)
	assert.False(t, report.Changed)
	assert.Equal(t, []string{"restart " + systemd.ServeUnitName}, fake.verbs())
}

// A package's unit is never rewritten: running its own binary it is just
// restarted, and running another one the switch is refused, with the way
// out, before anything is run.
func TestUserUnits_RestartSwitchPackagedUnit(t *testing.T) {
	bins := t.TempDir()
	packagedBin := fakeBinary(t, bins, "usr/bin/lmm")
	devBuild := fakeBinary(t, bins, "home/u/go/bin/lmm")
	unit := strings.Replace(string(systemd.ServeUnit), "ExecStart=/usr/bin/lmm ", "ExecStart="+packagedBin+" ", 1)
	ctx := context.Background()

	fake := &fakeSystemd{}
	units, unitPath := newTestUserUnits(t, fake, packagedBin)
	units.BinaryVersion = versionsByPath(nil)
	packaged := filepath.Join(units.PackagedDirs[0], systemd.ServeUnitName)
	require.NoError(t, os.WriteFile(packaged, []byte(unit), 0o644))
	_, err := units.RestartSwitch(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"restart " + systemd.ServeUnitName}, fake.verbs())

	fake.calls = nil
	units.Executable = func() (string, error) { return devBuild, nil }
	_, err = units.RestartSwitch(ctx)
	var pkgErr *core.PackagedSwitchError
	require.ErrorAs(t, err, &pkgErr)
	assert.Equal(t, packagedBin, pkgErr.Binary)
	assert.Equal(t, devBuild, pkgErr.Running)
	assert.Contains(t, err.Error(), "systemctl --user edit lmm-serve")
	assert.Empty(t, fake.verbs())
	assert.NoFileExists(t, unitPath, "no copy shadows the package's")
}

// A unit lmm did not write is not rewritten; nothing installed is the
// restart's own error.
func TestUserUnits_RestartSwitchRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("foreign unit", func(t *testing.T) {
		fake := &fakeSystemd{}
		units, unitPath := newTestUserUnits(t, fake, "/new/lmm")
		require.NoError(t, os.MkdirAll(filepath.Dir(unitPath), 0o755))
		require.NoError(t, os.WriteFile(unitPath, []byte("[Service]\nExecStart=/old/lmm serve\n"), 0o644))
		_, err := units.RestartSwitch(ctx)
		var foreign *core.ForeignUnitError
		require.ErrorAs(t, err, &foreign)
		assert.Empty(t, fake.verbs())
	})
	t.Run("not installed", func(t *testing.T) {
		fake := &fakeSystemd{}
		units, _ := newTestUserUnits(t, fake, "/new/lmm")
		_, err := units.RestartSwitch(ctx)
		require.ErrorIs(t, err, core.ErrServiceNotInstalled)
		assert.Empty(t, fake.verbs())
	})
	t.Run("go run build", func(t *testing.T) {
		fake := &fakeSystemd{}
		units, unitPath := newTestUserUnits(t, fake, "/tmp/go-build1/b001/exe/lmm")
		writeOwnServeUnit(t, unitPath, "/old/lmm", core.ServiceInstallOptions{})
		_, err := units.RestartSwitch(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "go run")
		assert.Empty(t, fake.verbs())
	})
}

// Install over an existing unit says what it changed: the binary it now
// runs and the one it ran, per unit, and nothing at all when nothing
// changed.
func TestUserUnits_InstallReportsWhatChanged(t *testing.T) {
	bins := t.TempDir()
	old := fakeBinary(t, bins, "old/lmm")
	cur := fakeBinary(t, bins, "new/lmm")
	fake := &fakeSystemd{}
	units, _ := newTestUserUnits(t, fake, old)
	units.Version = "2.3.0"
	ctx := context.Background()

	report, err := units.Install(ctx, core.ServiceInstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.UnitInstalled, report.Units[0].Change)
	assert.Empty(t, report.Units[0].PreviousBinary)
	assert.False(t, report.Units[0].Restarted, "started, not restarted")

	report, err = units.Install(ctx, core.ServiceInstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.UnitStatus{
		Name: systemd.ServeUnitName, Installed: true, Origin: core.UnitOriginUser, Path: report.Units[0].Path,
		Enabled: true, Active: true, EnabledState: "enabled", ActiveState: "active", Restarted: true,
	}, report.Units[0], "unchanged: no change, no binaries")
	assert.Nil(t, report.Running)

	units.Executable = func() (string, error) { return cur, nil }
	report, err = units.Install(ctx, core.ServiceInstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.UnitUpdated, report.Units[0].Change)
	assert.Equal(t, old, report.Units[0].PreviousBinary)
	assert.Equal(t, &core.LmmBinary{Path: cur, Version: "2.3.0"}, report.Units[0].Binary)
	assert.True(t, report.Units[0].Restarted)

	report, err = units.Install(ctx, core.ServiceInstallOptions{Addr: "127.0.0.1:9000"})
	require.NoError(t, err)
	assert.Equal(t, core.UnitUpdated, report.Units[0].Change, "a new --addr")
	assert.Empty(t, report.Units[0].PreviousBinary, "on the same binary")
	assert.Nil(t, report.Units[0].Binary)
}

// The real BinaryVersion runs `<path> --version` and reads lmm's line; a
// program that is not lmm, or fails, is an error the report turns into
// "unknown".
func TestNewUserUnits_BinaryVersionRunsTheBinary(t *testing.T) {
	units, err := core.NewUserUnits()
	require.NoError(t, err)
	require.NotNil(t, units.BinaryVersion)
	dir := t.TempDir()
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
		return p
	}
	ctx := context.Background()

	v, err := units.BinaryVersion(ctx, script("lmm", `[ "$1" = --version ] && echo "lmm version 2.3.0 (dev: v2.3.0-484-gabc)"`))
	require.NoError(t, err)
	assert.Equal(t, "2.3.0 (dev: v2.3.0-484-gabc)", v)

	_, err = units.BinaryVersion(ctx, script("other", `echo "something else 1.0"`))
	require.Error(t, err)
	_, err = units.BinaryVersion(ctx, script("fails", `exit 2`))
	require.Error(t, err)
	_, err = units.BinaryVersion(ctx, filepath.Join(dir, "missing"))
	require.Error(t, err)
}

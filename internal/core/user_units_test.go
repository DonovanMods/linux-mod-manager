package core_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/packaging/systemd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateUnitGoldens re-records testdata/systemd/*.golden, the rendered
// unit files (#567).
var updateUnitGoldens = flag.Bool("update-unit-goldens", false, "rewrite internal/core/testdata/systemd/*.golden")

// fakeSystemd stands in for systemctl and loginctl (#567): it records every
// command and answers is-enabled/is-active from its own state, which enable,
// disable, start and restart change the way the real user manager would. No
// test touches the real user manager.
type fakeSystemd struct {
	calls    []string
	missing  bool  // systemctl is not installed
	noBus    error // the user manager cannot be reached
	enabled  bool
	active   bool
	linger   string
	failVerb string            // a systemctl verb that fails
	env      map[string]string // what show-environment prints
	unitPath string            // what show -p UnitPath --value prints
	// others is the enabled/active state of every unit but
	// lmm-serve.service, whose state is enabled and active above (#569).
	others map[string]*[2]bool
}

// state returns the enabled and active flags the verb's unit argument
// names: lmm-serve.service's own fields, or its entry in others.
func (f *fakeSystemd) state(args []string) (enabled, active *bool) {
	unit := args[len(args)-1]
	if unit == systemd.ServeUnitName || (!strings.HasSuffix(unit, ".service") && !strings.HasSuffix(unit, ".timer")) {
		return &f.enabled, &f.active
	}
	if f.others == nil {
		f.others = map[string]*[2]bool{}
	}
	if f.others[unit] == nil {
		f.others[unit] = &[2]bool{}
	}
	return &f.others[unit][0], &f.others[unit][1]
}

func (f *fakeSystemd) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	switch name {
	case "loginctl":
		if f.linger == "" {
			return nil, errors.New("exit status 1: User u is not logged in or lingering")
		}
		return []byte("Linger=" + f.linger + "\n"), nil
	case "systemctl":
	default:
		return nil, fmt.Errorf("unexpected command %q", line)
	}
	if f.missing {
		return nil, &exec.Error{Name: "systemctl", Err: exec.ErrNotFound}
	}
	if f.noBus != nil {
		return nil, f.noBus
	}
	if len(args) < 2 || args[0] != "--user" {
		return nil, fmt.Errorf("systemctl without --user: %q", line)
	}
	verb := args[1]
	if verb == f.failVerb {
		return nil, fmt.Errorf("exit status 1: %s failed", verb)
	}
	now := len(args) > 2 && args[2] == "--now"
	enabled, active := f.state(args)
	switch verb {
	case "show":
		if len(args) > 3 && args[2] == "-p" && args[3] == "UnitPath" {
			return []byte(f.unitPath + "\n"), nil
		}
	case "show-environment":
		keys := make([]string, 0, len(f.env))
		for k := range f.env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(k + "=" + f.env[k] + "\n")
		}
		return []byte(b.String()), nil
	case "daemon-reload":
	case "enable":
		*enabled = true
		*active = *active || now
	case "disable":
		*enabled = false
		*active = *active && !now
	case "start", "restart":
		*active = true
	case "stop":
		*active = false
	case "try-restart":
	case "is-enabled":
		if *enabled {
			return []byte("enabled\n"), nil
		}
		return []byte("disabled\n"), errors.New("exit status 1")
	case "is-active":
		if *active {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit status 3")
	default:
		return nil, fmt.Errorf("unexpected systemctl verb %q", verb)
	}
	return nil, nil
}

// verbs returns the systemctl verbs run, in order, without the queries.
func (f *fakeSystemd) verbs() []string {
	var out []string
	for _, c := range f.calls {
		fields := strings.Fields(c)
		if fields[0] != "systemctl" || len(fields) < 3 {
			continue
		}
		switch fields[2] {
		case "show", "show-environment", "is-enabled", "is-active":
			continue
		}
		out = append(out, strings.Join(fields[2:], " "))
	}
	return out
}

// newTestUserUnits returns UserUnits over a temp home, an empty
// packaged-unit directory and fake, with the running binary at exe, an
// empty shell environment, no PATH lookup and no wait after a start.
func newTestUserUnits(t *testing.T, fake *fakeSystemd, exe string) (*core.UserUnits, string) {
	t.Helper()
	home := t.TempDir()
	return &core.UserUnits{
		Home:         home,
		PackagedDirs: []string{t.TempDir()},
		Executable:   func() (string, error) { return exe, nil },
		Run:          fake.run,
		User:         "u",
	}, filepath.Join(home, ".config", "systemd", "user", systemd.ServeUnitName)
}

// The packaged unit and the rendered one come from the same embedded file:
// rendering for /usr/bin/lmm with no --addr gives back the packaged
// [Unit]/[Service]/[Install] sections unchanged.
func TestRenderServeUnit_PackagedBinaryMatchesShippedFile(t *testing.T) {
	got, err := core.RenderServeUnit("/usr/bin/lmm", core.ServiceInstallOptions{})
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(string(got), string(systemd.ServeUnit)),
		"the rendered unit is the shipped file behind one header line")
	assert.Contains(t, string(systemd.ServeUnit), "\nExecStart=/usr/bin/lmm serve --no-open\n")
	assert.Contains(t, string(systemd.ServeUnit), "\nRestart=on-failure\n")
	assert.Contains(t, string(systemd.ServeUnit), "\nWantedBy=default.target\n")
	assert.Contains(t, string(systemd.ServeUnit), "\nNoNewPrivileges=yes\n")
	for line := range strings.SplitSeq(string(systemd.ServeUnit), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		for _, banned := range []string{"ProtectHome", "ProtectSystem", "PrivateTmp", "ReadOnlyPaths", "InaccessiblePaths"} {
			assert.NotContains(t, line, banned+"=", "nothing may narrow where lmm can deploy")
		}
	}
}

func TestRenderServeUnit_Golden(t *testing.T) {
	got, err := core.RenderServeUnit("/home/u/go/bin/lmm", core.ServiceInstallOptions{Addr: "127.0.0.1:8080"})
	require.NoError(t, err)
	path := filepath.Join("testdata", "systemd", "lmm-serve.service.golden")
	if *updateUnitGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run with -update-unit-goldens to record it")
	assert.Equal(t, string(want), string(got))
}

// ExecStart quotes a path systemd would split and escapes the specifiers
// and variables it would expand.
func TestRenderServeUnit_QuotesExecStart(t *testing.T) {
	got, err := core.RenderServeUnit(`/opt/my games/lm"m%$`, core.ServiceInstallOptions{Addr: "[::1]:7420"})
	require.NoError(t, err)
	assert.Contains(t, string(got), "\nExecStart=\"/opt/my games/lm\\\"m%%$$\" serve --no-open --addr [::1]:7420\n")

	_, err = core.RenderServeUnit("/opt/lmm\nExecStartPre=/bin/rm", core.ServiceInstallOptions{})
	require.Error(t, err, "a newline would inject a directive")
	_, err = core.RenderServeUnit("lmm", core.ServiceInstallOptions{})
	require.Error(t, err, "systemd needs an absolute path")
}

func TestUserUnits_InstallWritesEnablesAndStarts(t *testing.T) {
	fake := &fakeSystemd{linger: "no"}
	exeDir := t.TempDir()
	real := filepath.Join(exeDir, "lmm")
	require.NoError(t, os.WriteFile(real, nil, 0o755))
	link := filepath.Join(exeDir, "lmm-link")
	require.NoError(t, os.Symlink(real, link))
	units, unitPath := newTestUserUnits(t, fake, real)
	units.LookPath = func(string) (string, error) { return link, nil }

	report, err := units.Install(context.Background(), core.ServiceInstallOptions{Addr: "127.0.0.1:8080"})
	require.NoError(t, err)

	raw, err := os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "\nExecStart="+link+" serve --no-open --addr 127.0.0.1:8080\n", "the lmm on PATH, when it is this binary")
	info, err := os.Stat(unitPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	assert.Equal(t, []string{"daemon-reload", "enable --now " + systemd.ServeUnitName}, fake.verbs())
	assert.True(t, report.Changed)
	require.Len(t, report.Units, 1)
	assert.Equal(t, core.UnitStatus{
		Name: systemd.ServeUnitName, Installed: true, Origin: core.UnitOriginUser, Path: unitPath,
		Enabled: true, Active: true, EnabledState: "enabled", ActiveState: "active",
	}, report.Units[0])
	assert.False(t, report.Linger)
}

// Installing again restarts a running service whether or not the file
// changed, so neither a new --addr nor an install interrupted between the
// write and the restart leaves an old ExecStart running; a service that is
// not running is just enabled and started.
func TestUserUnits_ReinstallRestartsARunningService(t *testing.T) {
	fake := &fakeSystemd{}
	units, _ := newTestUserUnits(t, fake, "/usr/local/bin/lmm")
	ctx := context.Background()
	_, err := units.Install(ctx, core.ServiceInstallOptions{})
	require.NoError(t, err)

	fake.calls = nil
	report, err := units.Install(ctx, core.ServiceInstallOptions{})
	require.NoError(t, err)
	assert.False(t, report.Changed)
	assert.Equal(t, []string{"daemon-reload", "enable " + systemd.ServeUnitName, "restart " + systemd.ServeUnitName}, fake.verbs(),
		"unchanged, but running: it may still run a previous ExecStart")

	fake.calls = nil
	fake.active = false
	report, err = units.Install(ctx, core.ServiceInstallOptions{})
	require.NoError(t, err)
	assert.False(t, report.Changed)
	assert.Equal(t, []string{"daemon-reload", "enable --now " + systemd.ServeUnitName}, fake.verbs())

	fake.calls = nil
	report, err = units.Install(ctx, core.ServiceInstallOptions{Addr: "127.0.0.1:9000"})
	require.NoError(t, err)
	assert.True(t, report.Changed)
	assert.Equal(t, []string{"daemon-reload", "enable " + systemd.ServeUnitName, "restart " + systemd.ServeUnitName}, fake.verbs())
}

// A package's unit is enabled where it is, not shadowed by a second copy.
func TestUserUnits_InstallDefersToAPackagedUnit(t *testing.T) {
	fake := &fakeSystemd{}
	units, unitPath := newTestUserUnits(t, fake, "/usr/bin/lmm")
	units.PackagedDirs = append(units.PackagedDirs, t.TempDir(), t.TempDir())
	packaged := filepath.Join(units.PackagedDirs[1], systemd.ServeUnitName)
	require.NoError(t, os.WriteFile(packaged, systemd.ServeUnit, 0o644))

	_, err := units.Install(context.Background(), core.ServiceInstallOptions{})
	var pkgErr *core.PackagedUnitError
	require.ErrorAs(t, err, &pkgErr)
	assert.Equal(t, packaged, pkgErr.Path)
	assert.Contains(t, err.Error(), "systemctl --user enable --now "+systemd.ServeUnitName)
	assert.NoFileExists(t, unitPath)
	assert.Empty(t, fake.verbs())
}

func TestUserUnits_InstallRefusesAGoRunBuild(t *testing.T) {
	fake := &fakeSystemd{}
	units, unitPath := newTestUserUnits(t, fake, "/tmp/go-build123/b001/exe/lmm")
	_, err := units.Install(context.Background(), core.ServiceInstallOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go run")
	assert.NoFileExists(t, unitPath)
}

func TestUserUnits_UninstallDisablesRemovesAndReloads(t *testing.T) {
	fake := &fakeSystemd{}
	units, unitPath := newTestUserUnits(t, fake, "/usr/local/bin/lmm")
	ctx := context.Background()
	_, err := units.Install(ctx, core.ServiceInstallOptions{})
	require.NoError(t, err)

	fake.calls = nil
	report, err := units.Uninstall(ctx)
	require.NoError(t, err)
	assert.NoFileExists(t, unitPath)
	assert.Equal(t, []string{"disable --now " + systemd.ServeUnitName, "daemon-reload"}, fake.verbs())
	assert.True(t, report.Changed)
	require.Len(t, report.Units, 1)
	assert.False(t, report.Units[0].Installed)
	assert.False(t, report.Units[0].Active)

	// Nothing left to remove is not an error.
	fake.calls = nil
	report, err = units.Uninstall(ctx)
	require.NoError(t, err)
	assert.False(t, report.Changed)
	assert.Empty(t, fake.verbs())
}

// A failed disable leaves the file in place, so the unit is never orphaned
// enabled with nothing behind it.
func TestUserUnits_UninstallKeepsTheFileWhenDisableFails(t *testing.T) {
	fake := &fakeSystemd{}
	units, unitPath := newTestUserUnits(t, fake, "/usr/local/bin/lmm")
	_, err := units.Install(context.Background(), core.ServiceInstallOptions{})
	require.NoError(t, err)

	fake.failVerb = "disable"
	_, err = units.Uninstall(context.Background())
	require.Error(t, err)
	assert.FileExists(t, unitPath)
}

func TestUserUnits_UninstallLeavesAPackagedUnitToTheUser(t *testing.T) {
	fake := &fakeSystemd{}
	units, _ := newTestUserUnits(t, fake, "/usr/bin/lmm")
	units.PackagedDirs = append(units.PackagedDirs, t.TempDir(), t.TempDir())
	packaged := filepath.Join(units.PackagedDirs[2], systemd.ServeUnitName)
	require.NoError(t, os.WriteFile(packaged, systemd.ServeUnit, 0o644))

	_, err := units.Uninstall(context.Background())
	var pkgErr *core.PackagedUnitError
	require.ErrorAs(t, err, &pkgErr)
	assert.Contains(t, err.Error(), "systemctl --user disable --now "+systemd.ServeUnitName)
	assert.FileExists(t, packaged)
	assert.Empty(t, fake.verbs())
}

func TestUserUnits_Status(t *testing.T) {
	fake := &fakeSystemd{linger: "yes", enabled: true}
	units, _ := newTestUserUnits(t, fake, "/usr/bin/lmm")

	report, err := units.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, report.Linger)
	assert.Contains(t, fake.calls, "loginctl show-user u -p Linger")
	require.Len(t, report.Units, 1)
	assert.Equal(t, core.UnitStatus{
		Name: systemd.ServeUnitName, Enabled: true, EnabledState: "enabled", ActiveState: "inactive",
	}, report.Units[0], "not installed anywhere lmm looks")

	packaged := filepath.Join(units.PackagedDirs[0], systemd.ServeUnitName)
	require.NoError(t, os.WriteFile(packaged, systemd.ServeUnit, 0o644))
	report, err = units.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, report.Units[0].Installed)
	assert.Equal(t, core.UnitOriginPackage, report.Units[0].Origin)
	assert.Equal(t, packaged, report.Units[0].Path)

	fake.linger = ""
	report, err = units.Status(context.Background())
	require.NoError(t, err)
	assert.False(t, report.Linger, "loginctl failing reads as no lingering")
}

// No systemctl, or no user manager to talk to, is a typed error from every
// operation, and nothing is written.
func TestUserUnits_SystemdUnavailable(t *testing.T) {
	for name, fake := range map[string]*fakeSystemd{
		"no systemctl":    {missing: true},
		"no user manager": {noBus: errors.New("exit status 1: Failed to connect to bus: No medium found")},
	} {
		t.Run(name, func(t *testing.T) {
			units, unitPath := newTestUserUnits(t, fake, "/usr/local/bin/lmm")
			ctx := context.Background()
			_, err := units.Install(ctx, core.ServiceInstallOptions{})
			var unavailable *core.SystemdUnavailableError
			require.ErrorAs(t, err, &unavailable)
			assert.ErrorIs(t, err, core.ErrSystemdUnavailable)
			assert.NoFileExists(t, unitPath)

			_, err = units.Uninstall(ctx)
			assert.ErrorIs(t, err, core.ErrSystemdUnavailable)
			_, err = units.Status(ctx)
			assert.ErrorIs(t, err, core.ErrSystemdUnavailable)
		})
	}
}

func TestNewUserUnits_ReadsTheRealEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "/somewhere/else")
	units, err := core.NewUserUnits()
	require.NoError(t, err)
	assert.Equal(t, home, units.Home)
	assert.Equal(t, []string{"/etc/systemd/user", "/usr/local/lib/systemd/user", "/usr/lib/systemd/user"}, units.PackagedDirs,
		"every directory a system-wide unit can be in, in systemd's own order")
	assert.Equal(t, "/somewhere/else", units.Getenv("XDG_CONFIG_HOME"), "the shell's environment, for the service's Environment=")
	assert.NotNil(t, units.LookPath)
	assert.NotNil(t, units.Sleep)
}

// The unit goes where the user manager searches: its own XDG_CONFIG_HOME,
// not the installing shell's, which systemd never sees.
func TestUserUnits_InstallWritesWhereTheUserManagerLooks(t *testing.T) {
	managerConfig := t.TempDir()
	fake := &fakeSystemd{env: map[string]string{"XDG_CONFIG_HOME": managerConfig}}
	units, defaultPath := newTestUserUnits(t, fake, "/usr/local/bin/lmm")
	shellConfig := t.TempDir()
	units.Getenv = func(k string) string { return map[string]string{"XDG_CONFIG_HOME": shellConfig}[k] }

	report, err := units.Install(context.Background(), core.ServiceInstallOptions{})
	require.NoError(t, err)
	want := filepath.Join(managerConfig, "systemd", "user", systemd.ServeUnitName)
	assert.FileExists(t, want)
	assert.NoFileExists(t, defaultPath)
	assert.NoFileExists(t, filepath.Join(shellConfig, "systemd", "user", systemd.ServeUnitName))
	assert.Equal(t, want, report.Units[0].Path)

	status, err := units.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, want, status.Units[0].Path, "status and uninstall look in the same place")
}

// A directory the user manager does not search would hold a unit it never
// loads: that is a clear error, and nothing is written.
func TestUserUnits_InstallRefusesADirectoryOffTheUnitPath(t *testing.T) {
	fake := &fakeSystemd{unitPath: "/elsewhere/systemd/user /usr/lib/systemd/user"}
	units, unitPath := newTestUserUnits(t, fake, "/usr/local/bin/lmm")

	_, err := units.Install(context.Background(), core.ServiceInstallOptions{})
	var offPath *core.UnitPathError
	require.ErrorAs(t, err, &offPath)
	assert.Equal(t, filepath.Dir(unitPath), offPath.Dir)
	assert.Contains(t, err.Error(), filepath.Dir(unitPath))
	assert.NoFileExists(t, unitPath)
	assert.Empty(t, fake.verbs())

	fake.unitPath = "/elsewhere " + filepath.Dir(unitPath) + "/ /usr/lib/systemd/user"
	_, err = units.Install(context.Background(), core.ServiceInstallOptions{})
	require.NoError(t, err)
	assert.FileExists(t, unitPath)
}

// The service reads the same config and data as the CLI that installed it:
// the shell's XDG directories, when they differ from the user manager's,
// become Environment= lines, and --config/--data become serve flags.
func TestUserUnits_InstallCarriesTheShellsDirectories(t *testing.T) {
	fake := &fakeSystemd{env: map[string]string{"XDG_DATA_HOME": "/srv/data"}}
	units, unitPath := newTestUserUnits(t, fake, "/usr/local/bin/lmm")
	shell := map[string]string{"XDG_CONFIG_HOME": "/my config/%h", "XDG_DATA_HOME": "/srv/data/"}
	units.Getenv = func(k string) string { return shell[k] }

	_, err := units.Install(context.Background(), core.ServiceInstallOptions{ConfigDir: "/c/lmm", DataDir: "/d/lmm data"})
	require.NoError(t, err)
	raw, err := os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "\nEnvironment=\"XDG_CONFIG_HOME=/my config/%%h\"\nExecStart=")
	assert.NotContains(t, string(raw), "XDG_DATA_HOME", "the user manager already has the same one")
	assert.Contains(t, string(raw), "\nExecStart=/usr/local/bin/lmm serve --no-open --config /c/lmm --data \"/d/lmm data\"\n")

	// The shell's value is the user manager's default: nothing to carry.
	shell = map[string]string{"XDG_CONFIG_HOME": filepath.Join(units.Home, ".config")}
	fake.env = nil
	_, err = units.Install(context.Background(), core.ServiceInstallOptions{})
	require.NoError(t, err)
	raw, err = os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "Environment=")
}

func TestRenderServeUnit_QuotesEnvironmentAndRefusesRelativeDirs(t *testing.T) {
	got, err := core.RenderServeUnit("/usr/bin/lmm", core.ServiceInstallOptions{Environment: []string{`XDG_DATA_HOME=/a "b"\c%$`}})
	require.NoError(t, err)
	assert.Contains(t, string(got), "\nEnvironment=\"XDG_DATA_HOME=/a \\\"b\\\"\\\\c%%$\"\nExecStart=")

	_, err = core.RenderServeUnit("/usr/bin/lmm", core.ServiceInstallOptions{Environment: []string{"A=1\nExecStartPre=/bin/rm"}})
	require.Error(t, err, "a newline would inject a directive")
	_, err = core.RenderServeUnit("/usr/bin/lmm", core.ServiceInstallOptions{ConfigDir: "rel/lmm"})
	require.Error(t, err, "the service has no working directory to resolve it against")
	_, err = core.RenderServeUnit("/usr/bin/lmm", core.ServiceInstallOptions{DataDir: "rel/lmm"})
	require.Error(t, err)
}

// A bad --addr is a typed error before anything is written or run.
func TestUserUnits_InstallValidatesAddr(t *testing.T) {
	for _, addr := range []string{"localhost", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:http", "127.0.0.1:-1", "a:b:c"} {
		t.Run(addr, func(t *testing.T) {
			fake := &fakeSystemd{}
			units, unitPath := newTestUserUnits(t, fake, "/usr/local/bin/lmm")
			_, err := units.Install(context.Background(), core.ServiceInstallOptions{Addr: addr})
			var bad *core.InvalidAddrError
			require.ErrorAs(t, err, &bad)
			assert.Equal(t, addr, bad.Addr)
			assert.NoFileExists(t, unitPath)
			assert.Empty(t, fake.calls)
		})
	}
	for _, addr := range []string{"127.0.0.1:7420", "[::1]:1", ":65535", "localhost:8080"} {
		assert.NoError(t, core.ValidateServeAddr(addr), addr)
	}
}

// A service that dies straight after it starts is reported as not running:
// Install waits a moment, then asks again.
func TestUserUnits_InstallRechecksTheServiceAfterAMoment(t *testing.T) {
	fake := &fakeSystemd{}
	units, _ := newTestUserUnits(t, fake, "/usr/local/bin/lmm")
	var waited time.Duration
	units.Sleep = func(_ context.Context, d time.Duration) {
		waited = d
		require.True(t, fake.active, "the wait comes after the start")
		fake.active = false // it exited during the wait
		fake.calls = append(fake.calls, "sleep")
	}

	report, err := units.Install(context.Background(), core.ServiceInstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, time.Second, waited)
	assert.False(t, report.Units[0].Active)
	assert.Equal(t, "inactive", report.Units[0].ActiveState)
	slept := slices.Index(fake.calls, "sleep")
	require.GreaterOrEqual(t, slept, 0)
	assert.Contains(t, fake.calls[slept+1:], "systemctl --user is-active "+systemd.ServeUnitName)
}

// The lmm on PATH is preferred only when it is this very binary; a go run
// build is refused by its exact go-build<digits> directory, not by a
// directory that only starts with the word.
func TestUserUnits_ResolveExecutable(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "lmm-2.1.0")
	other := filepath.Join(dir, "other-lmm")
	link := filepath.Join(dir, "lmm")
	require.NoError(t, os.WriteFile(real, nil, 0o755))
	require.NoError(t, os.WriteFile(other, []byte("x"), 0o755))
	require.NoError(t, os.Symlink(real, link))

	for name, tc := range map[string]struct {
		exe, onPath, want string
	}{
		"PATH names this binary":    {real, link, link},
		"PATH names another binary": {real, other, real},
		"nothing on PATH":           {real, "", real},
		"a go-build-like directory": {"/home/u/go-build-tools/lmm", "", "/home/u/go-build-tools/lmm"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeSystemd{}
			units, unitPath := newTestUserUnits(t, fake, tc.exe)
			units.LookPath = func(string) (string, error) {
				if tc.onPath == "" {
					return "", exec.ErrNotFound
				}
				return tc.onPath, nil
			}
			_, err := units.Install(context.Background(), core.ServiceInstallOptions{})
			require.NoError(t, err)
			raw, err := os.ReadFile(unitPath)
			require.NoError(t, err)
			assert.Contains(t, string(raw), "\nExecStart="+tc.want+" serve --no-open\n")
		})
	}
}

// lmm overwrites or removes only a unit file it wrote: a regular file with
// its header. Anything else is the user's, and a typed refusal.
func TestUserUnits_LeavesAUnitItDidNotWriteAlone(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, path string){
		"hand-written": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, systemd.ServeUnit, 0o644))
		},
		"a symlink to an lmm-written file": func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), systemd.ServeUnitName)
			unit, err := core.RenderServeUnit("/usr/bin/lmm", core.ServiceInstallOptions{})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(target, unit, 0o644))
			require.NoError(t, os.Symlink(target, path))
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeSystemd{}
			units, unitPath := newTestUserUnits(t, fake, "/usr/local/bin/lmm")
			require.NoError(t, os.MkdirAll(filepath.Dir(unitPath), 0o755))
			plant(t, unitPath)
			before, err := os.ReadFile(unitPath)
			require.NoError(t, err)

			_, err = units.Install(context.Background(), core.ServiceInstallOptions{})
			var foreign *core.ForeignUnitError
			require.ErrorAs(t, err, &foreign)
			assert.Equal(t, unitPath, foreign.Path)
			assert.Contains(t, err.Error(), unitPath)
			assert.Contains(t, err.Error(), "by hand")

			_, err = units.Uninstall(context.Background())
			require.ErrorAs(t, err, &foreign)
			assert.Contains(t, err.Error(), "by hand")

			after, err := os.ReadFile(unitPath)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Empty(t, fake.verbs(), "nothing is disabled, reloaded or started")
		})
	}
}

// --auto-update (#569) adds lmm-update.timer and the oneshot
// lmm-update.service it starts. The timer is enabled and started; the
// service is only written, since the timer is what runs it.
func TestUserUnits_InstallAutoUpdateAddsTheTimer(t *testing.T) {
	fake := &fakeSystemd{}
	units, servePath := newTestUserUnits(t, fake, "/home/u/.local/bin/lmm")
	dir := filepath.Dir(servePath)
	ctx := context.Background()

	report, err := units.Install(ctx, core.ServiceInstallOptions{AutoUpdate: true, ConfigDir: "/home/u/cfg"})
	require.NoError(t, err)
	assert.Equal(t, []string{"daemon-reload", "enable --now " + systemd.ServeUnitName, "enable --now lmm-update.timer"}, fake.verbs())

	svc, err := os.ReadFile(filepath.Join(dir, "lmm-update.service"))
	require.NoError(t, err)
	assert.Contains(t, string(svc), "\nType=oneshot\n")
	assert.Contains(t, string(svc), "\nExecStart=/home/u/.local/bin/lmm self-update --auto --config /home/u/cfg\n")
	assert.True(t, strings.HasPrefix(string(svc), "# Written by `lmm service install`"), "the ownership header")
	timer, err := os.ReadFile(filepath.Join(dir, "lmm-update.timer"))
	require.NoError(t, err)
	for _, want := range []string{"\nOnCalendar=daily\n", "\nPersistent=true\n", "\nRandomizedDelaySec=1h\n", "\nWantedBy=timers.target\n"} {
		assert.Contains(t, string(timer), want)
	}
	assert.True(t, strings.HasPrefix(string(timer), "# Written by `lmm service install`"))

	var names []string
	for _, u := range report.Units {
		names = append(names, u.Name)
		assert.True(t, u.Installed, u.Name)
	}
	assert.Equal(t, []string{systemd.ServeUnitName, "lmm-update.timer", "lmm-update.service"}, names)

	// A later install without the flag keeps them, rewritten for the
	// binary it runs from.
	units.Executable = func() (string, error) { return "/opt/lmm/lmm", nil }
	_, err = units.Install(ctx, core.ServiceInstallOptions{})
	require.NoError(t, err)
	svc, err = os.ReadFile(filepath.Join(dir, "lmm-update.service"))
	require.NoError(t, err)
	assert.Contains(t, string(svc), "\nExecStart=/opt/lmm/lmm self-update --auto\n")

	// Uninstall stops and removes all three.
	fake.calls = nil
	report, err = units.Uninstall(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"disable --now " + systemd.ServeUnitName,
		"disable --now lmm-update.timer",
		"stop lmm-update.service",
		"daemon-reload",
	}, fake.verbs())
	for _, name := range []string{systemd.ServeUnitName, "lmm-update.timer", "lmm-update.service"} {
		assert.NoFileExists(t, filepath.Join(dir, name))
	}
	assert.Len(t, report.Units, 3, "the removed units are reported")
}

// Without --auto-update, and with no timer installed before, the report
// and the unit directory hold only lmm-serve.service.
func TestUserUnits_InstallWithoutAutoUpdateWritesNoTimer(t *testing.T) {
	fake := &fakeSystemd{}
	units, servePath := newTestUserUnits(t, fake, "/home/u/.local/bin/lmm")
	report, err := units.Install(context.Background(), core.ServiceInstallOptions{})
	require.NoError(t, err)
	assert.Len(t, report.Units, 1)
	assert.NoFileExists(t, filepath.Join(filepath.Dir(servePath), "lmm-update.timer"))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(servePath), "lmm-update.service"))
}

// A timer file lmm did not write is refused like a serve unit is.
func TestUserUnits_InstallAutoUpdateLeavesAForeignTimerAlone(t *testing.T) {
	fake := &fakeSystemd{}
	units, servePath := newTestUserUnits(t, fake, "/home/u/.local/bin/lmm")
	timer := filepath.Join(filepath.Dir(servePath), "lmm-update.timer")
	require.NoError(t, os.MkdirAll(filepath.Dir(timer), 0o755))
	require.NoError(t, os.WriteFile(timer, []byte("[Timer]\nOnCalendar=hourly\n"), 0o644))

	_, err := units.Install(context.Background(), core.ServiceInstallOptions{AutoUpdate: true})
	var foreign *core.ForeignUnitError
	require.ErrorAs(t, err, &foreign)
	assert.NoFileExists(t, servePath, "nothing is written")
}

func TestRenderUpdateUnits_Golden(t *testing.T) {
	opts := core.ServiceInstallOptions{DataDir: "/home/u/data", Environment: []string{"XDG_CONFIG_HOME=/home/u/cfg"}}
	for name, render := range map[string]func(string, core.ServiceInstallOptions) ([]byte, error){
		"lmm-update.service": core.RenderUpdateService,
		"lmm-update.timer":   core.RenderUpdateTimer,
	} {
		got, err := render("/home/u/go/bin/lmm", opts)
		require.NoError(t, err)
		path := filepath.Join("testdata", "systemd", name+".golden")
		if *updateUnitGoldens {
			require.NoError(t, os.WriteFile(path, got, 0o644))
		}
		want, err := os.ReadFile(path)
		require.NoError(t, err, "run with -update-unit-goldens to record it")
		assert.Equal(t, string(want), string(got))
	}
}

// After a self-update, a running lmm-serve.service is restarted onto the
// new binary with try-restart; one that is not running is left stopped,
// and a host without systemd is not an error.
func TestUserUnits_RestartServeIfActive(t *testing.T) {
	ctx := context.Background()

	fake := &fakeSystemd{active: true}
	units, _ := newTestUserUnits(t, fake, "/home/u/.local/bin/lmm")
	restarted, err := units.RestartServeIfActive(ctx)
	require.NoError(t, err)
	assert.True(t, restarted)
	assert.Equal(t, []string{"try-restart " + systemd.ServeUnitName}, fake.verbs())

	fake = &fakeSystemd{}
	units, _ = newTestUserUnits(t, fake, "/home/u/.local/bin/lmm")
	restarted, err = units.RestartServeIfActive(ctx)
	require.NoError(t, err)
	assert.False(t, restarted)
	assert.Empty(t, fake.verbs())

	fake = &fakeSystemd{missing: true}
	units, _ = newTestUserUnits(t, fake, "/home/u/.local/bin/lmm")
	restarted, err = units.RestartServeIfActive(ctx)
	require.NoError(t, err)
	assert.False(t, restarted)

	fake = &fakeSystemd{active: true, failVerb: "try-restart"}
	units, _ = newTestUserUnits(t, fake, "/home/u/.local/bin/lmm")
	restarted, err = units.RestartServeIfActive(ctx)
	require.Error(t, err)
	assert.False(t, restarted)
}

// lmm-update.service retries a run that fails - a network that is not up
// yet, most often - a few times, five minutes apart, rather than waiting a
// day for the timer. network-online.target is not named: a user manager
// has no such target to order against, so it would do nothing. Nor is
// PrivateTmp=: in a user manager it needs unprivileged user namespaces,
// which some distributions turn off, and the unit would then fail to start.
func TestRenderUpdateService_RetriesAndHardening(t *testing.T) {
	got, err := core.RenderUpdateService("/home/u/go/bin/lmm", core.ServiceInstallOptions{})
	require.NoError(t, err)
	unit := string(got)
	for _, line := range []string{
		"Restart=on-failure",
		"RestartSec=5min",
		"StartLimitIntervalSec=1h",
		"StartLimitBurst=4",
		"NoNewPrivileges=yes",
	} {
		assert.Contains(t, unit, "\n"+line+"\n")
	}
	assert.NotContains(t, unit, "network-online.target")
	assert.NotContains(t, unit, "PrivateTmp")
	unitSection, _, _ := strings.Cut(unit, "[Service]")
	assert.Contains(t, unitSection, "StartLimitBurst=", "the start limit belongs in [Unit]")
}

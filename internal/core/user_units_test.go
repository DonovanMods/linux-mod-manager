package core_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	failVerb string // a systemctl verb that fails
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
	switch verb {
	case "show", "daemon-reload":
	case "enable":
		f.enabled = true
		f.active = f.active || now
	case "disable":
		f.enabled = false
		f.active = f.active && !now
	case "start", "restart":
		f.active = true
	case "is-enabled":
		if f.enabled {
			return []byte("enabled\n"), nil
		}
		return []byte("disabled\n"), errors.New("exit status 1")
	case "is-active":
		if f.active {
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
		case "show", "is-enabled", "is-active":
			continue
		}
		out = append(out, strings.Join(fields[2:], " "))
	}
	return out
}

// newTestUserUnits returns UserUnits over a temp config home, an empty
// packaged-unit directory and fake, with the running binary at exe.
func newTestUserUnits(t *testing.T, fake *fakeSystemd, exe string) (*core.UserUnits, string) {
	t.Helper()
	configHome := t.TempDir()
	return &core.UserUnits{
		ConfigHome:   configHome,
		PackagedDirs: []string{t.TempDir()},
		Executable:   func() (string, error) { return exe, nil },
		Run:          fake.run,
		User:         "u",
	}, filepath.Join(configHome, "systemd", "user", systemd.ServeUnitName)
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
	units, unitPath := newTestUserUnits(t, fake, link)

	report, err := units.Install(context.Background(), core.ServiceInstallOptions{Addr: "127.0.0.1:8080"})
	require.NoError(t, err)

	raw, err := os.ReadFile(unitPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "\nExecStart="+real+" serve --no-open --addr 127.0.0.1:8080\n", "symlinks are resolved")
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

// Installing again with a different --addr rewrites the file and restarts
// the running service, so it picks up the new ExecStart; the same install
// again changes nothing and only makes sure it is enabled and running.
func TestUserUnits_ReinstallRestartsOnlyWhenTheUnitChanged(t *testing.T) {
	fake := &fakeSystemd{}
	units, _ := newTestUserUnits(t, fake, "/usr/local/bin/lmm")
	ctx := context.Background()
	_, err := units.Install(ctx, core.ServiceInstallOptions{})
	require.NoError(t, err)

	fake.calls = nil
	report, err := units.Install(ctx, core.ServiceInstallOptions{})
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
	packaged := filepath.Join(units.PackagedDirs[0], systemd.ServeUnitName)
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
	packaged := filepath.Join(units.PackagedDirs[0], systemd.ServeUnitName)
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

func TestNewUserUnits_FollowsXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	units, err := core.NewUserUnits()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "systemd", "user"), units.UserUnitDir())
	assert.Equal(t, []string{"/usr/lib/systemd/user"}, units.PackagedDirs)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative")
	units, err = core.NewUserUnits()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".config", "systemd", "user"), units.UserUnitDir(), "a relative XDG value is ignored")
}

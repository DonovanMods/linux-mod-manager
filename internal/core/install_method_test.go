package core_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProbe answers DetectInstallMethod's three questions from tables: which
// tools are on PATH, and what each owner query prints. A query for a tool
// not in owners fails as an unowned file does (exit 1).
type fakeProbe struct {
	exe    string
	tools  map[string]bool
	owners map[string]string // tool name -> stdout of its owner query
	ran    []string
}

func (f *fakeProbe) probe() core.InstallProbe {
	return core.InstallProbe{
		Executable: func() (string, error) { return f.exe, nil },
		LookPath: func(name string) (string, error) {
			if f.tools[name] {
				return "/usr/bin/" + name, nil
			}
			return "", exec.ErrNotFound
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			f.ran = append(f.ran, name+" "+strings.Join(args, " "))
			if out, ok := f.owners[name]; ok {
				return []byte(out), nil
			}
			return nil, errors.New("exit status 1")
		},
	}
}

func TestDetectInstallMethod(t *testing.T) {
	tests := []struct {
		name   string
		tools  []string
		owners map[string]string
		want   core.InstallMethod
	}{
		{
			name:   "dpkg, with the architecture qualifier dropped",
			tools:  []string{"dpkg", "apt"},
			owners: map[string]string{"dpkg": "lmm:amd64: /usr/bin/lmm\n"},
			want: core.InstallMethod{Method: core.InstallPackage, Manager: "dpkg", Package: "lmm",
				UpgradeCommand: "sudo apt install --only-upgrade lmm"},
		},
		{
			name:   "rpm upgraded through dnf",
			tools:  []string{"rpm", "dnf"},
			owners: map[string]string{"rpm": "lmm\n"},
			want: core.InstallMethod{Method: core.InstallPackage, Manager: "rpm", Package: "lmm",
				UpgradeCommand: "sudo dnf upgrade lmm"},
		},
		{
			name:   "rpm upgraded through zypper",
			tools:  []string{"rpm", "zypper"},
			owners: map[string]string{"rpm": "lmm\n"},
			want: core.InstallMethod{Method: core.InstallPackage, Manager: "rpm", Package: "lmm",
				UpgradeCommand: "sudo zypper update lmm"},
		},
		{
			name:   "apk, with the version-release suffix dropped",
			tools:  []string{"apk"},
			owners: map[string]string{"apk": "lmm-2.0.1-r0\n"},
			want: core.InstallMethod{Method: core.InstallPackage, Manager: "apk", Package: "lmm",
				UpgradeCommand: "sudo apk upgrade lmm"},
		},
		{
			name:   "apk, a hyphenated name that only looks like a release",
			tools:  []string{"apk"},
			owners: map[string]string{"apk": "lmm-rust-2.0.1-r3\n"},
			want: core.InstallMethod{Method: core.InstallPackage, Manager: "apk", Package: "lmm-rust",
				UpgradeCommand: "sudo apk upgrade lmm-rust"},
		},
		{
			name:   "pacman",
			tools:  []string{"pacman"},
			owners: map[string]string{"pacman": "lmm-bin\n"},
			want: core.InstallMethod{Method: core.InstallPackage, Manager: "pacman", Package: "lmm-bin",
				UpgradeCommand: "sudo pacman -Syu"},
		},
		{
			name:   "a manager that does not own the file is passed over",
			tools:  []string{"dpkg", "pacman"},
			owners: map[string]string{"pacman": "lmm\n"},
			want: core.InstallMethod{Method: core.InstallPackage, Manager: "pacman", Package: "lmm",
				UpgradeCommand: "sudo pacman -Syu"},
		},
		{
			name:  "no manager owns it",
			tools: []string{"dpkg", "rpm", "apk", "pacman"},
			want:  core.InstallMethod{Method: core.InstallSelfManaged},
		},
		{
			name: "no manager on PATH",
			want: core.InstallMethod{Method: core.InstallSelfManaged},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeProbe{exe: "/usr/bin/lmm", tools: map[string]bool{}, owners: tt.owners}
			for _, tool := range tt.tools {
				f.tools[tool] = true
			}
			got := core.DetectInstallMethod(context.Background(), f.probe(), false)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestDetectInstallMethod_AsksAboutTheResolvedExecutable: each query names
// the binary's own path, with exactly the arguments the manager documents.
func TestDetectInstallMethod_AsksAboutTheResolvedExecutable(t *testing.T) {
	f := &fakeProbe{exe: "/opt/lmm/bin/lmm", tools: map[string]bool{"dpkg": true, "rpm": true, "apk": true, "pacman": true}}
	core.DetectInstallMethod(context.Background(), f.probe(), false)
	assert.Equal(t, []string{
		"dpkg -S /opt/lmm/bin/lmm",
		"rpm -qf --queryformat %{NAME}\n /opt/lmm/bin/lmm",
		"apk info -q --who-owns /opt/lmm/bin/lmm",
		"pacman -Qqo /opt/lmm/bin/lmm",
	}, f.ran)
}

// TestDetectInstallMethod_DpkgMatchesThePathExactly: `dpkg -S` takes a glob
// pattern, so a path's own metacharacters are escaped - a binary under
// "/opt/lmm[1]/" must not be matched against some other package's file.
func TestDetectInstallMethod_DpkgMatchesThePathExactly(t *testing.T) {
	f := &fakeProbe{exe: `/opt/lmm[1]*?\x/lmm`, tools: map[string]bool{"dpkg": true}}
	core.DetectInstallMethod(context.Background(), f.probe(), false)
	assert.Equal(t, []string{`dpkg -S /opt/lmm\[1\]\*\?\\x/lmm`}, f.ran)
}

// TestDetectInstallMethod_RpmWithMoreThanOneOwner: rpm prints one line per
// owning package, and the first is the one named.
func TestDetectInstallMethod_RpmWithMoreThanOneOwner(t *testing.T) {
	f := &fakeProbe{exe: "/usr/bin/lmm", tools: map[string]bool{"rpm": true, "dnf": true},
		owners: map[string]string{"rpm": "lmm\nlmm-compat\n"}}
	got := core.DetectInstallMethod(context.Background(), f.probe(), false)
	assert.Equal(t, "lmm", got.Package)
	assert.Equal(t, "sudo dnf upgrade lmm", got.UpgradeCommand)
}

// TestDetectInstallMethod_DevBuildAsksNothing: a dev build is "dev" whoever
// owns the file, and no package manager is run for it.
func TestDetectInstallMethod_DevBuildAsksNothing(t *testing.T) {
	f := &fakeProbe{exe: "/usr/bin/lmm", tools: map[string]bool{"pacman": true}, owners: map[string]string{"pacman": "lmm\n"}}
	got := core.DetectInstallMethod(context.Background(), f.probe(), true)
	assert.Equal(t, core.InstallMethod{Method: core.InstallDev}, got)
	assert.Empty(t, f.ran)
}

// TestDetectInstallMethod_UnresolvableExecutable: a binary lmm cannot find
// is not anyone's package.
func TestDetectInstallMethod_UnresolvableExecutable(t *testing.T) {
	probe := core.InstallProbe{
		Executable: func() (string, error) { return "", errors.New("no /proc") },
		LookPath:   func(string) (string, error) { t.Fatal("must not look"); return "", nil },
		Run:        func(context.Context, string, ...string) ([]byte, error) { t.Fatal("must not run"); return nil, nil },
	}
	got := core.DetectInstallMethod(context.Background(), probe, false)
	assert.Equal(t, core.InstallMethod{Method: core.InstallSelfManaged}, got)
}

// TestDefaultInstallProbe_ResolvesSymlinks: the default probe asks about the
// real file, so a /usr/local/bin/lmm symlink into a package's tree is still
// recognised as that package's.
func TestDefaultInstallProbe_ResolvesSymlinks(t *testing.T) {
	probe := core.DefaultInstallProbe()
	require.NotNil(t, probe.Executable)
	require.NotNil(t, probe.LookPath)
	require.NotNil(t, probe.Run)
	exe, err := probe.Executable()
	require.NoError(t, err)
	assert.NotContains(t, exe, "..", "an absolute, cleaned path")
	assert.True(t, strings.HasPrefix(exe, "/"))
}

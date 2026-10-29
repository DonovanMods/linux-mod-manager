package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// install_method.go - how THIS lmm binary was installed (#568), which is
// what decides the one line an update notice ends with: a package
// manager's upgrade command, or the release page. #569's self-update reads
// the same answer to refuse replacing a file a package manager owns.

// InstallKind names how the running lmm binary was installed.
type InstallKind string

const (
	// InstallPackage is a binary a system package manager owns: upgrade it
	// with that manager, never by replacing the file.
	InstallPackage InstallKind = "package"
	// InstallSelfManaged is a binary no package manager claims - a release
	// download, `make install`, `go install` - which the user upgrades by
	// replacing it themselves.
	InstallSelfManaged InstallKind = "self-managed"
	// InstallDev is a development build (one not made from a release tag):
	// it never reports an update, and lmm asks no package manager about it.
	InstallDev InstallKind = "dev"
)

// InstallMethod is DetectInstallMethod's answer. Manager, Package and
// UpgradeCommand are set only for InstallPackage.
type InstallMethod struct {
	Method InstallKind `json:"method"`
	// Manager is the package database that owns the binary: "dpkg",
	// "rpm", "apk" or "pacman".
	Manager string `json:"manager,omitempty"`
	// Package is the owning package's name, as the upgrade command takes it.
	Package string `json:"package,omitempty"`
	// UpgradeCommand is the command that upgrades that package, for a
	// person to run: lmm never runs it itself.
	UpgradeCommand string `json:"upgrade_command,omitempty"`
}

// InstallProbe is DetectInstallMethod's view of the system: a seam, so a
// test never asks the real package database about the test binary.
// DefaultInstallProbe is the real one.
type InstallProbe struct {
	// Executable is the running binary's path, symlinks resolved.
	Executable func() (string, error)
	// LookPath reports whether a tool is on PATH (exec.LookPath).
	LookPath func(name string) (string, error)
	// Run runs a tool and returns its standard output; a non-zero exit is
	// an error, which an owner query uses to say "not mine".
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// installQueryTimeout bounds one owner query. A package database answers
// in milliseconds; one that is locked or wedged must not hold a command.
const installQueryTimeout = 5 * time.Second

// DefaultInstallProbe asks the real system: os.Executable with symlinks
// resolved, exec.LookPath, and exec.CommandContext.
func DefaultInstallProbe() InstallProbe {
	return InstallProbe{
		Executable: func() (string, error) {
			exe, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(exe)
		},
		LookPath: exec.LookPath,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, installQueryTimeout)
			defer cancel()
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

// ownerQuery is one package database's "who owns this file?": the tool,
// its arguments around the path, and how to read the package name out of
// what it prints.
type ownerQuery struct {
	tool  string
	args  func(path string) []string
	parse func(out string) string
}

// ownerQueries are asked in this order, and the first that claims the file
// wins. More than one can be installed at once (dpkg is packaged on Arch
// and Fedora), and only the one that owns the file says so.
var ownerQueries = []ownerQuery{
	{
		// "lmm:amd64: /usr/bin/lmm", or "lmm: /usr/bin/lmm".
		tool: "dpkg",
		args: func(p string) []string { return []string{"-S", p} },
		parse: func(out string) string {
			name, _, _ := strings.Cut(out, ": ")
			name, _, _ = strings.Cut(name, ":")
			return name
		},
	},
	{
		tool:  "rpm",
		args:  func(p string) []string { return []string{"-qf", "--queryformat", "%{NAME}", p} },
		parse: func(out string) string { return out },
	},
	{
		// "lmm-2.0.1-r0": apk prints the package with its version.
		tool:  "apk",
		args:  func(p string) []string { return []string{"info", "-q", "--who-owns", p} },
		parse: apkPackageName,
	},
	{
		tool:  "pacman",
		args:  func(p string) []string { return []string{"-Qqo", p} },
		parse: func(out string) string { return out },
	},
}

// apkPackageName strips apk's "-<version>-r<release>" from a package
// identifier. A package name may itself contain hyphens, so it drops only
// a trailing release segment and the version segment before it.
func apkPackageName(id string) string {
	parts := strings.Split(id, "-")
	if n := len(parts); n > 1 && len(parts[n-1]) > 1 && parts[n-1][0] == 'r' && allDigits(parts[n-1][1:]) {
		parts = parts[:n-1]
	}
	if n := len(parts); n > 1 && parts[n-1] != "" && parts[n-1][0] >= '0' && parts[n-1][0] <= '9' {
		parts = parts[:n-1]
	}
	return strings.Join(parts, "-")
}

// allDigits reports whether s is a non-empty run of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// DetectInstallMethod reports how the running binary was installed: dev for
// a development build (asking nothing), package when a package manager on
// PATH owns the resolved executable, otherwise self-managed. It never
// fails - a probe that errors is a manager that does not own the file.
func DetectInstallMethod(ctx context.Context, probe InstallProbe, dev bool) InstallMethod {
	if dev {
		return InstallMethod{Method: InstallDev}
	}
	exe, err := probe.Executable()
	if err != nil || exe == "" {
		return InstallMethod{Method: InstallSelfManaged}
	}
	for _, q := range ownerQueries {
		if _, err := probe.LookPath(q.tool); err != nil {
			continue
		}
		out, err := probe.Run(ctx, q.tool, q.args(exe)...)
		if err != nil {
			continue
		}
		pkg := strings.TrimSpace(q.parse(strings.TrimSpace(string(out))))
		if pkg == "" {
			continue
		}
		return InstallMethod{
			Method:         InstallPackage,
			Manager:        q.tool,
			Package:        pkg,
			UpgradeCommand: upgradeCommand(q.tool, pkg, probe.LookPath),
		}
	}
	return InstallMethod{Method: InstallSelfManaged}
}

// upgradeCommand is the command that upgrades pkg under manager. An rpm
// system's front end varies by distribution, so it names whichever is on
// PATH. Arch upgrades the whole system, never one package: a partial
// upgrade is unsupported there.
func upgradeCommand(manager, pkg string, lookPath func(string) (string, error)) string {
	switch manager {
	case "dpkg":
		return "sudo apt install --only-upgrade " + pkg
	case "rpm":
		for _, fe := range []struct{ tool, verb string }{{"dnf", "upgrade"}, {"zypper", "update"}, {"yum", "update"}} {
			if _, err := lookPath(fe.tool); err == nil {
				return "sudo " + fe.tool + " " + fe.verb + " " + pkg
			}
		}
		return "sudo dnf upgrade " + pkg
	case "apk":
		return "sudo apk upgrade " + pkg
	case "pacman":
		return "sudo pacman -Syu"
	}
	return ""
}

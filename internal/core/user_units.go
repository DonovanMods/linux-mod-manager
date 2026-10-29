package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/packaging/systemd"
)

// ErrSystemdUnavailable marks a host where lmm cannot manage systemd user
// units: systemctl is not installed, or there is no user manager for this
// session to talk to (#567). Every *SystemdUnavailableError matches it
// under errors.Is.
var ErrSystemdUnavailable = errors.New("systemd user services are not available")

// SystemdUnavailableError is ErrSystemdUnavailable with the reason: which
// of the two it was, and what systemctl said.
type SystemdUnavailableError struct {
	// Reason is the sentence a user reads after the colon.
	Reason string
	// Err is the underlying failure, if any.
	Err error
}

// Error names the reason.
func (e *SystemdUnavailableError) Error() string {
	return ErrSystemdUnavailable.Error() + ": " + e.Reason
}

// Unwrap returns the underlying failure.
func (e *SystemdUnavailableError) Unwrap() error { return e.Err }

// Is reports whether target is ErrSystemdUnavailable.
func (e *SystemdUnavailableError) Is(target error) bool { return target == ErrSystemdUnavailable }

// PackagedUnitError refuses to install or uninstall a unit that a package
// already installed (#567): a second copy under ~/.config would shadow the
// package's, and removing the package's file is the package manager's job.
// The message says which systemctl command does what the user wanted.
type PackagedUnitError struct {
	// Unit is the unit's file name, e.g. lmm-serve.service.
	Unit string
	// Path is the packaged unit file.
	Path string
	// Uninstall is true for the uninstall refusal, false for install's.
	Uninstall bool
}

// Error names the packaged file and the systemctl command to run instead.
func (e *PackagedUnitError) Error() string {
	if e.Uninstall {
		return fmt.Sprintf("%s was installed by your lmm package (%s), so lmm will not remove it; turn it off with: systemctl --user disable --now %s", e.Unit, e.Path, e.Unit)
	}
	return fmt.Sprintf("%s is already installed by your lmm package (%s); turn it on with: systemctl --user enable --now %s", e.Unit, e.Path, e.Unit)
}

// UnitOrigin says who installed a unit file.
type UnitOrigin string

const (
	// UnitOriginUser is a unit `lmm service install` wrote under the user's
	// config directory.
	UnitOriginUser UnitOrigin = "user"
	// UnitOriginPackage is a unit a package installed under
	// /usr/lib/systemd/user.
	UnitOriginPackage UnitOrigin = "package"
)

// UnitStatus is one unit's state, as `lmm service status` reports it.
type UnitStatus struct {
	// Name is the unit's file name.
	Name string `json:"name"`
	// Installed is true when the unit file exists where systemd looks.
	Installed bool `json:"installed"`
	// Origin is who installed it; empty when it is not installed.
	Origin UnitOrigin `json:"origin,omitempty"`
	// Path is the unit file systemd uses; empty when it is not installed.
	Path string `json:"path,omitempty"`
	// Enabled is true when it starts with the user manager.
	Enabled bool `json:"enabled"`
	// Active is true when it is running now.
	Active bool `json:"active"`
	// EnabledState is `systemctl --user is-enabled`'s answer verbatim
	// ("enabled", "disabled", "not-found", ...).
	EnabledState string `json:"enabled_state"`
	// ActiveState is `systemctl --user is-active`'s answer verbatim
	// ("active", "inactive", "failed", ...).
	ActiveState string `json:"active_state"`
}

// ServiceReport is the document `lmm service install|uninstall|status`
// emits: every unit lmm manages, after the command ran.
type ServiceReport struct {
	// Changed is true when the command wrote or removed a unit file.
	Changed bool `json:"changed"`
	// Units is every unit lmm manages, in a fixed order.
	Units []UnitStatus `json:"units"`
	// Linger is true when the user manager runs from boot, without a
	// login (`loginctl enable-linger`). lmm reports it and never sets it.
	Linger bool `json:"linger"`
}

// ServiceInstallOptions are `lmm service install`'s choices.
type ServiceInstallOptions struct {
	// Addr is passed to `lmm serve --addr`; empty keeps serve's default.
	Addr string
}

// userUnit is one systemd user unit lmm manages. lmm-serve.service is the
// only one today; a second (#569's update timer) is another entry in
// managedUnits.
type userUnit struct {
	name   string
	render func(exe string, opts ServiceInstallOptions) ([]byte, error)
}

// managedUnits is every unit lmm manages, in report order.
var managedUnits = []userUnit{
	{name: systemd.ServeUnitName, render: RenderServeUnit},
}

// packagedUnitDir is where packages install lmm's user units.
const packagedUnitDir = "/usr/lib/systemd/user"

// UserUnits installs, removes and reports lmm's systemd user units (#567).
// It needs no Service: it touches no database, only unit files and the
// user manager. Every field is a seam a test replaces; NewUserUnits fills
// them from the environment.
type UserUnits struct {
	// ConfigHome is $XDG_CONFIG_HOME, or ~/.config; units are written to
	// ConfigHome/systemd/user.
	ConfigHome string
	// PackagedDirs are where a package's units live.
	PackagedDirs []string
	// Executable returns the running lmm binary's path.
	Executable func() (string, error)
	// Run runs a command and returns its stdout. A command that is not
	// installed returns an error wrapping exec.ErrNotFound.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// User is the login name loginctl is asked about.
	User string
}

// NewUserUnits returns UserUnits for this user and machine.
func NewUserUnits() (*UserUnits, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" || !filepath.IsAbs(configHome) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	return &UserUnits{
		ConfigHome:   configHome,
		PackagedDirs: []string{packagedUnitDir},
		Executable:   os.Executable,
		Run:          runCommand,
		User:         name,
	}, nil
}

// runCommand is UserUnits.Run's real implementation: stdout is returned,
// and stderr is folded into the error so a failure says why.
func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
	}
	return out, err
}

// UserUnitDir is where `lmm service install` writes units.
func (u *UserUnits) UserUnitDir() string {
	return filepath.Join(u.ConfigHome, "systemd", "user")
}

// systemctl runs `systemctl --user args...`.
func (u *UserUnits) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return u.Run(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

// checkSystemd confirms systemctl exists and a user manager answers it.
func (u *UserUnits) checkSystemd(ctx context.Context) error {
	if _, err := u.systemctl(ctx, "show", "--property=Version"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return &SystemdUnavailableError{Reason: "systemctl is not installed", Err: err}
		}
		return &SystemdUnavailableError{Reason: "no systemd user manager is running for this session (" + err.Error() + ")", Err: err}
	}
	return nil
}

// Install writes every managed unit for the running binary, reloads the
// user manager and enables and starts each one. A unit whose existing file
// changed is restarted, so a running service picks up the new ExecStart. A unit a
// package already installed is a *PackagedUnitError, before anything is
// written.
func (u *UserUnits) Install(ctx context.Context, opts ServiceInstallOptions) (ServiceReport, error) {
	if err := u.checkSystemd(ctx); err != nil {
		return ServiceReport{}, err
	}
	exe, err := u.resolveExecutable()
	if err != nil {
		return ServiceReport{}, err
	}

	rendered := make([][]byte, len(managedUnits))
	for i, unit := range managedUnits {
		userPath := filepath.Join(u.UserUnitDir(), unit.name)
		if !fileExists(userPath) {
			if pkg := u.packagedPath(unit.name); pkg != "" {
				return ServiceReport{}, &PackagedUnitError{Unit: unit.name, Path: pkg}
			}
		}
		if rendered[i], err = unit.render(exe, opts); err != nil {
			return ServiceReport{}, err
		}
	}

	changed, replaced := make([]bool, len(managedUnits)), make([]bool, len(managedUnits))
	for i, unit := range managedUnits {
		path := filepath.Join(u.UserUnitDir(), unit.name)
		existed := fileExists(path)
		if changed[i], err = writeUnitFile(path, rendered[i]); err != nil {
			return ServiceReport{}, err
		}
		replaced[i] = existed && changed[i]
	}
	if _, err := u.systemctl(ctx, "daemon-reload"); err != nil {
		return ServiceReport{}, fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	anyChanged := false
	for i, unit := range managedUnits {
		anyChanged = anyChanged || changed[i]
		steps := [][]string{{"enable", "--now", unit.name}}
		if replaced[i] {
			steps = [][]string{{"enable", unit.name}, {"restart", unit.name}}
		}
		for _, step := range steps {
			if _, err := u.systemctl(ctx, step...); err != nil {
				return ServiceReport{}, fmt.Errorf("systemctl --user %s: %w", strings.Join(step, " "), err)
			}
		}
	}
	return u.report(ctx, anyChanged), nil
}

// Uninstall disables, stops and removes every unit `lmm service install`
// wrote, then reloads the user manager. Nothing to remove is not an error;
// a unit that only a package installed is a *PackagedUnitError. A unit that
// fails to disable keeps its file.
func (u *UserUnits) Uninstall(ctx context.Context) (ServiceReport, error) {
	if err := u.checkSystemd(ctx); err != nil {
		return ServiceReport{}, err
	}
	removed := false
	var refusal error
	for _, unit := range managedUnits {
		path := filepath.Join(u.UserUnitDir(), unit.name)
		if !fileExists(path) {
			if pkg := u.packagedPath(unit.name); pkg != "" && refusal == nil {
				refusal = &PackagedUnitError{Unit: unit.name, Path: pkg, Uninstall: true}
			}
			continue
		}
		if _, err := u.systemctl(ctx, "disable", "--now", unit.name); err != nil {
			return ServiceReport{}, fmt.Errorf("systemctl --user disable --now %s: %w", unit.name, err)
		}
		if err := os.Remove(path); err != nil {
			return ServiceReport{}, fmt.Errorf("removing %s: %w", path, err)
		}
		removed = true
	}
	if removed {
		if _, err := u.systemctl(ctx, "daemon-reload"); err != nil {
			return ServiceReport{}, fmt.Errorf("systemctl --user daemon-reload: %w", err)
		}
	} else if refusal != nil {
		return ServiceReport{}, refusal
	}
	return u.report(ctx, removed), nil
}

// Status reports every managed unit and whether lingering is on.
func (u *UserUnits) Status(ctx context.Context) (ServiceReport, error) {
	if err := u.checkSystemd(ctx); err != nil {
		return ServiceReport{}, err
	}
	return u.report(ctx, false), nil
}

// report reads the state of every managed unit. Its queries cannot fail in
// a way worth stopping for: is-enabled and is-active exit non-zero for any
// state but the positive one, and still print it.
func (u *UserUnits) report(ctx context.Context, changed bool) ServiceReport {
	r := ServiceReport{Changed: changed, Units: make([]UnitStatus, 0, len(managedUnits))}
	for _, unit := range managedUnits {
		s := UnitStatus{Name: unit.name}
		if path := filepath.Join(u.UserUnitDir(), unit.name); fileExists(path) {
			s.Installed, s.Origin, s.Path = true, UnitOriginUser, path
		} else if pkg := u.packagedPath(unit.name); pkg != "" {
			s.Installed, s.Origin, s.Path = true, UnitOriginPackage, pkg
		}
		s.EnabledState = u.query(ctx, "is-enabled", unit.name, "not-found")
		s.ActiveState = u.query(ctx, "is-active", unit.name, "inactive")
		s.Enabled = s.EnabledState == "enabled" || s.EnabledState == "enabled-runtime"
		s.Active = s.ActiveState == "active"
		r.Units = append(r.Units, s)
	}
	r.Linger = u.linger(ctx)
	return r
}

// query returns the first line systemctl --user verb unit prints, or
// fallback when it prints nothing.
func (u *UserUnits) query(ctx context.Context, verb, unit, fallback string) string {
	out, _ := u.systemctl(ctx, verb, unit)
	if line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n"); line != "" {
		return line
	}
	return fallback
}

// linger asks loginctl whether the user manager starts at boot. A user
// loginctl does not know (no session, no lingering) answers with an error,
// which reads as no.
func (u *UserUnits) linger(ctx context.Context) bool {
	if u.User == "" {
		return false
	}
	out, err := u.Run(ctx, "loginctl", "show-user", u.User, "-p", "Linger")
	return err == nil && strings.TrimSpace(string(out)) == "Linger=yes"
}

// packagedPath returns the packaged unit file for name, or "" when no
// package installed one.
func (u *UserUnits) packagedPath(name string) string {
	for _, dir := range u.PackagedDirs {
		if path := filepath.Join(dir, name); fileExists(path) {
			return path
		}
	}
	return ""
}

// resolveExecutable returns the running binary with symlinks resolved, so
// the unit keeps working when a symlink it was reached through moves.
func (u *UserUnits) resolveExecutable() (string, error) {
	exe, err := u.Executable()
	if err != nil {
		return "", fmt.Errorf("finding the lmm binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return "", fmt.Errorf("lmm is running from a temporary `go run` build (%s), which is deleted when it exits; install it (go install, make install) and run lmm service install from there", exe)
	}
	return exe, nil
}

// fileExists reports whether path names an existing file.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeUnitFile writes data to path (0644) through a temporary file in the
// same directory, and reports whether the content changed.
func writeUnitFile(path string, data []byte) (bool, error) {
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// serveUnitHeader is the line RenderServeUnit puts above the shipped unit,
// so a reader of ~/.config/systemd/user knows where the file came from.
const serveUnitHeader = "# Written by `lmm service install`; `lmm service uninstall` removes it.\n"

// RenderServeUnit returns lmm-serve.service for the lmm binary at exe: the
// packaged unit (packaging/systemd) with its ExecStart pointed at exe and
// opts.Addr passed through. exe must be absolute, and a newline in it or in
// opts.Addr is refused rather than written into the unit.
func RenderServeUnit(exe string, opts ServiceInstallOptions) ([]byte, error) {
	if !filepath.IsAbs(exe) {
		return nil, fmt.Errorf("the lmm binary path %q is not absolute", exe)
	}
	args := []string{exe, "serve", "--no-open"}
	if opts.Addr != "" {
		args = append(args, "--addr", opts.Addr)
	}
	words := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, "\n\r") {
			return nil, fmt.Errorf("%q cannot be written into a unit file: it contains a line break", a)
		}
		words[i] = systemdQuote(a)
	}

	const directive = "ExecStart="
	var out bytes.Buffer
	out.WriteString(serveUnitHeader)
	found := false
	for line := range strings.SplitAfterSeq(string(systemd.ServeUnit), "\n") {
		if strings.HasPrefix(line, directive) {
			line = directive + strings.Join(words, " ") + "\n"
			found = true
		}
		out.WriteString(line)
	}
	if !found {
		return nil, errors.New("the embedded lmm-serve.service has no ExecStart line")
	}
	return out.Bytes(), nil
}

// systemdQuote escapes one ExecStart word: % and $ are doubled so systemd
// expands neither a specifier nor a variable, and a word with whitespace,
// quotes or backslashes is double-quoted with those escaped.
func systemdQuote(s string) string {
	s = strings.NewReplacer("%", "%%", "$", "$$").Replace(s)
	if !strings.ContainsAny(s, " \t\"'\\;") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

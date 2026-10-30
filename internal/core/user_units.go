package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

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

// ForeignUnitError refuses to overwrite or remove a unit file lmm did not
// write (#567): a symlink, or a file without the header `lmm service
// install` puts on its own. It is the user's, so lmm leaves it to them.
type ForeignUnitError struct {
	// Path is the unit file.
	Path string
	// Symlink is true when Path is a symlink rather than a file.
	Symlink bool
	// Uninstall is true for the uninstall refusal, false for install's.
	Uninstall bool
}

// Error names the file and says to change it by hand.
func (e *ForeignUnitError) Error() string {
	what := "was not written by lmm service install"
	if e.Symlink {
		what = "is a symlink, not a file lmm service install wrote"
	}
	if e.Uninstall {
		return fmt.Sprintf("%s %s, so lmm will not remove it; disable and remove it by hand", e.Path, what)
	}
	return fmt.Sprintf("%s %s, so lmm will not overwrite it; edit or remove it by hand", e.Path, what)
}

// UnitPathError refuses to write a unit into a directory the systemd user
// manager does not search (#567): it would never load it.
type UnitPathError struct {
	// Dir is where lmm would have written the unit.
	Dir string
	// UnitPath is the directories the user manager does search.
	UnitPath []string
}

// Error names the directory and the ones systemd searches.
func (e *UnitPathError) Error() string {
	return fmt.Sprintf("the systemd user manager does not load units from %s (it searches: %s); check XDG_CONFIG_HOME in `systemctl --user show-environment`",
		e.Dir, strings.Join(e.UnitPath, " "))
}

// InvalidAddrError is an --addr `lmm serve` could not listen on (#567),
// refused before a unit is written with it.
type InvalidAddrError struct {
	// Addr is the value given.
	Addr string
	// Reason says what is wrong with it.
	Reason string
}

// Error names the address and what is wrong with it.
func (e *InvalidAddrError) Error() string {
	return fmt.Sprintf("invalid --addr %q: %s", e.Addr, e.Reason)
}

// ValidateServeAddr checks addr is host:port with a numeric port from 1 to
// 65535; an empty host listens on every interface. It returns an
// *InvalidAddrError otherwise.
func ValidateServeAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return &InvalidAddrError{Addr: addr, Reason: "want host:port, e.g. 127.0.0.1:7420"}
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return &InvalidAddrError{Addr: addr, Reason: "the port must be a number from 1 to 65535"}
	}
	return nil
}

// UnitOrigin says who installed a unit file.
type UnitOrigin string

const (
	// UnitOriginUser is a unit `lmm service install` wrote under the user's
	// config directory.
	UnitOriginUser UnitOrigin = "user"
	// UnitOriginPackage is a unit a package (or an administrator)
	// installed system-wide, e.g. under /usr/lib/systemd/user.
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
	// EnvOnlyCredentials is every source whose API key, in the shell that
	// ran the command, comes only from its environment variable, with no
	// stored key, and whose variable the user manager's own environment
	// does not carry either (#582). A systemd user service does not inherit
	// a login shell's exports, so lmm-serve runs without that key: the source then
	// refuses update checks, verify's version check and searches. The fix
	// is a stored key (`lmm auth login <source>`, or the web UI's Setup >
	// Authentication) - lmm never copies a key into the unit file, which is
	// plain text. Filled by install and status; omitempty.
	EnvOnlyCredentials []EnvOnlyCredential `json:"env_only_credentials,omitempty"`
}

// EnvOnlyCredential names a source whose key comes only from its
// environment variable (ServiceReport.EnvOnlyCredentials, #582). It never
// carries the key, not even masked.
type EnvOnlyCredential struct {
	// SourceID is the source's id, as `lmm auth login` takes it.
	SourceID string `json:"source_id"`
	// Name is the source's display name.
	Name string `json:"name"`
	// EnvVar is the variable the key is read from.
	EnvVar string `json:"env_var"`
}

// ServiceInstallOptions are `lmm service install`'s choices.
type ServiceInstallOptions struct {
	// Addr is passed to `lmm serve --addr`; empty keeps serve's default.
	Addr string
	// ConfigDir and DataDir are passed to `lmm serve --config/--data`;
	// empty passes neither. Each must be absolute.
	ConfigDir, DataDir string
	// Environment is KEY=value variables set on the service. Install adds
	// the installing shell's XDG_CONFIG_HOME and XDG_DATA_HOME where they
	// differ from the user manager's, so the service reads what the CLI
	// does.
	Environment []string
	// AutoUpdate also installs lmm-update.timer and lmm-update.service
	// (#569), which run `lmm self-update --auto` daily. Once installed they
	// are kept, and rewritten, by every later install; uninstall removes
	// them.
	AutoUpdate bool
}

// userUnit is one systemd user unit lmm manages.
type userUnit struct {
	name   string
	render func(exe string, opts ServiceInstallOptions) ([]byte, error)
	// autoUpdate marks a unit `lmm service install` writes only with
	// --auto-update, or when it is already installed.
	autoUpdate bool
	// static marks a unit with no [Install] section, which another unit
	// starts: install only writes it, and uninstall stops it rather than
	// disabling it.
	static bool
}

// UpdateServiceName and UpdateTimerName are the unattended self-update's
// units (#569): the timer starts the oneshot service once a day.
const (
	UpdateServiceName = "lmm-update.service"
	UpdateTimerName   = "lmm-update.timer"
)

// managedUnits is every unit lmm manages, in report order. The timer comes
// before the service it starts, so uninstall stops the timer first.
var managedUnits = []userUnit{
	{name: systemd.ServeUnitName, render: RenderServeUnit},
	{name: UpdateTimerName, render: RenderUpdateTimer, autoUpdate: true},
	{name: UpdateServiceName, render: RenderUpdateService, autoUpdate: true, static: true},
}

// packagedUnitDirs is every system-wide directory the user manager loads
// units from, highest precedence first: an administrator's, then a local
// install's, then a package's.
var packagedUnitDirs = []string{"/etc/systemd/user", "/usr/local/lib/systemd/user", "/usr/lib/systemd/user"}

// settleTime is how long Install waits after starting a unit before it asks
// again whether it is running, so a service that exits at once is reported
// as not running rather than as started.
const settleTime = time.Second

// UserUnits installs, removes and reports lmm's systemd user units (#567).
// It needs no Service: it touches no database, only unit files and the
// user manager. Every field is a seam a test replaces; NewUserUnits fills
// them from the environment.
type UserUnits struct {
	// Home is the user's home directory. Units are written to
	// $XDG_CONFIG_HOME/systemd/user as the user manager's own environment
	// has it, or Home/.config/systemd/user; the shell's XDG_CONFIG_HOME
	// plays no part, because systemd never sees it.
	Home string
	// PackagedDirs are where a system-wide unit lives.
	PackagedDirs []string
	// Executable returns the running lmm binary's path.
	Executable func() (string, error)
	// LookPath finds a program on the shell's PATH; nil skips the lookup.
	LookPath func(file string) (string, error)
	// Getenv reads the installing shell's environment; nil reads as empty.
	Getenv func(key string) string
	// Sleep waits d, or until ctx ends, before Install asks whether a unit
	// it started is still running; nil does not wait.
	Sleep func(ctx context.Context, d time.Duration)
	// Run runs a command and returns its stdout. A command that is not
	// installed returns an error wrapping exec.ErrNotFound.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// User is the login name loginctl is asked about.
	User string
}

// NewUserUnits returns UserUnits for this user and machine.
func NewUserUnits() (*UserUnits, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home directory: %w", err)
	}
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	return &UserUnits{
		Home:         home,
		PackagedDirs: slices.Clone(packagedUnitDirs),
		Executable:   os.Executable,
		LookPath:     exec.LookPath,
		Getenv:       os.Getenv,
		Sleep:        sleepContext,
		Run:          runCommand,
		User:         name,
	}, nil
}

// sleepContext is UserUnits.Sleep's real implementation.
func sleepContext(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
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

// managerDirs is the config and data homes the user manager hands its
// services, and so the directory it loads a user's own units from.
type managerDirs struct {
	configHome, dataHome string
}

// unitDir is where `lmm service install` writes units.
func (d managerDirs) unitDir() string {
	return filepath.Join(d.configHome, "systemd", "user")
}

// managerDirs asks the user manager for its environment: its
// XDG_CONFIG_HOME and XDG_DATA_HOME, where absolute, or the defaults under
// Home. A value systemd prints $'...'-escaped is one lmm does not decode;
// the default stands, and Install's UnitPath check catches a wrong guess.
func (u *UserUnits) managerDirs(ctx context.Context) (managerDirs, error) {
	out, err := u.systemctl(ctx, "show-environment")
	if err != nil {
		return managerDirs{}, fmt.Errorf("systemctl --user show-environment: %w", err)
	}
	env := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(v, "$'") {
			env[k] = v
		}
	}
	dir := func(key string, def ...string) string {
		if v := env[key]; filepath.IsAbs(v) {
			return filepath.Clean(v)
		}
		return filepath.Join(append([]string{u.Home}, def...)...)
	}
	return managerDirs{configHome: dir("XDG_CONFIG_HOME", ".config"), dataHome: dir("XDG_DATA_HOME", ".local", "share")}, nil
}

// ManagerEnvironmentNames returns the name of every variable the user
// manager hands its services with a non-empty value (`systemctl --user
// show-environment`), so a caller can tell whether lmm-serve.service sees
// a variable without ever holding its value (#582).
func (u *UserUnits) ManagerEnvironmentNames(ctx context.Context) (map[string]bool, error) {
	out, err := u.systemctl(ctx, "show-environment")
	if err != nil {
		return nil, fmt.Errorf("systemctl --user show-environment: %w", err)
	}
	names := map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && k != "" && v != "" {
			names[k] = true
		}
	}
	return names, nil
}

// WithoutManagerEnvironment drops from creds every source whose variable
// the user manager's environment already carries (#582): lmm-serve.service
// inherits it - from environment.d, a uwsm env file or `systemctl --user
// import-environment` - so it has that key after all. When the manager
// cannot be asked, creds come back unchanged: a warning that may be
// spurious beats silence about a key the service lacks.
func (u *UserUnits) WithoutManagerEnvironment(ctx context.Context, creds []EnvOnlyCredential) []EnvOnlyCredential {
	if len(creds) == 0 {
		return nil
	}
	names, err := u.ManagerEnvironmentNames(ctx)
	if err != nil {
		return creds
	}
	var out []EnvOnlyCredential
	for _, c := range creds {
		if !names[c.EnvVar] {
			out = append(out, c)
		}
	}
	return out
}

// checkUnitPath confirms the user manager loads units from dir. A manager
// that does not say (an older systemd, or an empty answer) is taken at its
// word that the default holds.
func (u *UserUnits) checkUnitPath(ctx context.Context, dir string) error {
	out, err := u.systemctl(ctx, "show", "-p", "UnitPath", "--value")
	searched := strings.Fields(string(out))
	if err != nil || len(searched) == 0 {
		return nil
	}
	for _, d := range searched {
		if filepath.Clean(d) == dir {
			return nil
		}
	}
	return &UnitPathError{Dir: dir, UnitPath: searched}
}

// shellEnvironment returns an Environment= entry for each of the shell's
// XDG_CONFIG_HOME and XDG_DATA_HOME that is absolute and differs from the
// user manager's, so the service resolves the same config and data
// directories as the CLI that installed it.
func (u *UserUnits) shellEnvironment(mgr managerDirs) []string {
	if u.Getenv == nil {
		return nil
	}
	var env []string
	for _, v := range []struct{ key, manager string }{
		{"XDG_CONFIG_HOME", mgr.configHome},
		{"XDG_DATA_HOME", mgr.dataHome},
	} {
		if val := u.Getenv(v.key); filepath.IsAbs(val) && filepath.Clean(val) != v.manager {
			env = append(env, v.key+"="+filepath.Clean(val))
		}
	}
	return env
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

// Install writes every managed unit for the running binary into the
// directory the user manager loads units from, reloads it and enables and
// starts each one. A unit that is already running is restarted, so it never
// keeps a previous ExecStart. A moment later it asks again whether each is
// running, so one that exited at once is reported as not running. Before
// anything is written: a bad opts.Addr is an *InvalidAddrError, a unit a
// package installed a *PackagedUnitError, a unit file lmm did not write a
// *ForeignUnitError, and a directory systemd does not search a
// *UnitPathError.
func (u *UserUnits) Install(ctx context.Context, opts ServiceInstallOptions) (ServiceReport, error) {
	if opts.Addr != "" {
		if err := ValidateServeAddr(opts.Addr); err != nil {
			return ServiceReport{}, err
		}
	}
	if err := u.checkSystemd(ctx); err != nil {
		return ServiceReport{}, err
	}
	exe, err := u.resolveExecutable()
	if err != nil {
		return ServiceReport{}, err
	}
	mgr, err := u.managerDirs(ctx)
	if err != nil {
		return ServiceReport{}, err
	}
	dir := mgr.unitDir()
	if err := u.checkUnitPath(ctx, dir); err != nil {
		return ServiceReport{}, err
	}
	opts.Environment = append(u.shellEnvironment(mgr), opts.Environment...)

	var install []userUnit
	var rendered [][]byte
	for _, unit := range managedUnits {
		path := filepath.Join(dir, unit.name)
		if unit.autoUpdate && !opts.AutoUpdate {
			// Kept and rewritten when lmm installed it before; a file of
			// the user's own is no business of an install that did not
			// ask for the timer.
			if own, err := checkOwnUnit(path, false); err != nil || !own {
				continue
			}
		}
		exists, err := checkOwnUnit(path, false)
		if err != nil {
			return ServiceReport{}, err
		}
		if !exists {
			if pkg := u.packagedPath(unit.name); pkg != "" {
				return ServiceReport{}, &PackagedUnitError{Unit: unit.name, Path: pkg}
			}
		}
		data, err := unit.render(exe, opts)
		if err != nil {
			return ServiceReport{}, err
		}
		install, rendered = append(install, unit), append(rendered, data)
	}

	anyChanged := false
	for i, unit := range install {
		changed, err := writeUnitFile(filepath.Join(dir, unit.name), rendered[i])
		if err != nil {
			return ServiceReport{}, err
		}
		anyChanged = anyChanged || changed
	}
	if _, err := u.systemctl(ctx, "daemon-reload"); err != nil {
		return ServiceReport{}, fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	for _, unit := range install {
		if unit.static {
			continue
		}
		steps := [][]string{{"enable", "--now", unit.name}}
		if u.query(ctx, "is-active", unit.name, "inactive") == "active" {
			steps = [][]string{{"enable", unit.name}, {"restart", unit.name}}
		}
		for _, step := range steps {
			if _, err := u.systemctl(ctx, step...); err != nil {
				return ServiceReport{}, fmt.Errorf("systemctl --user %s: %w", strings.Join(step, " "), err)
			}
		}
	}
	if u.Sleep != nil {
		u.Sleep(ctx, settleTime)
	}
	return u.report(ctx, dir, anyChanged, nil), nil
}

// Uninstall disables, stops and removes every unit `lmm service install`
// wrote, then reloads the user manager. Nothing to remove is not an error;
// a unit that only a package installed is a *PackagedUnitError, and a unit
// file lmm did not write is a *ForeignUnitError, before anything is
// disabled. A unit that fails to disable keeps its file. Disabling removes
// the unit's enablement link, which a packaged unit of the same name
// shares, so that one is disabled too.
func (u *UserUnits) Uninstall(ctx context.Context) (ServiceReport, error) {
	if err := u.checkSystemd(ctx); err != nil {
		return ServiceReport{}, err
	}
	mgr, err := u.managerDirs(ctx)
	if err != nil {
		return ServiceReport{}, err
	}
	dir := mgr.unitDir()
	present := make([]bool, len(managedUnits))
	var refusal error
	for i, unit := range managedUnits {
		if present[i], err = checkOwnUnit(filepath.Join(dir, unit.name), true); err != nil {
			return ServiceReport{}, err
		}
		if !present[i] && refusal == nil {
			if pkg := u.packagedPath(unit.name); pkg != "" {
				refusal = &PackagedUnitError{Unit: unit.name, Path: pkg, Uninstall: true}
			}
		}
	}
	removed := false
	gone := map[string]bool{}
	for i, unit := range managedUnits {
		if !present[i] {
			continue
		}
		path := filepath.Join(dir, unit.name)
		step := []string{"disable", "--now", unit.name}
		if unit.static {
			step = []string{"stop", unit.name}
		}
		if _, err := u.systemctl(ctx, step...); err != nil {
			return ServiceReport{}, fmt.Errorf("systemctl --user %s: %w", strings.Join(step, " "), err)
		}
		if err := os.Remove(path); err != nil {
			return ServiceReport{}, fmt.Errorf("removing %s: %w", path, err)
		}
		removed, gone[unit.name] = true, true
	}
	if removed {
		if _, err := u.systemctl(ctx, "daemon-reload"); err != nil {
			return ServiceReport{}, fmt.Errorf("systemctl --user daemon-reload: %w", err)
		}
	} else if refusal != nil {
		return ServiceReport{}, refusal
	}
	return u.report(ctx, dir, removed, gone), nil
}

// Status reports every managed unit and whether lingering is on.
func (u *UserUnits) Status(ctx context.Context) (ServiceReport, error) {
	if err := u.checkSystemd(ctx); err != nil {
		return ServiceReport{}, err
	}
	mgr, err := u.managerDirs(ctx)
	if err != nil {
		return ServiceReport{}, err
	}
	return u.report(ctx, mgr.unitDir(), false, nil), nil
}

// RestartServeIfActive restarts lmm-serve.service with try-restart when it
// is running, so it runs the binary `lmm self-update` just installed (#569),
// and reports whether it did. A host without systemd, or a service that is
// not running, is nothing to restart and no error.
func (u *UserUnits) RestartServeIfActive(ctx context.Context) (bool, error) {
	if err := u.checkSystemd(ctx); err != nil {
		return false, nil
	}
	if u.query(ctx, "is-active", systemd.ServeUnitName, "inactive") != "active" {
		return false, nil
	}
	if _, err := u.systemctl(ctx, "try-restart", systemd.ServeUnitName); err != nil {
		return false, fmt.Errorf("systemctl --user try-restart %s: %w", systemd.ServeUnitName, err)
	}
	return true, nil
}

// report reads the state of every managed unit, with the user's own
// units in dir: lmm-serve.service always, and the auto-update units when
// they are installed or are among removed, the ones this call just took
// out. Its queries cannot fail in
// a way worth stopping for: is-enabled and is-active exit non-zero for any
// state but the positive one, and still print it.
func (u *UserUnits) report(ctx context.Context, dir string, changed bool, removed map[string]bool) ServiceReport {
	r := ServiceReport{Changed: changed, Units: make([]UnitStatus, 0, len(managedUnits))}
	for _, unit := range managedUnits {
		s := UnitStatus{Name: unit.name}
		if path := filepath.Join(dir, unit.name); fileExists(path) {
			s.Installed, s.Origin, s.Path = true, UnitOriginUser, path
		} else if pkg := u.packagedPath(unit.name); pkg != "" {
			s.Installed, s.Origin, s.Path = true, UnitOriginPackage, pkg
		}
		if unit.autoUpdate && !s.Installed && !removed[unit.name] {
			continue
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

// resolveExecutable returns the lmm binary the unit runs. On Linux
// os.Executable is already the resolved target, which can be a versioned
// path an upgrade replaces (a tarball under /opt/lmm-x.y, a nix store path,
// a mise install), so when the lmm on PATH is the same file its path is
// used instead, and keeps working when an upgrade repoints it. Otherwise it
// is os.Executable's. A `go run` build, deleted when it exits, is refused.
func (u *UserUnits) resolveExecutable() (string, error) {
	exe, err := u.Executable()
	if err != nil {
		return "", fmt.Errorf("finding the lmm binary: %w", err)
	}
	if u.LookPath != nil {
		if found, err := u.LookPath("lmm"); err == nil {
			if abs, err := filepath.Abs(found); err == nil && sameFile(abs, exe) {
				exe = abs
			}
		}
	}
	if isGoRunBuild(exe) {
		return "", fmt.Errorf("lmm is running from a temporary `go run` build (%s), which is deleted when it exits; install it (go install, make install) and run lmm service install from there", exe)
	}
	return exe, nil
}

// sameFile reports whether a and b name the same file, through symlinks.
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// isGoRunBuild reports whether exe is under a go-build<digits> directory,
// where `go run` puts the binary it deletes on exit.
func isGoRunBuild(exe string) bool {
	for part := range strings.SplitSeq(filepath.ToSlash(exe), "/") {
		digits, ok := strings.CutPrefix(part, "go-build")
		if ok && digits != "" && strings.Trim(digits, "0123456789") == "" {
			return true
		}
	}
	return false
}

// checkOwnUnit reports whether a unit file is at path and, when one is,
// that lmm wrote it: a regular file, not a symlink, starting with the
// header RenderServeUnit writes. Anything else is a *ForeignUnitError.
func checkOwnUnit(path string, uninstall bool) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return true, &ForeignUnitError{Path: path, Symlink: info.Mode()&fs.ModeSymlink != 0, Uninstall: uninstall}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return true, fmt.Errorf("reading %s: %w", path, err)
	}
	if !bytes.HasPrefix(data, []byte(unitHeaderMark)) {
		return true, &ForeignUnitError{Path: path, Uninstall: uninstall}
	}
	return true, nil
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

// unitHeaderMark starts the header line of every unit lmm writes; a file
// without it is not lmm's to overwrite or remove.
const unitHeaderMark = "# Written by `lmm service install`"

// serveUnitHeader is the line every unit `lmm service install` writes
// starts with, so a reader of ~/.config/systemd/user knows where the file
// came from and lmm knows the file is its own.
const serveUnitHeader = unitHeaderMark + "; `lmm service uninstall` removes it.\n"

// RenderServeUnit returns lmm-serve.service for the lmm binary at exe: the
// packaged unit (packaging/systemd) with its ExecStart pointed at exe,
// opts.Addr, opts.ConfigDir and opts.DataDir passed through as serve flags,
// and an Environment= line above it for each of opts.Environment. exe and
// the directories must be absolute, and a newline in any of them is refused
// rather than written into the unit.
func RenderServeUnit(exe string, opts ServiceInstallOptions) ([]byte, error) {
	args := []string{"serve", "--no-open"}
	if opts.Addr != "" {
		args = append(args, "--addr", opts.Addr)
	}
	execLine, err := renderExecStart(exe, args, opts)
	if err != nil {
		return nil, err
	}

	const directive = "ExecStart="
	var out bytes.Buffer
	out.WriteString(serveUnitHeader)
	found := false
	for line := range strings.SplitAfterSeq(string(systemd.ServeUnit), "\n") {
		if strings.HasPrefix(line, directive) {
			line = execLine
			found = true
		}
		out.WriteString(line)
	}
	if !found {
		return nil, errors.New("the embedded lmm-serve.service has no ExecStart line")
	}
	return out.Bytes(), nil
}

// RenderUpdateService returns lmm-update.service (#569) for the lmm binary
// at exe: a oneshot that runs `lmm self-update --auto`, which does nothing
// unless config.yaml says auto_update: true. opts.ConfigDir, opts.DataDir
// and opts.Environment reach it as they reach lmm-serve.service.
//
// A run that fails - the network not up yet after a resume, most often - is
// retried by systemd itself, Restart=on-failure five minutes apart and at
// most four starts an hour, rather than by a retry loop inside lmm: each
// attempt is its own journal entry, a failure that persists ends in the
// unit's failed state where `systemctl --user status` shows it, and a
// manual `lmm self-update` has no backoff to wait through. It names no
// network-online.target, which a user manager does not have. It sets no
// PrivateTmp=: in a user manager that needs unprivileged user namespaces,
// which some distributions turn off, and the unit would then not start.
func RenderUpdateService(exe string, opts ServiceInstallOptions) ([]byte, error) {
	execLine, err := renderExecStart(exe, []string{"self-update", "--auto"}, opts)
	if err != nil {
		return nil, err
	}
	return []byte(serveUnitHeader + `# lmm's unattended self-update (#569). lmm-update.timer starts it once a
# day; it installs a new lmm release only while config.yaml says
#   auto_update: true
# and otherwise exits at once, so that setting turns updates off without
# touching this file.

[Unit]
Description=lmm unattended self-update
Documentation=https://donovanmods.github.io/linux-mod-manager/updates.html
Documentation=man:lmm-self-update(1)
# A failed run (most often a network that is not up yet) is tried again
# five minutes later, up to three more times.
StartLimitIntervalSec=1h
StartLimitBurst=4

[Service]
Type=oneshot
` + execLine + `Restart=on-failure
RestartSec=5min
NoNewPrivileges=yes
`), nil
}

// RenderUpdateTimer returns lmm-update.timer (#569): lmm-update.service
// once a day at a random time in the first hour, and at the next boot or
// login when the machine was off at that time.
func RenderUpdateTimer(string, ServiceInstallOptions) ([]byte, error) {
	return []byte(serveUnitHeader + `# Starts lmm-update.service once a day (#569).

[Unit]
Description=Daily lmm self-update
Documentation=https://donovanmods.github.io/linux-mod-manager/updates.html

[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=1h

[Install]
WantedBy=timers.target
`), nil
}

// renderExecStart returns the Environment= lines and the ExecStart= line
// that run the lmm binary at exe with args, then opts.ConfigDir and
// opts.DataDir as --config and --data. exe and the directories must be
// absolute, and a line break anywhere is refused rather than written into
// the unit.
func renderExecStart(exe string, args []string, opts ServiceInstallOptions) (string, error) {
	if !filepath.IsAbs(exe) {
		return "", fmt.Errorf("the lmm binary path %q is not absolute", exe)
	}
	args = append([]string{exe}, args...)
	for _, d := range []struct{ flag, dir string }{{"--config", opts.ConfigDir}, {"--data", opts.DataDir}} {
		if d.dir == "" {
			continue
		}
		if !filepath.IsAbs(d.dir) {
			return "", fmt.Errorf("the %s directory %q is not absolute", d.flag, d.dir)
		}
		args = append(args, d.flag, d.dir)
	}
	var out strings.Builder
	for _, kv := range opts.Environment {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			return "", fmt.Errorf("%q is not a KEY=value environment entry", kv)
		}
		if strings.ContainsAny(kv, "\n\r") {
			return "", fmt.Errorf("%q cannot be written into a unit file: it contains a line break", kv)
		}
		out.WriteString("Environment=" + systemdQuoteEnv(kv) + "\n")
	}
	words := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, "\n\r") {
			return "", fmt.Errorf("%q cannot be written into a unit file: it contains a line break", a)
		}
		words[i] = systemdQuote(a)
	}
	out.WriteString("ExecStart=" + strings.Join(words, " ") + "\n")
	return out.String(), nil
}

// systemdQuote escapes one ExecStart word: % and $ are doubled so systemd
// expands neither a specifier nor a variable, and a word with whitespace,
// quotes or backslashes is double-quoted with those escaped.
func systemdQuote(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "%", "%%"), "$", "$$")
	if !strings.ContainsAny(s, " \t\"'\\;") {
		return s
	}
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// systemdQuoteEnv double-quotes one Environment= assignment: % is doubled
// so systemd expands no specifier, and backslashes and quotes are escaped.
// Environment= expands no $ variables, so $ stays as it is.
func systemdQuoteEnv(kv string) string {
	kv = strings.ReplaceAll(kv, "%", "%%")
	return `"` + strings.ReplaceAll(strings.ReplaceAll(kv, `\`, `\\`), `"`, `\"`) + `"`
}

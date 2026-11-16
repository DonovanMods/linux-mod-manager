package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/packaging/systemd"
)

// user_units_binary.go - which lmm binary each unit runs (#649). `lmm
// service install` writes the running binary's absolute path into
// ExecStart, and that path can be a versioned one (a mise Go install's
// .../go/1.27.3/bin/lmm) that a later build no longer writes to. Status and
// restart then name the binary the unit runs and flag one that is not the
// binary the user is running; RestartSwitch points lmm's own units at it.

// LmmBinary is one lmm binary and the version it reports (#649).
type LmmBinary struct {
	// Path is the binary's path: for a unit, as its ExecStart names it.
	Path string `json:"path"`
	// Version is what `<Path> --version` reports after "lmm version ", e.g.
	// "2.3.0" or "2.3.0 (dev: v2.3.0-484-gabc)"; UnknownBinaryVersion when
	// it could not be read.
	Version string `json:"version"`
}

// UnknownBinaryVersion is LmmBinary.Version for a binary that did not say
// its version in time: gone, hung, failing, or not lmm at all.
const UnknownBinaryVersion = "unknown"

// UnitChange says what a command did to one unit file (#649).
type UnitChange string

const (
	// UnitInstalled is a unit file written where there was none.
	UnitInstalled UnitChange = "installed"
	// UnitUpdated is an existing unit file rewritten with new content.
	UnitUpdated UnitChange = "updated"
)

// binaryVersionTimeout bounds one `<binary> --version` run: lmm answers it
// before it reads any config, so a binary that takes longer is reported
// unknown rather than holding up the command.
const binaryVersionTimeout = 3 * time.Second

// PackagedSwitchError refuses `lmm service restart --switch` for a unit a
// package installed that runs another lmm binary (#649): lmm never rewrites
// a package's file, and a copy under ~/.config would shadow it. The message
// says how to run the other binary instead.
type PackagedSwitchError struct {
	// Unit is the unit's file name, e.g. lmm-serve.service.
	Unit string
	// Path is the packaged unit file.
	Path string
	// Binary is the binary the unit runs.
	Binary string
	// Running is the binary the switch was asked to point it at.
	Running string
}

// Error names both binaries and the ways to run the other one.
func (e *PackagedSwitchError) Error() string {
	return fmt.Sprintf("%s was installed by your lmm package (%s) and runs %s, so lmm will not rewrite it to run %s; run that lmm with `lmm serve`, or override the unit's ExecStart with: systemctl --user edit %s",
		e.Unit, e.Path, e.Binary, e.Running, strings.TrimSuffix(e.Unit, ".service"))
}

// readBinaryVersion is UserUnits.BinaryVersion's real implementation: it
// runs `<path> --version` and returns what follows "lmm version ". Cobra
// answers --version before any command runs, so nothing is read or
// written by the binary it asks.
func readBinaryVersion(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return parseVersionOutput(string(out))
}

// parseVersionOutput reads `lmm --version`'s "lmm version X" line.
func parseVersionOutput(out string) (string, error) {
	line := strings.TrimSpace(firstLine(strings.TrimSpace(out)))
	v, ok := strings.CutPrefix(line, "lmm version ")
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("not lmm's version line: %q", line)
	}
	return strings.TrimSpace(v), nil
}

// runningBinary is the lmm binary this process runs, as install would
// write it into a unit: the lmm on PATH when that is the same file, else
// the executable's own path.
func (u *UserUnits) runningBinary() (string, error) {
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
	return exe, nil
}

// binaryVersion asks the binary at path for its version, bounded by
// binaryVersionTimeout; any failure is UnknownBinaryVersion.
func (u *UserUnits) binaryVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, binaryVersionTimeout)
	defer cancel()
	v, err := u.BinaryVersion(ctx, path)
	if err != nil || v == "" {
		return UnknownBinaryVersion
	}
	return v
}

// runsBinary reports whether a managed unit has an ExecStart, and so a
// binary to report: every unit but the timer.
func runsBinary(name string) bool {
	return name != UpdateTimerName
}

// addBinaries fills r.Running and each installed unit's Binary and
// BinaryMismatch (#649). Without a BinaryVersion seam it adds nothing, so
// a caller that does not ask gets the document it always did.
func (u *UserUnits) addBinaries(ctx context.Context, r *ServiceReport) {
	if u.BinaryVersion == nil {
		return
	}
	running, err := u.runningBinary()
	if err == nil {
		v := u.Version
		if v == "" {
			v = UnknownBinaryVersion
		}
		r.Running = &LmmBinary{Path: running, Version: v}
	}
	for i := range r.Units {
		s := &r.Units[i]
		if !s.Installed || !runsBinary(s.Name) {
			continue
		}
		path := u.unitExecBinary(ctx, s.Name, s.Path)
		if path == "" {
			continue
		}
		s.Binary = &LmmBinary{Path: path, Version: u.binaryVersion(ctx, path)}
		s.BinaryMismatch = r.Running != nil && !sameBinary(path, running)
	}
}

// unitExecBinary is the program a unit's ExecStart runs: what the user
// manager has loaded (drop-ins included) when it says, else the first
// ExecStart= line of the unit file at path. "" when neither names one.
func (u *UserUnits) unitExecBinary(ctx context.Context, name, path string) string {
	if out, err := u.systemctl(ctx, "show", "-p", "ExecStart", "--value", name); err == nil {
		if p := execPathFromShow(string(out)); p != "" {
			return p
		}
	}
	return u.unitFileBinary(path)
}

// unitFileBinary is the program of the first ExecStart= line in the unit
// file at path; "" when it has none or cannot be read.
func (u *UserUnits) unitFileBinary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		if word, _, ok := splitExecProgram(strings.TrimSpace(v)); ok {
			return unquoteExecWord(word)
		}
	}
	return ""
}

// execPathFromShow reads the path= field of `systemctl show -p ExecStart
// --value`'s first entry: "{ path=/usr/bin/lmm ; argv[]=... ; ... }".
func execPathFromShow(out string) string {
	_, rest, ok := strings.Cut(out, "path=")
	if !ok {
		return ""
	}
	if end := strings.Index(rest, " ;"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// splitExecProgram splits an ExecStart= value into its first word, as
// written (quotes and escapes kept), and the rest of the line.
func splitExecProgram(v string) (word, rest string, ok bool) {
	start := len(v) - len(strings.TrimLeft(v, "-@:+!"))
	if strings.HasPrefix(v[start:], `"`) {
		for i := start + 1; i < len(v); i++ {
			switch v[i] {
			case '\\':
				i++
			case '"':
				return v[:i+1], v[i+1:], true
			}
		}
		return "", "", false
	}
	if i := strings.IndexAny(v, " \t"); i >= 0 {
		return v[:i], v[i:], true
	}
	return v, "", v != ""
}

// unquoteExecWord undoes systemdQuote on one ExecStart word, after any of
// systemd's executable prefixes (- @ : + !).
func unquoteExecWord(word string) string {
	word = strings.TrimLeft(word, "-@:+!")
	if len(word) >= 2 && word[0] == '"' && word[len(word)-1] == '"' {
		var b strings.Builder
		inner := word[1 : len(word)-1]
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				i++
			}
			b.WriteByte(inner[i])
		}
		word = b.String()
	}
	return strings.ReplaceAll(strings.ReplaceAll(word, "%%", "%"), "$$", "$")
}

// sameBinary reports whether a and b are one binary: the same file, or -
// when either cannot be looked at, say a running binary since replaced -
// the same path once symlinks are resolved.
func sameBinary(a, b string) bool {
	return sameFile(a, b) || resolvedBinaryPath(a) == resolvedBinaryPath(b)
}

// resolvedBinaryPath is path with symlinks resolved where it exists, and
// the " (deleted)" Linux appends to a replaced running executable dropped.
func resolvedBinaryPath(path string) string {
	path = strings.TrimSuffix(path, " (deleted)")
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return filepath.Clean(path)
}

// switchExecStart rewrites the program of every ExecStart= line in a unit
// lmm wrote to exe, leaving its arguments and every other line as they
// are, and returns the program it replaced ("" when there was none).
func switchExecStart(data []byte, exe string) ([]byte, string, error) {
	const directive = "ExecStart="
	var out strings.Builder
	previous := ""
	for line := range strings.SplitAfterSeq(string(data), "\n") {
		v, ok := strings.CutPrefix(line, directive)
		if !ok {
			out.WriteString(line)
			continue
		}
		word, rest, ok := splitExecProgram(v)
		if !ok {
			return nil, "", fmt.Errorf("cannot read the program of %q", strings.TrimSpace(line))
		}
		if previous == "" {
			previous = unquoteExecWord(word)
		}
		out.WriteString(directive + systemdQuote(exe) + rest)
	}
	return []byte(out.String()), previous, nil
}

// RestartSwitch is `lmm service restart --switch` (#649): it points
// lmm-serve.service - and lmm-update.service, when lmm installed it - at
// the running lmm binary, changing only the program in each ExecStart and
// keeping every argument and setting install wrote, reloads the user
// manager, then restarts lmm-serve.service as Restart does. A unit already
// on this binary is not rewritten. Before anything is written or run: a
// unit file lmm did not write is a *ForeignUnitError; a packaged unit that
// runs another binary is a *PackagedSwitchError (one that runs this binary
// is just restarted); none installed is ErrServiceNotInstalled; and a `go
// run` build is refused, as install refuses it.
func (u *UserUnits) RestartSwitch(ctx context.Context) (ServiceReport, error) {
	if err := u.checkSystemd(ctx); err != nil {
		return ServiceReport{}, err
	}
	mgr, err := u.managerDirs(ctx)
	if err != nil {
		return ServiceReport{}, err
	}
	dir := mgr.unitDir()
	servePath := filepath.Join(dir, systemd.ServeUnitName)
	own, err := checkOwnUnit(servePath, false)
	if err != nil {
		return ServiceReport{}, err
	}
	pkg := ""
	if !own {
		if pkg = u.packagedPath(systemd.ServeUnitName); pkg == "" {
			return ServiceReport{}, ErrServiceNotInstalled
		}
	}
	exe, err := u.resolveExecutable()
	if err != nil {
		return ServiceReport{}, err
	}
	if pkg != "" {
		if bin := u.unitExecBinary(ctx, systemd.ServeUnitName, pkg); bin != "" && !sameBinary(bin, exe) {
			return ServiceReport{}, &PackagedSwitchError{Unit: systemd.ServeUnitName, Path: pkg, Binary: bin, Running: exe}
		}
		return u.restart(ctx, dir)
	}

	type rewrite struct {
		name, path, previous string
		data                 []byte
	}
	var rewrites []rewrite
	for _, unit := range managedUnits {
		if !runsBinary(unit.name) {
			continue
		}
		path := filepath.Join(dir, unit.name)
		present, err := checkOwnUnit(path, false)
		if err != nil {
			return ServiceReport{}, err
		}
		if !present {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return ServiceReport{}, fmt.Errorf("reading %s: %w", path, err)
		}
		next, previous, err := switchExecStart(data, exe)
		if err != nil {
			return ServiceReport{}, fmt.Errorf("%s: %w", path, err)
		}
		rewrites = append(rewrites, rewrite{name: unit.name, path: path, previous: previous, data: next})
	}
	changed := map[string]string{}
	for _, rw := range rewrites {
		wrote, err := writeUnitFile(rw.path, rw.data)
		if err != nil {
			return ServiceReport{}, err
		}
		if wrote {
			changed[rw.name] = rw.previous
		}
	}
	if len(changed) > 0 {
		if _, err := u.systemctl(ctx, "daemon-reload"); err != nil {
			return ServiceReport{}, fmt.Errorf("systemctl --user daemon-reload: %w", err)
		}
	}
	r, err := u.restart(ctx, dir)
	if err != nil {
		return r, err
	}
	r.Changed = len(changed) > 0
	for i := range r.Units {
		if previous, ok := changed[r.Units[i].Name]; ok {
			r.Units[i].Change = UnitUpdated
			if previous != exe {
				r.Units[i].PreviousBinary = previous
			}
		}
	}
	return r, nil
}

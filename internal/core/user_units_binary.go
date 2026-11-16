package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

// binaryVersionWaitDelay bounds how long a `--version` run's output pipe
// may stay open after the process exits or is killed - a child it started
// still holding it - before the run returns.
const binaryVersionWaitDelay = 500 * time.Millisecond

// binaryVersionMaxOutput caps what a `--version` run's output is kept of:
// lmm prints one short line, and anything past this is not lmm.
const binaryVersionMaxOutput = 4 << 10

// readBinaryVersion is UserUnits.BinaryVersion's real implementation: it
// runs `<path> --version` and returns what follows "lmm version ". Cobra
// answers --version before any command runs, so nothing is read or
// written by the binary it asks.
//
// path is whatever a unit's ExecStart names, and a drop-in can name any
// program, which is then run with --version. That is acceptable: it is the
// user's own unit, which systemd runs as the same user anyway, so nothing
// gains a privilege. The run is still kept small and bounded: its own
// process group, killed whole when ctx ends; a minimal environment (PATH
// and HOME, never the shell's credentials); its output capped; and its
// pipes closed shortly after it exits, however long a child it left
// behind holds them.
func readBinaryVersion(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = minimalEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = binaryVersionWaitDelay
	out := &limitedBuffer{max: binaryVersionMaxOutput}
	cmd.Stdout = out
	err := cmd.Run()
	if cmd.Process != nil {
		// A child the binary left behind, still in its group.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return "", err
	}
	return parseVersionOutput(out.String())
}

// minimalEnv is the environment a `--version` run gets: PATH and HOME,
// as this process has them.
func minimalEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// limitedBuffer keeps the first max bytes written to it and accepts, and
// drops, the rest, so a writer never blocks or fails on it.
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

// Write keeps what fits under max and reports all of p written.
func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// String is what was kept.
func (b *limitedBuffer) String() string { return b.buf.String() }

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
		path, known := u.unitExecBinary(ctx, s.Name, s.Path)
		switch {
		case path != "":
			s.Binary = &LmmBinary{Path: path, Version: u.binaryVersion(ctx, path)}
			s.BinaryMismatch = r.Running != nil && !sameBinary(path, running)
		case known:
			// systemd has the unit loaded and names no program - a drop-in
			// reset ExecStart - so what the unit file says is not what runs.
			s.Binary = &LmmBinary{Version: UnknownBinaryVersion}
		}
	}
}

// unitExecBinary is the program a unit's ExecStart runs. When the user
// manager has the unit loaded, it is what systemd says, drop-ins included,
// and known is true even when that names none (a drop-in's reset). Only
// when systemd has not loaded the unit - or cannot be asked - is it the
// unit file at path's own ExecStart. "" with known false: neither names
// one.
func (u *UserUnits) unitExecBinary(ctx context.Context, name, path string) (program string, known bool) {
	if out, err := u.systemctl(ctx, "show", "-p", "LoadState", "-p", "ExecStart", name); err == nil {
		if p, loaded := execStartFromShow(string(out)); loaded {
			return p, true
		}
	}
	return unitFileBinary(path), false
}

// execStartFromShow reads `systemctl show -p LoadState -p ExecStart`: the
// path= of the first ExecStart entry ("{ path=/usr/bin/lmm ; argv[]=... ;
// ... }"), and whether systemd has the unit loaded - any load state but
// not-found (or none), so a unit a drop-in left without an ExecStart
// (bad-setting) still counts as systemd's answer.
func execStartFromShow(out string) (program string, loaded bool) {
	for line := range strings.SplitSeq(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "LoadState":
			loaded = v != "" && v != "not-found"
		case "ExecStart":
			if program != "" {
				continue
			}
			if _, rest, ok := strings.Cut(v, "path="); ok {
				if end := strings.Index(rest, " ;"); end >= 0 {
					rest = rest[:end]
				}
				program = strings.TrimSpace(rest)
			}
		}
	}
	return program, loaded
}

// unitFileBinary is the program of the ExecStart that runs in the unit
// file at path: the first entry after the last reset (an empty
// `ExecStart=`), as systemd builds the list. "" when there is none, or the
// file cannot be read.
func unitFileBinary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	program := ""
	for line := range strings.SplitAfterSeq(string(data), "\n") {
		l, ok, err := parseExecStartLine(line)
		switch {
		case !ok || err != nil:
		case l.reset():
			program = ""
		case program == "":
			program = l.program
		}
	}
	return program
}

// execStartLine is one ExecStart= line of a unit file, taken apart so the
// program can be replaced and every other byte put back as it was.
type execStartLine struct {
	// lead is everything up to the value: indent, the key, the = and the
	// blanks around it.
	lead string
	// prefixes are systemd's executable prefixes (- @ : + ! !!) in front
	// of the program.
	prefixes string
	// program is the program, unquoted and unescaped; empty for a reset.
	program string
	// rest is everything after the program as written - the arguments -
	// and, for a reset, the whole blank value.
	rest string
	// eol is the line's ending: "\n", "\r\n" or "" for a last line without.
	eol string
}

// reset reports an empty `ExecStart=`, which clears the list before it.
func (l execStartLine) reset() bool { return l.program == "" }

// execPrefixes are the characters systemd reads in front of an ExecStart
// program.
const execPrefixes = "-@:+!"

// parseExecStartLine takes apart line when it is an ExecStart= line: ok is
// false for any other line, and err is a program it cannot read (an
// unterminated quote).
func parseExecStartLine(line string) (l execStartLine, ok bool, err error) {
	body := line
	switch {
	case strings.HasSuffix(body, "\r\n"):
		l.eol, body = "\r\n", strings.TrimSuffix(body, "\r\n")
	case strings.HasSuffix(body, "\n"):
		l.eol, body = "\n", strings.TrimSuffix(body, "\n")
	}
	key, value, found := strings.Cut(body, "=")
	if !found || strings.TrimSpace(key) != "ExecStart" {
		return execStartLine{}, false, nil
	}
	trimmed := strings.TrimLeft(value, " \t")
	l.lead = body[:len(body)-len(trimmed)]
	if strings.TrimSpace(trimmed) == "" {
		l.rest = trimmed
		return l, true, nil
	}
	word, rest, err := splitExecWord(trimmed)
	if err != nil {
		return execStartLine{}, true, err
	}
	l.rest = rest
	// A prefix may come before quotes or inside them; systemd reads both.
	l.prefixes = word[:len(word)-len(strings.TrimLeft(word, execPrefixes))]
	program := unquoteExecWord(word[len(l.prefixes):])
	inner := strings.TrimLeft(program, execPrefixes)
	l.prefixes += program[:len(program)-len(inner)]
	l.program = inner
	if l.program == "" {
		return execStartLine{}, true, fmt.Errorf("no program in %q", strings.TrimSpace(body))
	}
	return l, true, nil
}

// splitExecWord splits an ExecStart= value into its first word, as written
// (prefixes, quotes and escapes kept), and the rest.
func splitExecWord(v string) (word, rest string, err error) {
	start := len(v) - len(strings.TrimLeft(v, execPrefixes))
	if strings.HasPrefix(v[start:], `"`) {
		for i := start + 1; i < len(v); i++ {
			switch v[i] {
			case '\\':
				i++
			case '"':
				return v[:i+1], v[i+1:], nil
			}
		}
		return "", "", fmt.Errorf("unterminated quote in %q", v)
	}
	if i := strings.IndexAny(v, " \t"); i >= 0 {
		return v[:i], v[i:], nil
	}
	return v, "", nil
}

// unquoteExecWord undoes systemdQuote on one ExecStart word.
func unquoteExecWord(word string) string {
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
// lmm wrote to exe, keeping each line's indent, systemd's executable
// prefixes, its arguments and its line ending, and every other line as it
// is; a reset (`ExecStart=` with nothing after it) passes through. It
// returns the first program it replaced ("" when there was none), and
// refuses a line whose program it cannot read rather than guess.
func switchExecStart(data []byte, exe string) ([]byte, string, error) {
	var out strings.Builder
	previous := ""
	for line := range strings.SplitAfterSeq(string(data), "\n") {
		l, ok, err := parseExecStartLine(line)
		if err != nil {
			return nil, "", fmt.Errorf("cannot read the program of %q: %w", strings.TrimSpace(line), err)
		}
		if !ok || l.reset() {
			out.WriteString(line)
			continue
		}
		if previous == "" {
			previous = l.program
		}
		out.WriteString(l.lead + l.prefixes + systemdQuote(exe) + l.rest + l.eol)
	}
	return []byte(out.String()), previous, nil
}

// SwitchOverriddenError is `lmm service restart --switch` finding, after
// it rewrote the unit file, reloaded and restarted, that the service still
// does not run the binary it switched to (#649): a drop-in overrides
// ExecStart, and lmm never edits a drop-in.
type SwitchOverriddenError struct {
	// Unit is the unit's file name, e.g. lmm-serve.service.
	Unit string
	// Binary is what the unit runs after the switch; empty when systemd
	// names no program at all.
	Binary string
	// Running is the binary the switch pointed the unit file at.
	Running string
}

// Error names both binaries and where to look.
func (e *SwitchOverriddenError) Error() string {
	runs := e.Binary
	if runs == "" {
		runs = "no program lmm can name"
	}
	name := strings.TrimSuffix(e.Unit, ".service")
	return fmt.Sprintf("%s was pointed at %s and restarted, but it still runs %s: a drop-in overrides ExecStart (see: systemctl --user cat %s); change or remove it with: systemctl --user edit %s",
		e.Unit, e.Running, runs, name, name)
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
		if bin, _ := u.unitExecBinary(ctx, systemd.ServeUnitName, pkg); bin != "" && !sameBinary(bin, exe) {
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
	// What systemd runs now, drop-ins included, must be this binary;
	// otherwise the switch did not take, whatever the file says.
	if bin, known := u.unitExecBinary(ctx, systemd.ServeUnitName, servePath); known && (bin == "" || !sameBinary(bin, exe)) {
		return r, &SwitchOverriddenError{Unit: systemd.ServeUnitName, Binary: bin, Running: exe}
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

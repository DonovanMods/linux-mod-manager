package core

// game_running.go answers "is this game running right now?" for the
// automatic update pass (#587 decision 2): an update that replaces files
// under a running game can crash it or be half-read by it, so the pass
// waits for the game to close. Two signals, in order - a game lmm itself
// launched (#501), whose transient lmm-launch-<game>-* unit is still
// active, and otherwise a best-effort scan of the running processes for one
// whose executable sits under the game's install path. When neither can
// tell, the answer is "not running": an update that cannot be postponed on
// evidence is applied, as it was before.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// GameRunningSignal names how a running game was recognised - the wire
// value of ModAutoUpdate.DetectedBy.
type GameRunningSignal string

// The two signals GameRunning reads, in the order it reads them.
const (
	// GameRunningLaunchUnit: a game `lmm launch` started is still running
	// in its transient lmm-launch-<game>-* systemd user unit.
	GameRunningLaunchUnit GameRunningSignal = "launch_unit"
	// GameRunningProcess: a running process's executable, or a path its
	// command line names (a Wine or Proton game's .exe), is under the
	// game's install path.
	GameRunningProcess GameRunningSignal = "process"
)

// GameRunningStatus is GameRunning's answer.
type GameRunningStatus struct {
	// Running is true only on evidence; "could not tell" is false.
	Running bool
	// Signal says which check saw it; empty when not running.
	Signal GameRunningSignal
	// Detail is what was seen - the unit's name, or the process's path -
	// for a log line.
	Detail string
}

// ProcessInfo is one running process as GameProbe.Processes reports it.
type ProcessInfo struct {
	// PID is the process id.
	PID int
	// Exe is the process's executable (/proc/<pid>/exe), empty when it
	// cannot be read.
	Exe string
	// Cmdline is the process's arguments, argv[0] first.
	Cmdline []string
}

// GameProbe is how GameRunning looks at the machine: every field is a seam
// a test replaces, like Launcher's. NewGameProbe fills them from the
// machine; a nil field skips its check.
type GameProbe struct {
	// Run runs a command and returns its stdout - systemctl, here.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Processes lists the running processes.
	Processes func() ([]ProcessInfo, error)
}

// NewGameProbe returns a GameProbe for this machine: systemctl for the
// launch units, /proc for the processes.
func NewGameProbe() *GameProbe {
	return &GameProbe{
		Run:       runCommand,
		Processes: func() ([]ProcessInfo, error) { return procProcesses("/proc") },
	}
}

// SetGameProbe registers how this Service tells a running game. The
// composition root registers the machine's (app.Open); until then
// GameRunning can never tell, and answers not running.
func (s *Service) SetGameProbe(p *GameProbe) {
	s.gameProbePtr.Store(p)
}

// GameRunning reports whether game is running, reading the launch units
// first and the processes second. Every failure to look is "could not
// tell", which is not running: the caller then does what it would have
// done without the check.
func (s *Service) GameRunning(ctx context.Context, game *domain.Game) GameRunningStatus {
	p := s.gameProbePtr.Load()
	if p == nil || game == nil {
		return GameRunningStatus{}
	}
	if p.Run != nil {
		if unit, ok := activeLaunchUnit(ctx, p.Run, game.ID); ok {
			return GameRunningStatus{Running: true, Signal: GameRunningLaunchUnit, Detail: unit}
		}
	}
	if p.Processes != nil {
		if path, ok := processUnder(p.Processes, game.InstallPath); ok {
			return GameRunningStatus{Running: true, Signal: GameRunningProcess, Detail: path}
		}
	}
	return GameRunningStatus{}
}

// activeLaunchUnit returns the first active lmm-launch-<gameID>-<suffix>
// service. The glob alone is not enough - game "g1"'s pattern also matches
// game "g1-extra"'s units - so each name is held to launchUnitName's exact
// shape: the game's own prefix, then one hex suffix as randomUnitSuffix
// makes it.
func activeLaunchUnit(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), gameID string) (string, bool) {
	prefix := launchUnitName(gameID, "")
	out, err := run(ctx, "systemctl", "--user", "list-units", "--type=service", "--state=active",
		"--plain", "--no-legend", "--no-pager", prefix+"*")
	if err != nil {
		return "", false
	}
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		suffix, ok := strings.CutPrefix(strings.TrimSuffix(name, ".service"), prefix)
		if ok && isHexSuffix(suffix) {
			return name, true
		}
	}
	return "", false
}

// isHexSuffix reports whether s is a non-empty run of lowercase hex digits
// - randomUnitSuffix's alphabet, and never a game id's remainder ("extra-…").
func isHexSuffix(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// processUnder returns the path of the first process whose executable, or
// a path its command line names, is under installPath. An install path
// that is not absolute, or is the root, would match everything and so
// tells nothing.
func processUnder(list func() ([]ProcessInfo, error), installPath string) (string, bool) {
	root := filepath.Clean(installPath)
	if installPath == "" || !filepath.IsAbs(root) || root == "/" {
		return "", false
	}
	procs, err := list()
	if err != nil {
		return "", false
	}
	self := os.Getpid()
	for _, p := range procs {
		if p.PID == self {
			continue
		}
		for _, candidate := range append([]string{p.Exe}, p.Cmdline...) {
			if path := unixPathOf(candidate); path != "" && pathUnder(path, root) {
				return path, true
			}
		}
	}
	return "", false
}

// unixPathOf reads a process's executable or argument as a path: a Unix
// path as it is, a Wine "Z:\..." path (Wine's Z: is the Unix root) as the
// Unix path it names, and anything else - a flag, a C:\ path inside a
// prefix - as no path at all.
func unixPathOf(s string) string {
	if strings.HasPrefix(s, "/") {
		return s
	}
	if len(s) > 2 && (s[0] == 'Z' || s[0] == 'z') && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return strings.ReplaceAll(s[2:], `\`, "/")
	}
	return ""
}

// pathUnder reports whether path is root or inside it.
func pathUnder(path, root string) bool {
	path = filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+"/")
}

// procProcesses lists the processes under a /proc-shaped root. A process
// that exits mid-scan, or whose exe the user may not read, is reported
// with what could be read.
func procProcesses(root string) ([]ProcessInfo, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var procs []ProcessInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		p := ProcessInfo{PID: pid}
		if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
			p.Exe = exe
		}
		if raw, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
			for arg := range strings.SplitSeq(strings.TrimRight(string(raw), "\x00"), "\x00") {
				if arg != "" {
					p.Cmdline = append(p.Cmdline, arg)
				}
			}
		}
		if p.Exe != "" || len(p.Cmdline) > 0 {
			procs = append(procs, p)
		}
	}
	return procs, nil
}

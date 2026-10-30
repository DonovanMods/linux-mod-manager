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
	"fmt"
	"io/fs"
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
// machine; a nil field skips the check that needs it.
type GameProbe struct {
	// Run runs a command and returns its stdout - systemctl, here.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Processes lists the running processes.
	Processes func() ([]ProcessInfo, error)
	// UnitPIDs lists the processes in a user unit's control group.
	UnitPIDs func(ctx context.Context, unit string) ([]int, error)
}

// NewGameProbe returns a GameProbe for this machine: systemctl for the
// launch units, /proc for the processes, and the unified cgroup hierarchy
// for a unit's own processes.
func NewGameProbe() *GameProbe {
	return &GameProbe{
		Run:       runCommand,
		Processes: func() ([]ProcessInfo, error) { return procProcesses("/proc") },
		UnitPIDs: func(ctx context.Context, unit string) ([]int, error) {
			return unitPIDs(ctx, runCommand, "/sys/fs/cgroup", unit)
		},
	}
}

// SetGameProbe registers how this Service tells a running game. The
// composition root registers the machine's (app.Open); until then
// GameRunning can never tell, and answers not running.
func (s *Service) SetGameProbe(p *GameProbe) {
	s.gameProbePtr.Store(p)
}

// GameRunning reports whether game is running: a process of the game in
// one of its active lmm-launch units first, then any process of the game.
// "A process of the game" is one whose executable, or a path its command
// line names, is under the game's install path - so a launcher `lmm
// launch` started in the unit (Battle.net, or a Steam client that was not
// already running) keeps nothing waiting once the game itself has exited.
// Every failure to look is "could not tell", which is not running: the
// caller then does what it would have done without the check.
func (s *Service) GameRunning(ctx context.Context, game *domain.Game) GameRunningStatus {
	p := s.gameProbePtr.Load()
	if p == nil || game == nil || p.Processes == nil {
		return GameRunningStatus{}
	}
	root, ok := installRoot(game.InstallPath)
	if !ok {
		return GameRunningStatus{}
	}
	procs, err := p.Processes()
	if err != nil {
		return GameRunningStatus{}
	}
	if p.Run != nil && p.UnitPIDs != nil {
		for _, unit := range activeLaunchUnits(ctx, p.Run, game.ID) {
			pids, err := p.UnitPIDs(ctx, unit)
			if err != nil {
				continue
			}
			inUnit := make(map[int]bool, len(pids))
			for _, pid := range pids {
				inUnit[pid] = true
			}
			if _, ok := processUnder(procs, root, func(pid int) bool { return inUnit[pid] }); ok {
				return GameRunningStatus{Running: true, Signal: GameRunningLaunchUnit, Detail: unit}
			}
		}
	}
	if path, ok := processUnder(procs, root, func(int) bool { return true }); ok {
		return GameRunningStatus{Running: true, Signal: GameRunningProcess, Detail: path}
	}
	return GameRunningStatus{}
}

// activeLaunchUnits returns the active lmm-launch-<gameID>-<suffix>
// services. The glob alone is not enough - game "g1"'s pattern also matches
// game "g1-extra"'s units - so each name is held to launchUnitName's exact
// shape: the game's own prefix, then one hex suffix as randomUnitSuffix
// makes it.
func activeLaunchUnits(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), gameID string) []string {
	prefix := launchUnitName(gameID, "")
	out, err := run(ctx, "systemctl", "--user", "list-units", "--type=service", "--state=active",
		"--plain", "--no-legend", "--no-pager", prefix+"*")
	if err != nil {
		return nil
	}
	var units []string
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		suffix, ok := strings.CutPrefix(strings.TrimSuffix(name, ".service"), prefix)
		if ok && isHexSuffix(suffix) {
			units = append(units, name)
		}
	}
	return units
}

// unitPIDs lists the processes in unit's control group, and in any group
// below it, under cgroupRoot (the unified hierarchy's mount).
func unitPIDs(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), cgroupRoot, unit string) ([]int, error) {
	out, err := run(ctx, "systemctl", "--user", "show", "--property=ControlGroup", "--value", unit)
	if err != nil {
		return nil, err
	}
	group := strings.TrimSpace(string(out))
	if group == "" {
		return nil, fmt.Errorf("unit %s has no control group", unit)
	}
	var pids []int
	err = filepath.WalkDir(filepath.Join(cgroupRoot, group), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "cgroup.procs" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, field := range strings.Fields(string(raw)) {
			if pid, err := strconv.Atoi(field); err == nil {
				pids = append(pids, pid)
			}
		}
		return nil
	})
	return pids, err
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

// installRoot is the install path GameRunning matches processes against,
// and false when it cannot tell anything: empty, relative, or the root,
// which would match every process.
func installRoot(installPath string) (string, bool) {
	root := filepath.Clean(installPath)
	if installPath == "" || !filepath.IsAbs(root) || root == "/" {
		return "", false
	}
	return root, true
}

// processUnder returns the path of the first process, among those pick
// accepts, whose executable or a path its command line names is under
// root. lmm's own process is never one.
func processUnder(procs []ProcessInfo, root string, pick func(pid int) bool) (string, bool) {
	self := os.Getpid()
	driveC := prefixDriveC(root)
	for _, p := range procs {
		if p.PID == self || !pick(p.PID) {
			continue
		}
		for _, candidate := range append([]string{p.Exe}, p.Cmdline...) {
			if path := unixPathOf(candidate, driveC); path != "" && pathUnder(path, root) {
				return path, true
			}
		}
	}
	return "", false
}

// prefixDriveC is the drive_c directory of the Wine prefix root is inside,
// or "" when root is not inside one. A game installed in a prefix (#545's
// Battle.net games) runs with Windows paths on its command line, C:\
// meaning that directory.
func prefixDriveC(root string) string {
	if i := strings.Index(root+"/", "/drive_c/"); i >= 0 {
		return root[:i] + "/drive_c"
	}
	return ""
}

// unixPathOf reads a process's executable or argument as a path: a Unix
// path as it is, a Wine "Z:\..." path (Wine's Z: is the Unix root) as the
// Unix path it names, a "C:\..." path as the one under driveC when the
// game lives in a prefix, and anything else - a flag, another drive - as no
// path at all.
func unixPathOf(s, driveC string) string {
	if strings.HasPrefix(s, "/") {
		return s
	}
	if len(s) < 3 || s[1] != ':' || (s[2] != '\\' && s[2] != '/') {
		return ""
	}
	rest := strings.ReplaceAll(s[2:], `\`, "/")
	switch s[0] {
	case 'Z', 'z':
		return rest
	case 'C', 'c':
		if driveC != "" {
			return driveC + rest
		}
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

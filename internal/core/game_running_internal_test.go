package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProcProcesses_ReadsAProcShapedTree reads a /proc-shaped tree built in
// a temp dir - never the machine's own /proc: the exe link and the
// NUL-separated cmdline of each numeric entry, skipping everything else.
func TestProcProcesses_ReadsAProcShapedTree(t *testing.T) {
	root := t.TempDir()
	proc := func(pid, exe, cmdline string) {
		dir := filepath.Join(root, pid)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		if exe != "" {
			require.NoError(t, os.Symlink(exe, filepath.Join(dir, "exe")))
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644))
	}
	proc("101", "/games/Thing/thing", "/games/Thing/thing\x00--fullscreen\x00")
	proc("202", "", `Z:\games\Thing\Game.exe`+"\x00")
	proc("303", "", "") // a kernel thread: nothing to read
	require.NoError(t, os.MkdirAll(filepath.Join(root, "self"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "uptime"), []byte("1 2"), 0o644))

	procs, err := procProcesses(root)
	require.NoError(t, err)
	assert.ElementsMatch(t, []ProcessInfo{
		{PID: 101, Exe: "/games/Thing/thing", Cmdline: []string{"/games/Thing/thing", "--fullscreen"}},
		{PID: 202, Cmdline: []string{`Z:\games\Thing\Game.exe`}},
	}, procs)
}

// TestUnitPIDs_ReadsTheUnitsControlGroup reads a unit's processes from a
// cgroup-shaped tree in a temp dir - never the machine's - including a
// group nested below the unit's own.
func TestUnitPIDs_ReadsTheUnitsControlGroup(t *testing.T) {
	root := t.TempDir()
	group := "/user.slice/user-1000.slice/user@1000.service/app.slice/lmm-launch-g1-deadbeef.service"
	require.NoError(t, os.MkdirAll(filepath.Join(root, group, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, group, "cgroup.procs"), []byte("101\n102\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, group, "sub", "cgroup.procs"), []byte("203\n"), 0o644))

	var asked []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		asked = append([]string{name}, args...)
		return []byte(group + "\n"), nil
	}
	pids, err := unitPIDs(context.Background(), run, root, "lmm-launch-g1-deadbeef.service")
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{101, 102, 203}, pids)
	assert.Equal(t, []string{"systemctl", "--user", "show", "--property=ControlGroup", "--value", "lmm-launch-g1-deadbeef.service"}, asked)

	_, err = unitPIDs(context.Background(), func(context.Context, string, ...string) ([]byte, error) { return []byte("\n"), nil }, root, "x.service")
	assert.Error(t, err, "a unit with no control group has no processes to read")
}

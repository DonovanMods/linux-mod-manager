package core

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// switchExecStart changes only the program of each ExecStart= line (#649
// review): a line with no arguments keeps its line ending, an empty reset
// line passes through, CRLF survives, systemd's executable prefixes stay
// in front of the new program, and an indented line or one with spaces
// around the = is rewritten like any other.
func TestSwitchExecStart(t *testing.T) {
	for name, tc := range map[string]struct {
		in, want, previous string
	}{
		"no arguments": {
			in:       "ExecStart=/bin/lmm\nRestart=on-failure\n",
			want:     "ExecStart=/new/lmm\nRestart=on-failure\n",
			previous: "/bin/lmm",
		},
		"no arguments, last line without a newline": {
			in:       "Restart=on-failure\nExecStart=/bin/lmm",
			want:     "Restart=on-failure\nExecStart=/new/lmm",
			previous: "/bin/lmm",
		},
		"CRLF": {
			in:       "ExecStart=/bin/lmm\r\nRestart=on-failure\r\n",
			want:     "ExecStart=/new/lmm\r\nRestart=on-failure\r\n",
			previous: "/bin/lmm",
		},
		"CRLF with arguments": {
			in:       "ExecStart=/bin/lmm serve --no-open\r\n",
			want:     "ExecStart=/new/lmm serve --no-open\r\n",
			previous: "/bin/lmm",
		},
		"an empty reset line passes through": {
			in:       "ExecStart=\nExecStart=/x serve\n",
			want:     "ExecStart=\nExecStart=/new/lmm serve\n",
			previous: "/x",
		},
		"a reset line with trailing blanks passes through": {
			in:       "ExecStart=  \nExecStart=/x serve\n",
			want:     "ExecStart=  \nExecStart=/new/lmm serve\n",
			previous: "/x",
		},
		"the - prefix stays": {
			in:       "ExecStart=-/bin/lmm serve\n",
			want:     "ExecStart=-/new/lmm serve\n",
			previous: "/bin/lmm",
		},
		"every prefix stays": {
			in:       "ExecStart=@!!+:/bin/lmm argv0 serve\n",
			want:     "ExecStart=@!!+:/new/lmm argv0 serve\n",
			previous: "/bin/lmm",
		},
		"a prefix before a quoted program stays": {
			in:       "ExecStart=-\"/opt/my lmm/lmm\" serve\n",
			want:     "ExecStart=-/new/lmm serve\n",
			previous: "/opt/my lmm/lmm",
		},
		"a prefix inside the quotes stays": {
			in:       "ExecStart=\"-/opt/my lmm/lmm\" serve\n",
			want:     "ExecStart=-/new/lmm serve\n",
			previous: "/opt/my lmm/lmm",
		},
		"an indented line": {
			in:       "  ExecStart=/bin/lmm serve\n",
			want:     "  ExecStart=/new/lmm serve\n",
			previous: "/bin/lmm",
		},
		"spaces around the =": {
			in:       "ExecStart = /bin/lmm serve\n",
			want:     "ExecStart = /new/lmm serve\n",
			previous: "/bin/lmm",
		},
		"a single-quoted program": {
			in:       "ExecStart='/my dir/lmm' serve\n",
			want:     "ExecStart=/new/lmm serve\n",
			previous: "/my dir/lmm",
		},
		"a single-quoted program with no arguments": {
			in:       "ExecStart='/my dir/lmm'\n",
			want:     "ExecStart=/new/lmm\n",
			previous: "/my dir/lmm",
		},
		"a prefix before single quotes stays": {
			in:       "ExecStart=-'/my dir/lmm' serve\n",
			want:     "ExecStart=-/new/lmm serve\n",
			previous: "/my dir/lmm",
		},
		"a prefix inside single quotes stays": {
			in:       "ExecStart='-/my dir/lmm' serve\n",
			want:     "ExecStart=-/new/lmm serve\n",
			previous: "/my dir/lmm",
		},
		"a double quote inside single quotes": {
			in:       "ExecStart='/my \"dir\"/lmm' serve\n",
			want:     "ExecStart=/new/lmm serve\n",
			previous: "/my \"dir\"/lmm",
		},
		"not ExecStartPre": {
			in:       "ExecStartPre=/bin/true\nExecStart=/bin/lmm\n",
			want:     "ExecStartPre=/bin/true\nExecStart=/new/lmm\n",
			previous: "/bin/lmm",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, previous, err := switchExecStart([]byte(tc.in), "/new/lmm")
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
			assert.Equal(t, tc.previous, previous)
		})
	}

	for _, in := range []string{
		"ExecStart=\"/unterminated serve\n",
		"ExecStart='/unterminated serve\n",
		// A quote part-way into a word: systemd joins the pieces, and lmm
		// refuses rather than guess where the program ends.
		"ExecStart=/my' 'dir/lmm serve\n",
		"ExecStart=/my\"dir\"/lmm serve\n",
	} {
		_, _, err := switchExecStart([]byte(in), "/new/lmm")
		require.Error(t, err, "%q: a program lmm cannot read is refused, not mangled", in)
	}
}

// unitFileBinary reads the program of the ExecStart that runs: it agrees
// with switchExecStart on indented lines, CRLF and prefixes, and a reset
// line clears what came before it.
func TestUnitFileBinary(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct{ unit, want string }{
		"plain":                {"[Service]\nExecStart=/bin/lmm serve\n", "/bin/lmm"},
		"no arguments":         {"[Service]\nExecStart=/bin/lmm\n", "/bin/lmm"},
		"CRLF":                 {"[Service]\r\nExecStart=/bin/lmm\r\n", "/bin/lmm"},
		"indented":             {"[Service]\n  ExecStart=/bin/lmm serve\n", "/bin/lmm"},
		"spaces around =":      {"[Service]\nExecStart = /bin/lmm serve\n", "/bin/lmm"},
		"prefix":               {"[Service]\nExecStart=-/bin/lmm serve\n", "/bin/lmm"},
		"reset then a new":     {"[Service]\nExecStart=/old/lmm serve\nExecStart=\nExecStart=/bin/lmm serve\n", "/bin/lmm"},
		"reset last":           {"[Service]\nExecStart=/old/lmm serve\nExecStart=\n", ""},
		"none":                 {"[Service]\nRestart=on-failure\n", ""},
		"ExecStartPre":         {"[Service]\nExecStartPre=/bin/true\nExecStart=/bin/lmm\n", "/bin/lmm"},
		"single quotes":        {"[Service]\nExecStart='/my dir/lmm' serve\n", "/my dir/lmm"},
		"single, prefixed":     {"[Service]\nExecStart=-'/my dir/lmm'\n", "/my dir/lmm"},
		"single, unterminated": {"[Service]\nExecStart='/my dir/lmm serve\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".service")
			require.NoError(t, os.WriteFile(path, []byte(tc.unit), 0o644))
			assert.Equal(t, tc.want, unitFileBinary(path))
		})
	}
}

// execStartFromShow reads `systemctl show -p LoadState -p ExecStart`: the
// loaded unit's first ExecStart, and whether systemd's answer is the one
// that counts - a unit it has loaded, even one a drop-in's reset left with
// no ExecStart at all.
func TestExecStartFromShow(t *testing.T) {
	for name, tc := range map[string]struct {
		out, path string
		loaded    bool
	}{
		"loaded": {
			out:  "LoadState=loaded\nExecStart={ path=/bin/lmm ; argv[]=/bin/lmm serve ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\n",
			path: "/bin/lmm", loaded: true,
		},
		"loaded, reset by a drop-in": {out: "LoadState=loaded\nExecStart=\n", loaded: true},
		"bad setting":                {out: "LoadState=bad-setting\nExecStart=\n", loaded: true},
		"not found":                  {out: "LoadState=not-found\nExecStart=\n"},
		"nothing said":               {out: ""},
	} {
		t.Run(name, func(t *testing.T) {
			path, loaded := execStartFromShow(tc.out)
			assert.Equal(t, tc.path, path)
			assert.Equal(t, tc.loaded, loaded)
		})
	}
}

// limitedBuffer keeps the first n bytes and swallows the rest, so a child
// that prints without end neither fills memory nor blocks on its pipe.
func TestLimitedBuffer(t *testing.T) {
	b := &limitedBuffer{max: 4}
	n, err := b.Write([]byte("abcdef"))
	require.NoError(t, err)
	assert.Equal(t, 6, n, "the whole write is accepted")
	_, _ = b.Write([]byte("gh"))
	assert.Equal(t, "abcd", b.String())
}

// readBinaryVersion kills the process group after the run only when a
// member outlived the binary and held its output open (ErrWaitDelay): once
// the leader has exited cleanly and been reaped, its pid - and so its
// group id - may belong to someone else (#649 review).
func TestReadBinaryVersion_KillsTheGroupOnlyForALeftoverChild(t *testing.T) {
	var kills atomic.Int32
	old := killProcessGroup
	killProcessGroup = func(pid int) error {
		kills.Add(1)
		return old(pid)
	}
	t.Cleanup(func() { killProcessGroup = old })
	dir := t.TempDir()
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
		return p
	}

	v, err := readBinaryVersion(context.Background(), script("clean", `echo "lmm version 2.3.0"`))
	require.NoError(t, err)
	assert.Equal(t, "2.3.0", v)
	assert.Zero(t, kills.Load(), "a clean exit: no group kill")

	v, err = readBinaryVersion(context.Background(), script("forks", `echo "lmm version 2.3.0"; sleep 30 &`))
	require.NoError(t, err)
	assert.Equal(t, "2.3.0", v)
	assert.Equal(t, int32(1), kills.Load(), "the child left holding the output is killed")
}

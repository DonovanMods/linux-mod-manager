package core

import (
	"os"
	"path/filepath"
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

	_, _, err := switchExecStart([]byte("ExecStart=\"/unterminated serve\n"), "/new/lmm")
	require.Error(t, err, "a program lmm cannot read is refused, not mangled")
}

// unitFileBinary reads the program of the ExecStart that runs: it agrees
// with switchExecStart on indented lines, CRLF and prefixes, and a reset
// line clears what came before it.
func TestUnitFileBinary(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct{ unit, want string }{
		"plain":            {"[Service]\nExecStart=/bin/lmm serve\n", "/bin/lmm"},
		"no arguments":     {"[Service]\nExecStart=/bin/lmm\n", "/bin/lmm"},
		"CRLF":             {"[Service]\r\nExecStart=/bin/lmm\r\n", "/bin/lmm"},
		"indented":         {"[Service]\n  ExecStart=/bin/lmm serve\n", "/bin/lmm"},
		"spaces around =":  {"[Service]\nExecStart = /bin/lmm serve\n", "/bin/lmm"},
		"prefix":           {"[Service]\nExecStart=-/bin/lmm serve\n", "/bin/lmm"},
		"reset then a new": {"[Service]\nExecStart=/old/lmm serve\nExecStart=\nExecStart=/bin/lmm serve\n", "/bin/lmm"},
		"reset last":       {"[Service]\nExecStart=/old/lmm serve\nExecStart=\n", ""},
		"none":             {"[Service]\nRestart=on-failure\n", ""},
		"ExecStartPre":     {"[Service]\nExecStartPre=/bin/true\nExecStart=/bin/lmm\n", "/bin/lmm"},
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

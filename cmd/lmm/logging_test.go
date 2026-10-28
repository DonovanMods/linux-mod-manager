package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCLILogger(t *testing.T) {
	var buf bytes.Buffer
	l, err := newCLILogger("off", &buf)
	require.NoError(t, err)
	l.Error("x")
	assert.Empty(t, buf.String())

	l, err = newCLILogger("warn", &buf)
	require.NoError(t, err)
	l.Info("hidden")
	l.Warn("shown", "k", "v")
	assert.NotContains(t, buf.String(), "hidden")
	assert.Contains(t, buf.String(), "level=WARN msg=shown k=v")

	_, err = newCLILogger("loud", &buf)
	assert.EqualError(t, err, `invalid --log-level "loud": expected off, error, warn, info, or debug`)
}

// TestLogLevelFlag covers logLevelFlag's pflag.Value implementation
// directly (Minor #1 of the Task 1 review): String's nil-dest guard, Set
// on every valid level plus one invalid value, and Type.
func TestLogLevelFlag(t *testing.T) {
	t.Run("String_NilDest", func(t *testing.T) {
		var f logLevelFlag
		assert.Empty(t, f.String())
	})

	t.Run("Type", func(t *testing.T) {
		var dest string
		f := logLevelFlag{&dest}
		assert.Equal(t, "string", f.Type())
	})

	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "off", value: "off"},
		{name: "error", value: "error"},
		{name: "warn", value: "warn"},
		{name: "info", value: "info"},
		{name: "debug", value: "debug"},
		{name: "invalid", value: "loud", wantErr: `invalid --log-level "loud": expected off, error, warn, info, or debug`},
	}
	for _, tc := range cases {
		t.Run("Set_"+tc.name, func(t *testing.T) {
			var dest string
			f := logLevelFlag{&dest}
			err := f.Set(tc.value)
			if tc.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.value, dest)
				assert.Equal(t, tc.value, f.String())
				return
			}
			assert.EqualError(t, err, tc.wantErr)
			assert.Empty(t, dest, "an invalid value must not be written to dest")
		})
	}
}

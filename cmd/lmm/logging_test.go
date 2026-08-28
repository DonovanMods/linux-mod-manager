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

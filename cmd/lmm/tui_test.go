package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunTUIRejectsRealModeUntilImplemented(t *testing.T) {
	tuiOptions.prototype = false
	t.Cleanup(func() { tuiOptions.prototype = false })

	err := runTUI(tuiCmd, nil)
	require.ErrorContains(t, err, "use --prototype")
}

func TestRunTUIRejectsUnknownTheme(t *testing.T) {
	tuiOptions.prototype = true
	tuiOptions.theme = "vaporwave"
	t.Cleanup(func() {
		tuiOptions.prototype = false
		tuiOptions.theme = "wizardry"
	})

	err := runTUI(tuiCmd, nil)
	require.ErrorContains(t, err, `unknown TUI theme "vaporwave"`)
}

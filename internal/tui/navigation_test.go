package tui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScreenAtClampsOutOfRangeIndexes(t *testing.T) {
	t.Parallel()

	require.Equal(t, ScreenDashboard, screenAt(-1))
	require.Equal(t, ScreenSources, screenAt(len(screens)))
	require.Equal(t, ScreenInstalledMods, screenAt(1))
}

func TestScreenStringNamesEveryScreen(t *testing.T) {
	t.Parallel()

	for _, s := range screens {
		require.NotContains(t, s.String(), "Screen(", "screen %d needs a display name", int(s))
	}
	require.Equal(t, "Screen(99)", Screen(99).String())
}

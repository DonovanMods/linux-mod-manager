package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommands_RefuseWithoutWhatTheyNeed runs each command line through the
// real tree, in an empty config with no default game, and pins the error a
// user gets when the command lacks what it needs: a game (requireGame's
// "no game specified"), a game that exists ("game not found"), or its
// positional argument (cobra's arity check, or install's own "query or
// --id").
func TestCommands_RefuseWithoutWhatTheyNeed(t *testing.T) {
	const (
		noGame   = "no game specified"
		notFound = "game not found"
		oneArg   = "accepts 1 arg"
	)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"list"}, noGame},
		{[]string{"deploy"}, noGame},
		{[]string{"update"}, noGame},
		{[]string{"update", "rollback", "12345"}, noGame},
		{[]string{"mod", "set-update", "12345", "--auto"}, noGame},
		{[]string{"mod", "enable", "12345"}, noGame},
		{[]string{"mod", "disable", "12345"}, noGame},
		{[]string{"search", "test-query"}, noGame},
		{[]string{"profile", "list"}, noGame},
		{[]string{"profile", "create", "myprofile"}, noGame},
		{[]string{"install", "test mod"}, noGame},
		{[]string{"purge"}, noGame},
		{[]string{"uninstall", "12345"}, noGame},

		{[]string{"install", "--id", "12345", "--game", "non-existent-game"}, notFound},
		{[]string{"purge", "--yes", "--game", "non-existent-game"}, notFound},
		{[]string{"uninstall", "12345", "--game", "non-existent-game"}, notFound},
		{[]string{"game", "set-default", "non-existent-game"}, notFound},

		{[]string{"search", "--game", "test-game"}, "requires at least 1 arg"},
		{[]string{"install", "--game", "test-game"}, "search query or --id is required"},
		{[]string{"uninstall", "--game", "test-game"}, oneArg},
		{[]string{"mod", "enable", "--game", "test-game"}, oneArg},
		{[]string{"mod", "disable", "--game", "test-game"}, oneArg},
		{[]string{"profile", "create", "--game", "test-game"}, oneArg},
		{[]string{"game", "set-default"}, oneArg},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			restoreFlags(t)
			resetChangedFlags(rootCmd)
			configDir, dataDir = t.TempDir(), t.TempDir()
			rootCmd.SetArgs(tc.args)

			var err error
			_, _, _ = captureStdoutAndStderr(t, func() error {
				err = rootCmd.ExecuteContext(context.Background())
				return nil
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

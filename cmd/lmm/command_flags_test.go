package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The command tree's shape - every command's Use, Short and Long, and every
// flag with its default and shorthand - is pinned by the committed man
// pages (TestGenManTree_MatchesCommittedPages), and the behaviour tests
// execute the flags. These tables keep only what is a contract in its own
// right, which a `make man` regeneration must not silently move.

// TestCommandFlags_Contracts pins the flag defaults, shorthands and help
// text a user or script relies on.
func TestCommandFlags_Contracts(t *testing.T) {
	tests := []struct {
		cmd       *cobra.Command
		flag      string
		def       string // DefValue
		shorthand string
		usage     string // a substring the help text must carry
	}{
		{cmd: searchCmd, flag: "limit", def: "10", shorthand: "l"},
		// The aggregate default: no --source searches every source.
		{cmd: searchCmd, flag: "source", def: "", shorthand: "s", usage: "all configured sources"},
		// The destructive paths default to the safe side: nothing is skipped
		// past a confirmation, records are kept, and the cache is cleaned up
		// only on purpose (#600: these were pinned by the deleted DefaultFlags
		// tests, and a `make man` regeneration would carry a flipped default).
		{cmd: installCmd, flag: "yes", def: "false", shorthand: "y"},
		{cmd: installCmd, flag: "force", def: "false", shorthand: "f"},
		{cmd: installCmd, flag: "source", def: "", shorthand: "s"},
		{cmd: installCmd, flag: "profile", def: "", shorthand: "p"},
		{cmd: uninstallCmd, flag: "keep-cache", def: "false"},
		{cmd: uninstallCmd, flag: "force", def: "false", shorthand: "f"},
		{cmd: uninstallCmd, flag: "source", def: "", shorthand: "s"},
		{cmd: uninstallCmd, flag: "profile", def: "", shorthand: "p"},
		{cmd: purgeCmd, flag: "yes", def: "false", shorthand: "y"},
		{cmd: purgeCmd, flag: "uninstall", def: "false"},
		{cmd: purgeCmd, flag: "force", def: "false", shorthand: "f"},
		{cmd: purgeCmd, flag: "profile", def: "", shorthand: "p"},
	}
	for _, tc := range tests {
		t.Run(tc.cmd.Name()+" --"+tc.flag, func(t *testing.T) {
			f := tc.cmd.Flags().Lookup(tc.flag)
			require.NotNil(t, f)
			assert.Equal(t, tc.def, f.DefValue)
			assert.Equal(t, tc.shorthand, f.Shorthand)
			assert.Contains(t, f.Usage, tc.usage)
		})
	}
}

// TestCommandArgs_Arity pins how many positional arguments each command
// with an optional or multi-part argument list accepts: mod convert takes
// a mod and on|off, mod lock a mod and an optional version, mod unlock one
// mod, profile rename two names.
func TestCommandArgs_Arity(t *testing.T) {
	tests := []struct {
		cmd    *cobra.Command
		accept [][]string
		refuse [][]string
	}{
		{modConvertCmd, [][]string{{"a", "on"}, {"a", "off"}}, [][]string{{}, {"a"}, {"a", "on", "extra"}}},
		{modLockCmd, [][]string{{"a"}, {"a", "1.0"}}, [][]string{{}, {"a", "1.0", "extra"}}},
		{modUnlockCmd, [][]string{{"a"}}, [][]string{{}, {"a", "extra"}}},
		{profileRenameCmd, [][]string{{"old", "new"}}, [][]string{{"only-one"}}},
	}
	for _, tc := range tests {
		t.Run(tc.cmd.CommandPath(), func(t *testing.T) {
			for _, args := range tc.accept {
				assert.NoError(t, tc.cmd.Args(tc.cmd, args), "%q", args)
			}
			for _, args := range tc.refuse {
				assert.Error(t, tc.cmd.Args(tc.cmd, args), "%q", args)
			}
		})
	}
}

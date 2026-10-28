package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// directoryFlags names every flag that takes a DIRECTORY (#529), by the
// command that owns it ("" is the root's persistent flags). Each must offer
// the shell's directory-only completion, so Tab never proposes a file for a
// folder (the CLI half of the web UI's folder chooser).
var directoryFlags = []struct {
	cmd  []string
	flag string
}{
	{nil, "config"},
	{nil, "data"},
	{[]string{"game", "add"}, "path"},
	{[]string{"game", "add"}, "mod-path"},
	{[]string{"game", "edit"}, "install-path"},
	{[]string{"game", "edit"}, "mod-path"},
}

func findCommand(t *testing.T, path []string) *cobra.Command {
	t.Helper()
	c, _, err := rootCmd.Find(path)
	require.NoError(t, err)
	require.NotNil(t, c)
	return c
}

func TestDirectoryFlagsOfferDirectoryOnlyCompletion(t *testing.T) {
	for _, want := range directoryFlags {
		c := findCommand(t, want.cmd)
		name := strings.Join(append([]string{"lmm"}, want.cmd...), " ") + " --" + want.flag
		t.Run(name, func(t *testing.T) {
			require.NotNil(t, c.LocalFlags().Lookup(want.flag), "the command owns the flag")
			fn, ok := c.GetFlagCompletionFunc(want.flag)
			require.True(t, ok, "no completion registered: call MarkFlagDirname")
			_, directive := fn(c, nil, "")
			assert.Equal(t, cobra.ShellCompDirectiveFilterDirs, directive, "directories only, no files")
		})
	}
}

// TestEveryPathFlagIsClassified is the ratchet behind that table: a new flag
// whose name says it takes a path or directory must either join
// directoryFlags (and get completion) or be named here as not one.
func TestEveryPathFlagIsClassified(t *testing.T) {
	notDirectories := map[string]bool{
		// (none today: add "command path --flag" with the reason when one appears)
	}
	known := map[string]bool{}
	for _, d := range directoryFlags {
		known[strings.Join(append([]string{"lmm"}, d.cmd...), " ")+" --"+d.flag] = true
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			// By name only: help text mentions directories in passing too
			// (--loader's "installed in the game directory"). --config and
			// --data are in directoryFlags by hand.
			if !strings.Contains(f.Name, "path") && !strings.Contains(f.Name, "dir") && !strings.Contains(f.Name, "folder") {
				return
			}
			key := c.CommandPath() + " --" + f.Name
			assert.Truef(t, known[key] || notDirectories[key],
				"%s looks like it takes a path: add it to directoryFlags (with MarkFlagDirname) or to notDirectories", key)
		}
		c.LocalFlags().VisitAll(check)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceCmd_Structure(t *testing.T) {
	assert.Equal(t, "source", sourceCmd.Use)
	assert.NotEmpty(t, sourceCmd.Short)

	names := make([]string, 0)
	for _, c := range sourceCmd.Commands() {
		names = append(names, c.Name())
	}
	assert.Contains(t, names, "list")
	assert.Contains(t, names, "validate")
}

func TestSourceValidateCmd(t *testing.T) {
	run := func(args ...string) (string, error) {
		cmd := &cobra.Command{Use: "test"}
		cmd.AddCommand(sourceCmd)
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return buf.String(), err
	}

	t.Run("valid file passes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "good.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: my-mods
name: My Mods
type: directory
directory:
  path: ~/mods
`), 0644))

		out, err := run("source", "validate", path)
		assert.NoError(t, err)
		assert.Contains(t, out, "valid")
	})

	t.Run("invalid file fails with reason", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: BAD_ID
name: Bad
type: directory
directory:
  path: ~/x
`), 0644))

		_, err := run("source", "validate", path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "must match")
	})

	t.Run("missing argument errors", func(t *testing.T) {
		_, err := run("source", "validate")
		assert.Error(t, err)
	})
}

package main

import (
	"bytes"
	"encoding/json"
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

// TestSourceListCmd_ErrorRows is a regression test for final-review finding 2:
// `lmm source list` must not silently drop a definition whose source failed to
// construct, and must not relabel a built-in source's type just because a
// custom definition collides with its ID.
func TestSourceListCmd_ErrorRows(t *testing.T) {
	runList := func(t *testing.T) []sourceInfo {
		t.Helper()
		cmd := &cobra.Command{Use: "test"}
		cmd.AddCommand(sourceCmd)
		buf := new(bytes.Buffer)
		cmd.SetOut(buf)
		cmd.SetErr(buf)
		cmd.SetArgs([]string{"source", "list"})

		jsonOutput = true
		t.Cleanup(func() { jsonOutput = false })

		require.NoError(t, cmd.Execute())

		var rows []sourceInfo
		require.NoError(t, json.Unmarshal(buf.Bytes(), &rows))
		return rows
	}

	findRow := func(rows []sourceInfo, id, typ string) (sourceInfo, bool) {
		for _, r := range rows {
			if r.ID == id && r.Type == typ {
				return r, true
			}
		}
		return sourceInfo{}, false
	}

	t.Run("definition with missing path produces an error row", func(t *testing.T) {
		configDir = t.TempDir()
		dataDir = t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(configDir, "sources"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "sources", "broken.yaml"), []byte(`
id: broken-mods
name: Broken Mods
type: directory
directory:
  path: `+filepath.Join(t.TempDir(), "does-not-exist")+`
`), 0644))

		rows := runList(t)

		row, found := findRow(rows, "broken-mods", "error")
		require.True(t, found, "a definition whose source fails to construct must still produce a row: %+v", rows)
		assert.NotEmpty(t, row.Error)
	})

	t.Run("definition colliding with a built-in id keeps the built-in row and adds an error row", func(t *testing.T) {
		configDir = t.TempDir()
		dataDir = t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(configDir, "sources"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "sources", "collide.yaml"), []byte(`
id: nexusmods
name: Fake Nexus
type: directory
directory:
  path: `+t.TempDir()+`
`), 0644))

		rows := runList(t)

		_, builtinFound := findRow(rows, "nexusmods", "built-in")
		assert.True(t, builtinFound, "a colliding custom definition must not relabel the built-in source's type: %+v", rows)

		errRow, errFound := findRow(rows, "nexusmods", "error")
		require.True(t, errFound, "an id collision must still produce an error row: %+v", rows)
		assert.Contains(t, errRow.Error, "already in use")
	})
}

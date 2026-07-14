package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeSourceFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
}

func TestLoadSourceDefinitions(t *testing.T) {
	t.Run("missing sources dir is not an error", func(t *testing.T) {
		defs, loadErrs, err := LoadSourceDefinitions(t.TempDir())
		assert.NoError(t, err)
		assert.Empty(t, defs)
		assert.Empty(t, loadErrs)
	})

	t.Run("loads valid definitions and collects per-file errors", func(t *testing.T) {
		configDir := t.TempDir()
		srcDir := filepath.Join(configDir, "sources")
		writeSourceFile(t, srcDir, "good.yaml", `
id: my-mods
name: My Mods
type: directory
directory:
  path: ~/mods
`)
		writeSourceFile(t, srcDir, "bad-yaml.yaml", "id: [unclosed")
		writeSourceFile(t, srcDir, "invalid.yaml", `
id: BAD_ID
name: Bad
type: directory
directory:
  path: ~/x
`)
		writeSourceFile(t, srcDir, "notes.txt", "not yaml, ignored")

		defs, loadErrs, err := LoadSourceDefinitions(configDir)
		assert.NoError(t, err)
		require.Len(t, defs, 1)
		assert.Equal(t, "my-mods", defs[0].ID)
		require.Len(t, loadErrs, 2)
		files := []string{loadErrs[0].File, loadErrs[1].File}
		assert.Contains(t, files, "bad-yaml.yaml")
		assert.Contains(t, files, "invalid.yaml")
	})

	t.Run("duplicate ids across files are rejected", func(t *testing.T) {
		configDir := t.TempDir()
		srcDir := filepath.Join(configDir, "sources")
		def := `
id: dupe
name: Dupe
type: directory
directory:
  path: ~/mods
`
		writeSourceFile(t, srcDir, "a.yaml", def)
		writeSourceFile(t, srcDir, "b.yaml", def)

		defs, loadErrs, err := LoadSourceDefinitions(configDir)
		assert.NoError(t, err)
		assert.Len(t, defs, 1)
		require.Len(t, loadErrs, 1)
		assert.ErrorContains(t, loadErrs[0].Err, "duplicate")
	})
}

func TestLoadSourceDefinitionFile(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, dir, "s.yaml", `
id: my-mods
name: My Mods
type: directory
directory:
  path: ~/mods
`)

	def, err := LoadSourceDefinitionFile(filepath.Join(dir, "s.yaml"))
	assert.NoError(t, err)
	assert.Equal(t, "my-mods", def.ID)

	_, err = LoadSourceDefinitionFile(filepath.Join(dir, "missing.yaml"))
	assert.Error(t, err)
}

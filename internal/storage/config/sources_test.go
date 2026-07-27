package config

import (
	"errors"
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

	t.Run("unreadable sources dir is a hard error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permission checks, so this EACCES setup can't fail")
		}

		configDir := t.TempDir()
		srcDir := filepath.Join(configDir, "sources")
		require.NoError(t, os.MkdirAll(srcDir, 0755))
		require.NoError(t, os.Chmod(srcDir, 0000))
		t.Cleanup(func() { _ = os.Chmod(srcDir, 0755) }) // restore before TempDir's own cleanup removes it

		_, _, err := LoadSourceDefinitions(configDir)
		require.Error(t, err, "an unreadable sources directory must be a hard error, not silently skipped")
		assert.Contains(t, err.Error(), "sources")
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

// customLoadErr is a distinct error type used to prove SourceLoadError.Unwrap
// lets errors.As reach the wrapped cause, not just SourceLoadError itself.
type customLoadErr struct{ detail string }

func (e *customLoadErr) Error() string { return "custom: " + e.detail }

// TestSourceLoadError_UnwrapRoundTrip pins that SourceLoadError.Unwrap exposes
// Err so errors.As can match a wrapped cause through it (issue #52 item 9).
func TestSourceLoadError_UnwrapRoundTrip(t *testing.T) {
	cause := &customLoadErr{detail: "boom"}
	sle := SourceLoadError{File: "bad.yaml", Err: cause}

	var target *customLoadErr
	require.True(t, errors.As(sle, &target), "errors.As must unwrap SourceLoadError to reach the wrapped cause")
	assert.Same(t, cause, target)
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

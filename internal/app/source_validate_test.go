package app

// Tests for ValidateSourceFile (#309: `lmm source validate <file> --json`).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateSourceFile_Valid covers the success shape: Valid true, the
// definition's own ID/Type surfaced, no errors, and the def itself
// returned for a caller that wants to --probe it without re-parsing.
func TestValidateSourceFile_Valid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "good.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
id: my-mods
name: My Mods
type: directory
directory:
  path: `+t.TempDir()+`
`), 0644))

	report, def, err := ValidateSourceFile(path)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.True(t, report.Valid)
	assert.Equal(t, path, report.Path)
	assert.Equal(t, "my-mods", report.ID)
	assert.Equal(t, "directory", report.Type)
	assert.Empty(t, report.Errors)
	assert.Nil(t, report.Probe)
	assert.Equal(t, "my-mods", def.ID, "the parsed definition is returned alongside the report")
}

// TestValidateSourceFile_Invalid covers the failure shape: Valid false, the
// single load/validate error surfaced on Errors AND returned directly (so
// a caller preserves errors.Is/As through it), no ID/Type (nothing to read
// them from), and a zero-value definition.
func TestValidateSourceFile_Invalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
id: BAD_ID
name: Bad
type: directory
directory:
  path: ~/x
`), 0644))

	report, def, err := ValidateSourceFile(path)
	require.Error(t, err)
	require.NotNil(t, report)
	assert.False(t, report.Valid)
	assert.Equal(t, path, report.Path)
	assert.Empty(t, report.ID)
	assert.Empty(t, report.Type)
	require.Len(t, report.Errors, 1)
	assert.Equal(t, err.Error(), report.Errors[0])
	assert.Contains(t, report.Errors[0], "must match")
	assert.Equal(t, "", def.ID, "the zero-value definition for an invalid file")
}

// TestValidateSourceFile_MissingFile covers the read-failure path (file
// does not exist at all), the other LoadSourceDefinitionFile error shape.
func TestValidateSourceFile_MissingFile(t *testing.T) {
	report, _, err := ValidateSourceFile(filepath.Join(t.TempDir(), "nope.yaml"))
	require.Error(t, err)
	assert.False(t, report.Valid)
	require.Len(t, report.Errors, 1)
	assert.Equal(t, err.Error(), report.Errors[0])
}

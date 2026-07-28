package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
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

// runSourceCmd executes the source command tree with args against a fresh
// cobra root, capturing combined stdout/stderr. Shared by TestSourceValidateCmd
// and TestSourceValidateProbe so both drive the real command wiring rather
// than duplicating the harness.
func runSourceCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(sourceCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(sourceCmd); rootCmd.AddCommand(sourceCmd) })
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// resetSourceProbeFlags restores the package-level --probe/--id flag vars to
// their zero values. cobra flag vars persist across Execute() calls within a
// test binary since Parse only touches flags actually present in argv, so
// tests that don't pass --probe would otherwise see a value left behind by an
// earlier subtest.
func resetSourceProbeFlags(t *testing.T) {
	t.Helper()
	sourceProbe = false
	sourceProbeID = ""
}

func TestSourceValidateCmd(t *testing.T) {
	t.Run("valid file passes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "good.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: my-mods
name: My Mods
type: directory
directory:
  path: ~/mods
`), 0644))

		out, err := runSourceCmd(t, "source", "validate", path)
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

		_, err := runSourceCmd(t, "source", "validate", path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "must match")
	})

	t.Run("missing argument errors", func(t *testing.T) {
		_, err := runSourceCmd(t, "source", "validate")
		assert.Error(t, err)
	})
}

func TestSourceValidateProbe(t *testing.T) {
	t.Run("probe directory source reports mod count", func(t *testing.T) {
		resetSourceProbeFlags(t)
		t.Cleanup(func() { resetSourceProbeFlags(t) })

		configDir = t.TempDir()
		dataDir = t.TempDir()

		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "SomeMod"), 0755))
		path := filepath.Join(t.TempDir(), "dir.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: probe-dir
name: Probe Dir
type: directory
directory:
  path: `+root+`
`), 0644))

		out, err := runSourceCmd(t, "source", "validate", path, "--probe")
		assert.NoError(t, err)
		assert.Contains(t, out, "probe: ok")
		assert.Contains(t, out, "1 mod(s)")
	})

	t.Run("probe api without search requires --id", func(t *testing.T) {
		resetSourceProbeFlags(t)
		t.Cleanup(func() { resetSourceProbeFlags(t) })

		// probeSource resolves the API key via the service (a local DB read,
		// no network) before reaching the --id check, so withService still
		// needs a real, isolated config/data dir here rather than the
		// caller's actual home directory.
		configDir = t.TempDir()
		dataDir = t.TempDir()

		path := filepath.Join(t.TempDir(), "api.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: probe-api
name: Probe API
type: api
api:
  base_url: https://api.x.test
  endpoints:
    get_mod:
      path: /mods/{mod_id}
  mappings:
    mod:
      id: id
      name: name
`), 0644))

		_, err := runSourceCmd(t, "source", "validate", path, "--probe")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--id")
	})

	t.Run("probe failure exits non-zero", func(t *testing.T) {
		resetSourceProbeFlags(t)
		t.Cleanup(func() { resetSourceProbeFlags(t) })

		configDir = t.TempDir()
		dataDir = t.TempDir()

		path := filepath.Join(t.TempDir(), "bad.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
id: probe-bad
name: Probe Bad
type: manifest
manifest:
  url: `+filepath.Join(t.TempDir(), "missing.yaml")+`
`), 0644))

		_, err := runSourceCmd(t, "source", "validate", path, "--probe")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "probe-bad")
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
		t.Cleanup(func() { rootCmd.RemoveCommand(sourceCmd); rootCmd.AddCommand(sourceCmd) })
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

// TestSourceInfoRows_EmptyJSONEncode pins the JSON contract for `source list
// --json`: a zero-length row slice must encode as `[]`, never `null` (#52
// item 13). This is unreachable through the CLI today — registerSources
// always registers the built-in sources before sourceListCmd builds its row
// slice, so rows is never actually empty at runtime — so this exercises the
// encoding contract directly, guarding against a future regression back to
// `var rows []sourceInfo` (whose zero value is nil and encodes as `null`).
func TestSourceInfoRows_EmptyJSONEncode(t *testing.T) {
	rows := make([]sourceInfo, 0)
	b, err := json.Marshal(rows)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(b))

	// Sanity check on the failure mode being guarded against: a nil slice of
	// the same type really does encode as `null`.
	var nilRows []sourceInfo
	b, err = json.Marshal(nilRows)
	require.NoError(t, err)
	assert.Equal(t, "null", string(b))
}

// TestSourceListCmd_ReportsBrokenDefinitionOnce is the regression test for
// #52 item 14: registerCustomSources used to warn a broken definition
// straight to os.Stderr during service init, and `source list` then rendered
// the very same error again as a table row — the same broken definition
// reported twice per invocation. registerCustomSources writes to os.Stderr
// directly (bypassing cmd.SetErr/cmd.SetOut), so this redirects the real
// os.Stderr for the duration of the command run to observe what actually
// reaches it, alongside the command's own captured stdout.
func TestSourceListCmd_ReportsBrokenDefinitionOnce(t *testing.T) {
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

	r, w, err := os.Pipe()
	require.NoError(t, err)
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	stdout, cmdErr := runSourceCmd(t, "source", "list")

	os.Stderr = origStderr
	require.NoError(t, w.Close())
	var stderrBuf bytes.Buffer
	_, readErr := stderrBuf.ReadFrom(r)
	require.NoError(t, readErr)

	require.NoError(t, cmdErr)

	combined := stdout + stderrBuf.String()
	assert.Equal(t, 1, strings.Count(combined, "broken-mods"),
		"broken-mods should be reported exactly once across stdout+stderr, got: %q", combined)
}

// TestSourceTypeLabel_AllRealTypesPlusBareMock is Task 4's RED-first pin for
// `source list`'s TYPE column: one instance of every real source type
// (custom.Directory/Manifest/API and both built-ins) must report its own
// TypeLabel() rather than being classified by a hand-maintained concrete-type
// switch (the old isCustomSource), plus a bare source.ModSource double that
// implements no source.TypeLabeler — unreachable by any real source (all five
// implement it as of Task 1), but exercised here to pin the "unknown"
// fallback explicitly, since the OLD negative-switch would have mislabeled
// it "built-in" (anything not a recognized custom concrete type fell through
// to that default).
func TestSourceTypeLabel_AllRealTypesPlusBareMock(t *testing.T) {
	dir, err := custom.NewDirectory(custom.SourceDefinition{
		ID:        "d",
		Name:      "D",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)

	man, err := custom.NewManifest(custom.SourceDefinition{
		ID:       "m",
		Name:     "M",
		Type:     custom.TypeManifest,
		Manifest: &custom.ManifestConfig{URL: filepath.Join(t.TempDir(), "manifest.yaml")},
	})
	require.NoError(t, err)

	api, err := custom.NewAPI(custom.SourceDefinition{
		ID:   "a",
		Name: "A",
		Type: custom.TypeAPI,
		API:  &custom.APIConfig{BaseURL: "https://api.x.test"},
	})
	require.NoError(t, err)

	bare := &mockAuthSource{id: "bare", name: "Bare"}

	tests := []struct {
		name string
		src  source.ModSource
		want string
	}{
		{"directory", dir, "directory"},
		{"manifest", man, "manifest"},
		{"api", api, "api"},
		{"nexusmods built-in", nexusmods.New(nil, ""), "built-in"},
		{"curseforge built-in", curseforge.New(nil, ""), "built-in"},
		{"bare mock, no TypeLabeler", bare, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sourceTypeLabel(tt.src))
		})
	}
}

// TestSourceListCmd_TypeColumnFromTypeLabeler is the end-to-end companion to
// TestSourceTypeLabel_AllRealTypesPlusBareMock: `source list` against a real
// config dir with one definition of each custom type must show the exact
// same TypeLabel()-derived values in its TYPE column, alongside both
// built-ins (always registered). A bare mock can't be exercised here — the
// real registration pipeline only ever constructs sources via
// registerSources' built-in factories or custom.New, both of which always
// return a TypeLabeler — so that fallback stays covered at the unit level
// above only.
func TestSourceListCmd_TypeColumnFromTypeLabeler(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "sources"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sources", "dir.yaml"), []byte(`
id: my-directory
name: My Directory
type: directory
directory:
  path: `+t.TempDir()+`
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sources", "manifest.yaml"), []byte(`
id: my-manifest
name: My Manifest
type: manifest
manifest:
  url: `+filepath.Join(t.TempDir(), "manifest.yaml")+`
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sources", "api.yaml"), []byte(`
id: my-api
name: My API
type: api
api:
  base_url: https://api.x.test
  endpoints:
    get_mod:
      path: /mods/{mod_id}
  mappings:
    mod:
      id: id
      name: name
`), 0644))

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(sourceCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(sourceCmd); rootCmd.AddCommand(sourceCmd) })
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"source", "list"})

	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	require.NoError(t, cmd.Execute())

	var rows []sourceInfo
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rows))

	byID := make(map[string]string, len(rows))
	for _, r := range rows {
		byID[r.ID] = r.Type
	}
	assert.Equal(t, "directory", byID["my-directory"])
	assert.Equal(t, "manifest", byID["my-manifest"])
	assert.Equal(t, "api", byID["my-api"])
	assert.Equal(t, "built-in", byID["nexusmods"])
	assert.Equal(t, "built-in", byID["curseforge"])
}

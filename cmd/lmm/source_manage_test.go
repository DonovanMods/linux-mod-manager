package main

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReportError_JSON_SourceInUseError pins core.SourceInUseError's
// --json envelope shape - the entry detailsCoverage names for it
// (details_coverage_test.go).
func TestReportError_JSON_SourceInUseError(t *testing.T) {
	withJSONOutput(t)

	err := &core.SourceInUseError{SourceID: "my-mods", Games: []string{"alpha", "zeta"}}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"source \\\"my-mods\\\" is configured for 2 game(s): [alpha zeta]\",\n"+
		"  \"details\": {\n"+
		"    \"source_id\": \"my-mods\",\n"+
		"    \"games\": [\n"+
		"      \"alpha\",\n"+
		"      \"zeta\"\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
}

// setupSourceManageTest sandboxes HOME and every XDG variable, points the
// global --config/--data flags at temp directories, and blanks the two
// built-in key variables so nothing here can pick up a real credential.
func setupSourceManageTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("NEXUSMODS_API_KEY", "")
	t.Setenv("CURSEFORGE_API_KEY", "")

	cfg, data := t.TempDir(), t.TempDir()
	oldCfg, oldData, oldJSON := configDir, dataDir, jsonOutput
	t.Cleanup(func() { configDir, dataDir, jsonOutput = oldCfg, oldData, oldJSON })
	configDir, dataDir, jsonOutput = cfg, data, false
	return cfg
}

// writeDefinitionFile writes a valid directory definition to a scratch file
// and returns its path.
func writeDefinitionFile(t *testing.T, id, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), id+".yaml")
	body := fmt.Sprintf("id: %s\nname: %s\ntype: directory\ndirectory:\n  path: %s\n", id, name, t.TempDir())
	require.NoError(t, os.WriteFile(path, []byte(body), 0644))
	return path
}

func TestSourceAdd_InstallsTheDefinition(t *testing.T) {
	cfg := setupSourceManageTest(t)

	out, err := runSourceCmd(t, "source", "add", writeDefinitionFile(t, "my-mods", "My Mods"))
	require.NoError(t, err)
	assert.Equal(t, "added: directory source \"my-mods\"\n", out)
	assert.FileExists(t, filepath.Join(cfg, "sources", "my-mods.yaml"))
}

func TestSourceAdd_JSONEmitsTheSourceList(t *testing.T) {
	setupSourceManageTest(t)
	withJSONOutput(t)

	path := writeDefinitionFile(t, "my-mods", "My Mods")
	out := captureStdout(t, func() error {
		_, err := runSourceCmd(t, "source", "add", path)
		return err
	})

	var infos []app.SourceInfo
	require.NoError(t, json.Unmarshal([]byte(out), &infos))
	var ids []string
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	assert.Contains(t, ids, "my-mods", "the re-read registry names the source just added")
}

func TestSourceAdd_RefusesABuiltinID(t *testing.T) {
	cfg := setupSourceManageTest(t)

	_, err := runSourceCmd(t, "source", "add", writeDefinitionFile(t, "nexusmods", "Impostor"))
	require.ErrorIs(t, err, app.ErrBuiltinSourceID)
	assert.NoDirExists(t, filepath.Join(cfg, "sources"))
}

func TestSourceAdd_RefusesAnInvalidDefinitionWithTheValidationEnvelope(t *testing.T) {
	setupSourceManageTest(t)

	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("id: My-Mods\nname: x\ntype: directory\n"), 0644))

	_, err := runSourceCmd(t, "source", "add", path)
	require.Error(t, err)
	var validationErr *sourceValidationError
	require.ErrorAs(t, err, &validationErr, "the same envelope `source validate` fails with")
	assert.False(t, validationErr.report.Valid)
}

func TestSourceRemove_DeletesTheDefinition(t *testing.T) {
	cfg := setupSourceManageTest(t)
	_, err := runSourceCmd(t, "source", "add", writeDefinitionFile(t, "my-mods", "My Mods"))
	require.NoError(t, err)

	out, err := runSourceCmd(t, "source", "remove", "my-mods")
	require.NoError(t, err)
	assert.Equal(t, "removed: source \"my-mods\"\n", out)
	assert.NoFileExists(t, filepath.Join(cfg, "sources", "my-mods.yaml"))
}

func TestSourceRemove_RefusesASourceAGameStillMaps(t *testing.T) {
	cfg := setupSourceManageTest(t)
	_, err := runSourceCmd(t, "source", "add", writeDefinitionFile(t, "my-mods", "My Mods"))
	require.NoError(t, err)
	require.NoError(t, config.SaveGame(cfg, &domain.Game{
		ID: "g1", Name: "G1", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"my-mods": ""},
	}))

	_, err = runSourceCmd(t, "source", "remove", "my-mods")
	var inUse *core.SourceInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, []string{"g1"}, inUse.Games)
	assert.FileExists(t, filepath.Join(cfg, "sources", "my-mods.yaml"))
}

func TestSourceRemove_RefusesAnUnknownID(t *testing.T) {
	setupSourceManageTest(t)

	_, err := runSourceCmd(t, "source", "remove", "ghost")
	require.ErrorIs(t, err, app.ErrSourceDefinitionNotFound)
}

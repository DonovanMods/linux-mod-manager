package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileCmd_Structure(t *testing.T) {
	assert.Equal(t, "profile", profileCmd.Use)
	assert.NotEmpty(t, profileCmd.Short)

	// Check subcommands exist
	var subCmds []string
	for _, cmd := range profileCmd.Commands() {
		subCmds = append(subCmds, cmd.Name())
	}

	assert.Contains(t, subCmds, "list")
	assert.Contains(t, subCmds, "create")
	assert.Contains(t, subCmds, "delete")
	assert.Contains(t, subCmds, "switch")
	assert.Contains(t, subCmds, "export")
	assert.Contains(t, subCmds, "import")
}

func TestProfileListCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(profileCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"profile", "list"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestProfileCreateCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(profileCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"profile", "create", "myprofile"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestProfileCreateCmd_NoName(t *testing.T) {
	gameID = "test-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(profileCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"profile", "create"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestSelectPrimaryFile(t *testing.T) {
	tests := []struct {
		name     string
		files    []domain.DownloadableFile
		expected string
	}{
		{
			name: "returns primary file when available",
			files: []domain.DownloadableFile{
				{ID: "1", FileName: "optional.zip", IsPrimary: false},
				{ID: "2", FileName: "main.zip", IsPrimary: true},
				{ID: "3", FileName: "update.zip", IsPrimary: false},
			},
			expected: "2",
		},
		{
			name: "returns first file when no primary",
			files: []domain.DownloadableFile{
				{ID: "1", FileName: "first.zip", IsPrimary: false},
				{ID: "2", FileName: "second.zip", IsPrimary: false},
			},
			expected: "1",
		},
		{
			name: "returns first primary when multiple primaries",
			files: []domain.DownloadableFile{
				{ID: "1", FileName: "first.zip", IsPrimary: false},
				{ID: "2", FileName: "primary1.zip", IsPrimary: true},
				{ID: "3", FileName: "primary2.zip", IsPrimary: true},
			},
			expected: "2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := selectPrimaryFile(tt.files)
			assert.NotNil(t, result)
			assert.Equal(t, tt.expected, result.ID)
		})
	}
}

func TestSelectPrimaryFile_EmptySlice(t *testing.T) {
	var files []domain.DownloadableFile
	result := selectPrimaryFile(files)
	assert.Nil(t, result)
}

// --- doProfileSwitch (Task 4 CLI refit) ---

// withStdin temporarily replaces os.Stdin with a pipe pre-loaded with input,
// for exercising doProfileSwitch's "Proceed? [Y/n]" prompt. readPromptLine
// reads directly from os.Stdin with no injectable seam (unlike
// promptMultiSelectionFrom elsewhere in this package), so the swap happens
// here instead.
func withStdin(t *testing.T, input string, fn func()) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { os.Stdin = old }()
	os.Stdin = r

	_, err = w.WriteString(input)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	fn()
}

// setupDoProfileSwitchTest builds a *core.Service plus a game with a
// "default" profile already created and set as the active default,
// mirroring setupDoDeployTest's pattern. Callers seed their own additional
// profiles/mods.
func setupDoProfileSwitchTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	oldVerbose := verbose
	verbose = false
	t.Cleanup(func() { verbose = oldVerbose })

	pm := getProfileManager(svc)
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	return svc, game
}

func TestDoProfileSwitch_AlreadyActive_PrintsMessageAndReturnsNil(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)

	out := captureStdout(t, func() error {
		return doProfileSwitch(context.Background(), svc, game, "default")
	})

	assert.Equal(t, "Already on profile: default\n", out)
}

// TestDoProfileSwitch_NoChanges_SwitchesDefaultWithoutPrompting guards
// doProfileSwitch's fast path: when the target profile's mod set already
// matches, the CLI switches the default without ever prompting (no stdin
// interaction needed) and prints the plan header immediately followed by
// the short "✓ Switched" message - no leading blank line, unlike the
// mutation path's final message (see the happy-path test below).
func TestDoProfileSwitch_NoChanges_SwitchesDefaultWithoutPrompting(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "other")
	require.NoError(t, err)

	seedDeployableMod(t, svc, game, "shared", "Shared Mod", "shared.esp")
	require.NoError(t, pm.AddMod(game.ID, "other", domain.ModReference{SourceID: "src", ModID: "shared", Version: "1.0"}))

	out := captureStdout(t, func() error {
		return doProfileSwitch(context.Background(), svc, game, "other")
	})

	assert.Equal(t, "Switching to profile: other\n\n✓ Switched to profile: other\n", out)

	def, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "other", def.Name)
}

// TestDoProfileSwitch_PrintsPlanAndPrompts_ProceedDeclined_NoMutations
// guards the plan printout (byte-identical to the pre-extraction "Will
// disable/enable/install" blocks, printed purely from the SwitchPlan
// struct) and that declining the prompt performs zero mutations - not even
// SetDefault.
func TestDoProfileSwitch_PrintsPlanAndPrompts_ProceedDeclined_NoMutations(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)

	seedDeployableMod(t, svc, game, "disable-me", "Disable Me", "disable.esp")

	var out string
	withStdin(t, "n\n", func() {
		out = captureStdout(t, func() error {
			return doProfileSwitch(context.Background(), svc, game, "target")
		})
	})

	assert.Contains(t, out, "Switching to profile: target\n\n")
	assert.Contains(t, out, "Will disable 1 mod(s):\n")
	assert.Contains(t, out, "  - Disable Me (disable-me)\n")
	assert.Contains(t, out, "\nProceed? [Y/n]: ")
	assert.Contains(t, out, "Cancelled.\n")

	def, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "default", def.Name, "declining must not switch the default profile")

	mod, err := svc.GetInstalledMod("src", "disable-me", "g1", "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled, "declining must not disable any mod")
}

// TestDoProfileSwitch_ProceedAccepted_HappyPath_PrintsExpectedOutput guards
// doProfileSwitch's full apply path end to end (disable, enable, install,
// SetDefault) byte-identically to the pre-extraction CLI, across all three
// plan buckets in one switch. The install bucket uses a real custom
// manifest source served over httptest, mirroring
// TestDoInstall_VisibleUnderLMMGameIDWhenSourceMappingDiffers's pattern
// (mockSourceWithDownloads/createTestZip are internal/core-only test
// helpers, not available to cmd/lmm).
func TestDoProfileSwitch_ProceedAccepted_HappyPath_PrintsExpectedOutput(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	game.DeployMode = domain.DeployCopy
	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)

	// disable-me: enabled under "default", absent from "target".
	seedDeployableMod(t, svc, game, "disable-me", "Disable Me", "disable.esp")

	// enable-me: installed (cached) but disabled, under "target" already.
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "enable-me", "1.0", "enable.esp", []byte("e")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "enable-me", SourceID: "src", Name: "Enable Me", Version: "1.0", GameID: game.ID},
		ProfileName:  "target",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      false,
	}))
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "src", ModID: "enable-me", Version: "1.0"}))

	// install-me: referenced by "target" only, not installed at all.
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	manifest := fmt.Sprintf(`
version: 1
mods:
  - id: install-me
    name: Install Me
    version: "1.0"
    summary: A mod to install
    files:
      - id: main
        filename: install-me.dat
        version: "1.0"
        url: %s/files/install-me.dat
        primary: true
`, srv.URL)
	mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(manifest)) })
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("archive bytes")) })
	src, err := custom.New(custom.SourceDefinition{
		ID:        "e2e-repo",
		Name:      "E2E Repo",
		Type:      custom.TypeManifest,
		AllowHTTP: true,
		Manifest:  &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)
	svc.RegisterSource(src)
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "e2e-repo", ModID: "install-me", Version: "1.0"}))

	var out string
	withStdin(t, "y\n", func() {
		out = captureStdout(t, func() error {
			return doProfileSwitch(context.Background(), svc, game, "target")
		})
	})

	assert.Contains(t, out, "Switching to profile: target\n\n")
	assert.Contains(t, out, "Will disable 1 mod(s):\n  - Disable Me (disable-me)\n")
	assert.Contains(t, out, "Will enable 1 mod(s):\n  + Enable Me (enable-me)\n")
	assert.Contains(t, out, "Will install 1 mod(s):\n  ↓ e2e-repo:install-me v1.0\n")
	assert.Contains(t, out, "\nProceed? [Y/n]: ")
	assert.Contains(t, out, "  ✓ Disabled: Disable Me\n")
	assert.Contains(t, out, "  ✓ Enabled: Enable Me\n")
	assert.Contains(t, out, "\nInstalling missing mods...\n")
	assert.Contains(t, out, "  Installing e2e-repo:install-me...\n")
	assert.Contains(t, out, "    ✓ Installed: Install Me\n")
	assert.Contains(t, out, "\n✓ Switched to profile: target\n")

	def, err := pm.GetDefault(game.ID)
	require.NoError(t, err)
	assert.Equal(t, "target", def.Name)

	_, err = os.Lstat(filepath.Join(game.ModPath, "enable.esp"))
	assert.NoError(t, err, "enable.esp should be deployed")
	_, err = os.Lstat(filepath.Join(game.ModPath, "install-me.dat"))
	assert.NoError(t, err, "install-me.dat should be deployed")
	_, err = os.Lstat(filepath.Join(game.ModPath, "disable.esp"))
	assert.True(t, os.IsNotExist(err), "disable.esp should be undeployed")

	installed, err := svc.GetInstalledMod("e2e-repo", "install-me", "g1", "target")
	require.NoError(t, err)
	assert.Equal(t, "g1", installed.GameID, "persisted GameID must be normalized to the lmm game")
}

// TestDoProfileSwitch_VerboseNotePath_UndeployFailurePrintsUnderVerbose
// guards the CLI's wiring of SwitchDisableNote events to stdout, gated by
// --verbose - doProfileSwitch never writes to stderr, unlike deploy/
// uninstall (see the task report).
func TestDoProfileSwitch_VerboseNotePath_UndeployFailurePrintsUnderVerbose(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)

	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")
	// seedDeployableMod only seeds the cache/DB/profile - actually deploy the
	// mod first so there is a real symlink to corrupt.
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))
	// Corrupt the deployed symlink so Uninstall fails deterministically.
	deployedPath := filepath.Join(game.ModPath, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	oldVerbose := verbose
	verbose = true
	t.Cleanup(func() { verbose = oldVerbose })

	var out string
	withStdin(t, "y\n", func() {
		out = captureStdout(t, func() error {
			return doProfileSwitch(context.Background(), svc, game, "target")
		})
	})

	assert.Contains(t, out, "  Warning: failed to undeploy Test Mod: ")
	assert.Contains(t, out, "  ✓ Disabled: Test Mod\n")
}

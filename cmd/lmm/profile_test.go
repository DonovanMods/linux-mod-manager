package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

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
	t.Cleanup(func() { rootCmd.RemoveCommand(profileCmd); rootCmd.AddCommand(profileCmd) })

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
	t.Cleanup(func() { rootCmd.RemoveCommand(profileCmd); rootCmd.AddCommand(profileCmd) })

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
	t.Cleanup(func() { rootCmd.RemoveCommand(profileCmd); rootCmd.AddCommand(profileCmd) })

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

// seedApplyCandidateMod stores files (if any) in the game's cache and saves
// an InstalledMod DB record under the "default" profile with the given
// Enabled state, WITHOUT adding it to any profile's Mods list - unlike
// seedDeployableMod/seedInstalledForUpdate, which always add a profile
// reference. doProfileApply tests need mods that are installed but
// deliberately absent from profile.Mods (the toDisable case), so callers add
// their own profile.Mods references only where the scenario calls for it.
func seedApplyCandidateMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version string, enabled bool, files map[string][]byte) {
	t.Helper()

	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, path, content))
	}

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: name, Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

// TestDoProfileApply_PrintsDeterministicOrder_MatchesProfileMods guards Task
// 1's declared behavior change for doProfileApply: its disable/enable/install
// buckets used to be built by ranging Go maps (installedByKey/profileKeys),
// so the order mods were announced in was arbitrary from run to run. This
// seeds three disable-eligible, three enable-eligible, and three
// install-eligible mods with a profile.Mods order that deliberately doesn't
// match alphabetical or DB-insertion order, and asserts the printed order:
// disable-eligible mods are absent from profile.Mods entirely, so
// core.OrderByProfile sorts them first by "SourceID:ID" key (proven here by
// seeding their DB rows in reverse-alphabetical order and expecting
// alphabetical print order); enable/install-eligible mods are interleaved in
// a single profile.Mods list, so each bucket must independently preserve its
// relative position in that list.
func TestDoProfileApply_PrintsDeterministicOrder_MatchesProfileMods(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	// Disable-eligible: installed+enabled, absent from profile.Mods. Seeded
	// in reverse order (disC, disB, disA) - core.OrderByProfile sorts
	// unlisted mods by key, so the print order must come out alphabetical.
	for _, id := range []string{"disC", "disB", "disA"} {
		seedApplyCandidateMod(t, svc, game, "src", id, "Dis "+id, "1.0", true, nil)
	}

	// Enable-eligible: installed+disabled+cached, referenced by profile.Mods.
	for _, id := range []string{"enA", "enB", "enC"} {
		seedApplyCandidateMod(t, svc, game, "src", id, "En "+id, "1.0", false, map[string][]byte{id + ".dat": []byte(id)})
	}

	// profile.Mods interleaves enable/install refs in an order matching
	// neither alphabetical nor insertion order for either sub-sequence.
	for _, id := range []string{"enC", "insB", "enA", "insC", "enB", "insA"} {
		require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}))
	}

	origYes := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = origYes })

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	disA, disB, disC := strings.Index(out, "Dis disA"), strings.Index(out, "Dis disB"), strings.Index(out, "Dis disC")
	require.NotEqual(t, -1, disA, "Dis disA must be printed")
	require.NotEqual(t, -1, disB, "Dis disB must be printed")
	require.NotEqual(t, -1, disC, "Dis disC must be printed")
	assert.Less(t, disA, disB, "mods unlisted in profile.Mods must print in sorted-key order")
	assert.Less(t, disB, disC, "mods unlisted in profile.Mods must print in sorted-key order")

	enC, enA, enB := strings.Index(out, "En enC"), strings.Index(out, "En enA"), strings.Index(out, "En enB")
	require.NotEqual(t, -1, enC, "En enC must be printed")
	require.NotEqual(t, -1, enA, "En enA must be printed")
	require.NotEqual(t, -1, enB, "En enB must be printed")
	assert.Less(t, enC, enA, "enable-eligible mods must print in profile.Mods order")
	assert.Less(t, enA, enB, "enable-eligible mods must print in profile.Mods order")

	insB, insC, insA := strings.Index(out, "src:insB"), strings.Index(out, "src:insC"), strings.Index(out, "src:insA")
	require.NotEqual(t, -1, insB, "src:insB must be printed")
	require.NotEqual(t, -1, insC, "src:insC must be printed")
	require.NotEqual(t, -1, insA, "src:insA must be printed")
	assert.Less(t, insB, insC, "install-eligible mods must print in profile.Mods order")
	assert.Less(t, insC, insA, "install-eligible mods must print in profile.Mods order")
}

// TestSelectFilesToDownload_VersionAuthoritative is the #96 direct unit test
// for selectFilesToDownload's version parameter: same precedence as
// internal/core's selectVersionedDeployFiles (drift heals to the recorded
// version, gone IDs heal to the recorded version, an unresolvable target
// hard-fails naming the version, and an empty version preserves the exact
// pre-#96 behavior).
func TestSelectFilesToDownload_VersionAuthoritative(t *testing.T) {
	// file11 (another 1.0 file) and file12 (a non-primary file at an
	// unrelated version, 2.0) extend the base fixture to cover the fast
	// path and the stored-intersect-matches precedence (rule 3) without
	// disturbing any of the original four assertions below - each is
	// re-verified against the larger fixture inline.
	files := []domain.DownloadableFile{
		{ID: "10", Version: "1.5", IsPrimary: true, Category: "MAIN"},
		{ID: "9", Version: "1.0", Category: "ARCHIVED"},
		{ID: "11", Version: "1.0", Category: "ARCHIVED"},
		{ID: "12", Version: "2.0", Category: "OPTIONAL"},
	}

	// Drift: stored ID exists upstream but is the wrong version - version wins.
	got, err := selectFilesToDownload(files, []string{"10"}, "1.0")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "9", got[0].ID)

	// Gone IDs heal to the recorded version.
	got, err = selectFilesToDownload(files, []string{"999"}, "1.0")
	require.NoError(t, err)
	assert.Equal(t, "9", got[0].ID)

	// Unresolvable, no stored ID present upstream at all: extended #95
	// wording (rule 4a).
	_, err = selectFilesToDownload(files, []string{"999"}, "0.5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `version "0.5" not available`)

	// Legacy: empty version behaves exactly as before.
	got, err = selectFilesToDownload(files, nil, "")
	require.NoError(t, err)
	assert.Equal(t, "10", got[0].ID)

	// Rule 2 (fast path): both stored IDs are returned as-is, without
	// re-resolution, once their combined effective version agrees with the
	// record - file9 (1.0) is the representative (neither stored file is
	// primary, so the first one carrying a version wins), and file12 (2.0,
	// an unrelated companion file's own version) rides along BOTH. This is
	// what distinguishes the fast path from rule 3's stored-intersect-
	// matches, which would keep only file9 (file12 isn't a 1.0 match).
	got, err = selectFilesToDownload(files, []string{"9", "12"}, "1.0")
	require.NoError(t, err)
	require.Len(t, got, 2, "fast path must return the whole stored set, not just the version-matching subset")
	gotIDs := []string{got[0].ID, got[1].ID}
	assert.ElementsMatch(t, []string{"9", "12"}, gotIDs)

	// Rule 3 (stored ∩ matches): stored IDs 10 and 9 disagree in effective
	// version with the record (file10, the primary, is 1.5) so the fast
	// path does not fire; among the files that DO match "1.0" (9 and 11),
	// only the one that was ALSO a stored ID (9) is kept - file11 is
	// excluded even though it's a 1.0 match, because it was never a stored
	// ID for this mod.
	got, err = selectFilesToDownload(files, []string{"10", "9"}, "1.0")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "9", got[0].ID)

	// Rule 5 (ErrVersionNotFound wrap): no stored IDs at all, and the
	// requested version matches nothing upstream.
	_, err = selectFilesToDownload(files, nil, "3.0")
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrVersionNotFound))
	assert.Contains(t, err.Error(), "edit the profile's version or reinstall")

	// Rule 4b (new split, #96 fix round 1): a stored ID (12) IS still
	// present upstream, but the recorded version (3.0) matches nothing -
	// this is a wrong version record on a file that's still there, not a
	// gone file, so it must NOT get the #95 "no longer available upstream"
	// wording; it gets a distinct ErrVersionNotFound wrap pointing at
	// verify/update instead of reinstall.
	_, err = selectFilesToDownload(files, []string{"12"}, "3.0")
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrVersionNotFound))
	assert.NotContains(t, err.Error(), "no longer available upstream", "a present-upstream stored ID must not get the gone-file wording")
	assert.Contains(t, err.Error(), `installed file(s) (ID(s): 12) do not match recorded version "3.0"`)
	assert.Contains(t, err.Error(), "run 'lmm verify --fix' to correct the version record, or 'lmm update' to adopt the current version")
}

// TestSelectFilesToDownload_CategoryPriorityTieBreak covers #144 item 5 for
// the cmd twin: when the recorded version offers no primary file and no
// stored ID narrows the choice, the final fallback used to be matches[0] -
// upstream listing order - ignoring fileCategoryPriority entirely. The
// category priority (MAIN > OPTIONAL > UPDATE > MISCELLANEOUS > unknown >
// archived) now breaks that tie, stable: first-listed wins among equal
// priorities, so category-less listings (custom sources) behave exactly as
// before.
//
// PARITY: internal/core/flows_selection_test.go's
// TestSelectVersionedDeployFiles_CategoryPriorityTieBreak encodes this SAME
// fixture table and expected picks through selectVersionedDeployFiles - this
// tail is the hand-mirrored twin of core's pickVersionMatch (internal/core
// cannot import cmd/lmm), so the two tests are the drift guard for the
// twins. Change both together, or not at all.
func TestSelectFilesToDownload_CategoryPriorityTieBreak(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "misc20", Version: "2.0", Category: "MISCELLANEOUS"},
		{ID: "upd20", Version: "2.0", Category: "UPDATE"},
		{ID: "opt20", Version: "2.0", Category: "OPTIONAL"},
		{ID: "optA30", Version: "3.0", Category: "OPTIONAL"},
		{ID: "optB30", Version: "3.0", Category: "OPTIONAL"},
		{ID: "plain40a", Version: "4.0"},
		{ID: "plain40b", Version: "4.0"},
		{ID: "beta50", Version: "5.0", Category: "beta"},
		{ID: "upd50", Version: "5.0", Category: "update"},
		{ID: "opt60", Version: "6.0", Category: "OPTIONAL"},
		{ID: "miscPrim60", Version: "6.0", Category: "MISCELLANEOUS", IsPrimary: true},
	}

	cases := []struct {
		name    string
		version string
		wantID  string
	}{
		{"category priority beats listing order", "2.0", "opt20"},
		{"equal categories keep first-listed", "3.0", "optA30"},
		{"category-less files keep first-listed", "4.0", "plain40a"},
		{"known category beats unknown, case-insensitively", "5.0", "upd50"},
		{"a primary file still outranks category priority", "6.0", "miscPrim60"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectFilesToDownload(files, nil, tc.version)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.wantID, got[0].ID)
		})
	}
}

// TestDoProfileApply_StampsSelectedFileVersion guards issue #94 for
// doProfileApply's "install missing mods" save site: when a mod referenced
// by the profile (but not yet installed) has a primary file whose version
// differs from the mod's latest version, the persisted InstalledMod row
// must record the file's version - the version of the bytes actually
// downloaded and deployed - not the mod-level "latest" version. Reuses
// fakeInstallSource (defined in install_test.go, same package) rather than
// setupDoProfileSwitchTest's sourceless default, since this path needs a
// real GetMod/GetModFiles/download round-trip.
//
// The profile ref's Version is deliberately "" (a legacy/unpinned ref), not
// "1.0": #96 made a non-empty ref.Version an authoritative exact-match pin
// for selectFilesToDownload (mirroring internal/core's
// selectVersionedDeployFiles), and "1.0" here was only ever the mod-level
// label coincidentally reused for the ref before #96 existed - the
// mod-vs-file version discrepancy this test actually guards is entirely
// between src.AddMod's Version and the served file's Version, independent
// of ref.Version.
func TestDoProfileApply_StampsSelectedFileVersion(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"test-src": game.ID}

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "main", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.1"},
		})
	src.AddDownload("main", []byte("plugin content"))

	pm := getProfileManager(svc)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: ""}))

	origYes := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = origYes })

	require.NoError(t, doProfileApply(context.Background(), svc, game, nil))

	installed, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.1", installed.Version, "installed mod version must be the selected file's version, not the profile ref's version")
}

// TestDoProfileApply_StoredFileIDsGone_FailsModWithoutSubstitution guards
// #95 for doProfileApply's "install missing mods" loop: when a profile mod's
// stored FileIDs no longer match anything the source currently lists,
// selectFilesToDownload must fail the mod with the upstream-gone error
// instead of silently substituting the primary file (mirrors
// internal/core's TestService_DeployProfile_StoredFileIDsGone_
// SkipsModWithClearError from task B1). A second mod with valid FileIDs
// proves the toInstall loop continues past the failure rather than
// aborting.
//
// The served files carry no Version (unlike TestSelectFilesToDownload_
// VersionAuthoritative's fixture) so anyFileHasVersion is false and this
// exercises selectFilesToDownloadLegacy - #96 deliberately changes this
// exact scenario for a VERSIONED source (gone FileIDs now heal to a
// version-matched file rather than hard-failing); the un-extended #95
// no-substitution contract this test guards only still applies vacuously,
// per decision 1/4.
func TestDoProfileApply_StoredFileIDsGone_FailsModWithoutSubstitution(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"test-src": game.ID}

	// mod1: stored FileIDs reference "stale-id", which the source no longer
	// lists (only "main" is offered). No download is registered for "main"
	// either - if the old fallback-to-primary behavior fired, the download
	// itself would fail with a distinct (download) error, not the
	// upstream-gone message this test asserts on.
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Stale Mod", Version: "1.0", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "main", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN"},
		})

	// mod2: stored FileIDs match what the source lists - must install normally.
	src.AddMod(&domain.Mod{ID: "mod2", SourceID: "test-src", Name: "Good Mod", Version: "1.0", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "main2", Name: "Main File", FileName: "mod2.esp", IsPrimary: true, Category: "MAIN"},
		})
	src.AddDownload("main2", []byte("plugin content"))

	pm := getProfileManager(svc)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: "1.0", FileIDs: []string{"stale-id"}}))
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "mod2", Version: "1.0", FileIDs: []string{"main2"}}))

	origYes := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = origYes })

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "no longer available upstream", "mod1's stale FileIDs must fail with the upstream-gone error, not a silent fallback")
	assert.Contains(t, out, "stale-id", "the error must name the stale file ID")
	assert.NotContains(t, out, "using primary", "the old silent-fallback warning must not print")

	_, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	assert.Error(t, err, "mod1 must not be installed - no substitution should occur")

	installed2, err := svc.GetInstalledMod("test-src", "mod2", game.ID, "default")
	require.NoError(t, err, "mod2 must still install - the toInstall loop must continue past mod1's failure")
	assert.Equal(t, "1.0", installed2.Version)
}

// TestDoProfileApply_VersionDrift_SchedulesReinstall is the #96 convergence
// guard for doProfileApply's diff loop: a mod installed+enabled at "1.5" but
// pinned by the profile YAML at "1.0" must be classified into "Will
// install" (a reinstall at the profile's version), not left alone as
// already-satisfied. Declining the prompt proves classification alone
// (no source/download plumbing needed) and that nothing mutates meanwhile.
func TestDoProfileApply_VersionDrift_SchedulesReinstall(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	seedApplyCandidateMod(t, svc, game, "src", "mod1", "Mod One", "1.5", true, map[string][]byte{"mod1.esp": []byte("v1.5")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	var out string
	withStdin(t, "n\n", func() {
		out = captureStdout(t, func() error {
			return doProfileApply(context.Background(), svc, game, nil)
		})
	})

	assert.Contains(t, out, "Will install 1 mod(s):\n")
	assert.Contains(t, out, "src:mod1 v1.0")
	assert.NotContains(t, out, "Will enable", "a version-drifted mod must not be classified as a plain enable")

	installed, err := svc.GetInstalledMod("src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.5", installed.Version, "declining the prompt must not mutate anything")
}

// TestDoProfileApply_MatchingVersion_RemainsNoop is the regression guard for
// the drift case's guard condition: when the profile ref's Version matches
// the installed row's, doProfileApply must take its pre-#96 fast "already
// matches" path - no prompt, no toInstall entry.
func TestDoProfileApply_MatchingVersion_RemainsNoop(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	seedApplyCandidateMod(t, svc, game, "src", "mod1", "Mod One", "1.5", true, map[string][]byte{"mod1.esp": []byte("v1.5")})
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.5"}))

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	assert.Equal(t, "System already matches profile default.\n", out)
}

// TestDoProfileApply_VersionDrift_ReplacesDeployedMod_EndToEnd is the #96
// full happy-path guard: mod1 is installed+enabled+DEPLOYED at 1.5 (file
// "mod1.esp" symlinked into the game dir); the profile pins 1.0, served by a
// distinct file ("mod1-old.esp"). Applying must download+cache the 1.0 file,
// swap the deployment via Installer.Replace (not a bare Install, which would
// leave the obsolete 1.5 file behind - mirrors ApplyUpdate's replace
// semantics), and record the downgrade in the DB.
func TestDoProfileApply_VersionDrift_ReplacesDeployedMod_EndToEnd(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"test-src": game.ID}

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.5", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "new", Name: "Main", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.5"},
			{ID: "old", Name: "Old", FileName: "mod1-old.esp", Category: "ARCHIVED", Version: "1.0"},
		})
	src.AddDownload("new", []byte("new-payload"))
	src.AddDownload("old", []byte("old-payload"))

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", "1.5", "mod1.esp", []byte("new-payload")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.5", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		FileIDs:      []string{"new"},
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "test-src", Version: "1.5", GameID: game.ID}, "default"))
	_, err := os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	require.NoError(t, err, "precondition: 1.5 must be actually deployed")

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: "1.0"}))

	origYes := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = origYes })

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "✓ Installed: Mod One\n")

	installed, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version, "must record the pinned version, not the source's latest")
	assert.Equal(t, []string{"old"}, installed.FileIDs, "must have selected the archived 1.0 file")
	assert.True(t, installed.Deployed, "the row must record that files are actually live on disk (review finding 3)")

	assert.True(t, svc.GetGameCache(game).Exists(game.ID, "test-src", "mod1", "1.0"), "the downgraded version must be cached")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.True(t, os.IsNotExist(err), "the obsolete 1.5 file must be removed by Replace, not left behind by a bare Install")
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the new 1.0 file must be deployed")
}

// TestDoProfileApply_PartialCacheEntry_StillDownloads is the #96 review
// round 1 finding 2 guard for the cmd twin: a version directory can exist on
// disk (e.g. left behind by a previous download run that broke off before
// any file was committed) without actually holding the expected file. The
// cache-first guard must not mistake directory presence alone for "already
// cached" - bare cache.Exists would report true here and silently skip the
// download, leaving the mod stuck without its files despite doProfileApply
// reporting success.
func TestDoProfileApply_PartialCacheEntry_StillDownloads(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"test-src": game.ID}

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "main", Name: "Main", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN"},
		})
	src.AddDownload("main", []byte("plugin content"))

	// Simulate a version directory left behind by a previously broken-off
	// download run: the directory exists but holds none of the expected
	// files.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, os.MkdirAll(gameCache.ModPath(game.ID, "test-src", "mod1", "1.0"), 0755))
	require.True(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.0"), "precondition: bare Exists must see the empty dir as already cached")

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: "1.0"}))

	origYes := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = origYes })

	require.NoError(t, doProfileApply(context.Background(), svc, game, nil))

	cached, err := gameCache.ListFiles(game.ID, "test-src", "mod1", "1.0")
	require.NoError(t, err)
	assert.Contains(t, cached, "mod1.esp", "the cache entry must actually contain the downloaded file, not just an empty directory")

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.NoError(t, err, "the file must actually be deployed, not merely a DB row claiming so")

	// The completed download must leave the entry MARKED, so a later apply
	// over the same profile is a genuine cache hit.
	assert.True(t, gameCache.HasFileIDs(game.ID, "test-src", "mod1", "1.0", []string{"main"}),
		"a successful download must commit the per-file completion marker")
	assert.Equal(t, 1, src.DownloadCount(), "exactly one download, not a retry loop")
}

// TestDoProfileApply_FullyMarkedCache_SkipsDownload is the #96 review round 2
// guard for the cmd twin (mirrors internal/core/flows_test.go's
// TestApplyProfileSwitch_FullyMarkedCache_SkipsDownload): the cache-first
// guard must actually FIRE for a complete cache entry. Round 1 keyed the
// check off DownloadableFile.FileName, but the default DeployExtract flow
// stores an archive's extracted MEMBERS ("mod1.esp" here) rather than the
// archive's own name ("mod1.zip") - so the guard read false for essentially
// every archive-based mod and redownloaded despite a complete cache.
func TestDoProfileApply_FullyMarkedCache_SkipsDownload(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"test-src": game.ID}

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "main", Name: "Main", FileName: "mod1.zip", IsPrimary: true, Category: "MAIN"},
		})
	src.AddDownload("main", []byte("archive bytes"))

	// A COMPLETE cache entry as a real extract-mode download leaves it: the
	// archive member on disk under a name that matches nothing about the
	// DownloadableFile ("mod1.zip"), plus file "main"'s completion marker.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", "1.0", "mod1.esp", []byte("plugin content")))
	require.NoError(t, cache.MarkFileComplete(gameCache.ModPath(game.ID, "test-src", "mod1", "1.0"), "main"))

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: "1.0"}))

	origYes := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = origYes })

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})
	assert.Contains(t, out, "✓ Installed: Mod One\n")

	assert.Equal(t, 0, src.DownloadCount(),
		"a fully-marked cache entry must be deployed from cache, not redownloaded")

	_, err := os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.NoError(t, err, "the cached file must still be deployed")
}

// TestDoProfileApply_VersionDrift_OldCachePruned_InstallsWithoutReplace is
// the #96 review round 2 guard for the cmd twin's missing Replace guard:
// Installer.Replace reads the OLD version's cache entry to work out which
// files to retire, and hard-fails with "old mod not in cache" when that entry
// has been pruned. doProfileApply used to call Replace on prev.Deployed
// alone, so a pruned old cache made profile apply fail to converge outright.
// Core's ApplyProfileSwitch already guards on the old cache's presence and
// falls back to Install; this pins the twin to the same shape.
func TestDoProfileApply_VersionDrift_OldCachePruned_InstallsWithoutReplace(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"test-src": game.ID}

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.5", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "new", Name: "Main", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.5"},
			{ID: "old", Name: "Old", FileName: "mod1-old.esp", Category: "ARCHIVED", Version: "1.0"},
		})
	src.AddDownload("new", []byte("new-payload"))
	src.AddDownload("old", []byte("old-payload"))

	// mod1 is installed, enabled and genuinely DEPLOYED at 1.5...
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "test-src", "mod1", "1.5", "mod1.esp", []byte("new-payload")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.5", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		FileIDs:      []string{"new"},
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "test-src", Version: "1.5", GameID: game.ID}, "default"))
	_, err := os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	require.NoError(t, err, "precondition: 1.5 must be actually deployed")

	// ...but its 1.5 cache entry has since been pruned, so Replace has
	// nothing to read the old file list from.
	require.NoError(t, gameCache.Delete(game.ID, "test-src", "mod1", "1.5"))
	require.False(t, gameCache.Exists(game.ID, "test-src", "mod1", "1.5"), "precondition: the old cache entry must be gone")

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: "1.0"}))

	origYes := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = origYes })

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	assert.NotContains(t, out, "deploy failed", "a pruned old cache must not break convergence")
	assert.Contains(t, out, "✓ Installed: Mod One\n")

	installed, err := svc.GetInstalledMod("test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version, "convergence must still reach the profile's pinned version")
	assert.Equal(t, []string{"old"}, installed.FileIDs)

	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1-old.esp"))
	assert.NoError(t, err, "the new 1.0 file must be deployed via the Install fallback")
}

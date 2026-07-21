package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstallCmd_Structure tests the install command structure
func TestInstallCmd_Structure(t *testing.T) {
	assert.Equal(t, "install <query>", installCmd.Use)
	assert.NotEmpty(t, installCmd.Short)
	assert.NotEmpty(t, installCmd.Long)

	// Check flags exist
	assert.NotNil(t, installCmd.Flags().Lookup("source"))
	assert.NotNil(t, installCmd.Flags().Lookup("profile"))
	assert.NotNil(t, installCmd.Flags().Lookup("version"))
	assert.NotNil(t, installCmd.Flags().Lookup("id"))
	assert.NotNil(t, installCmd.Flags().Lookup("file"))
	assert.NotNil(t, installCmd.Flags().Lookup("yes"))
}

// TestInstallCmd_NoGame tests install without game flag
func TestInstallCmd_NoGame(t *testing.T) {
	// Reset flags
	gameID = ""
	installModID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(installCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"install", "test mod"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

// TestInstallCmd_NoQueryOrID tests install without query or --id
func TestInstallCmd_NoQueryOrID(t *testing.T) {
	gameID = "test-game"
	installModID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(installCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"install"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "search query or --id is required")
}

// TestInstallCmd_DefaultFlags tests that default flag values are set
func TestInstallCmd_DefaultFlags(t *testing.T) {
	// Check default values
	sourceFlag := installCmd.Flags().Lookup("source")
	assert.Equal(t, "", sourceFlag.DefValue) // empty = auto-detect from game config

	profileFlag := installCmd.Flags().Lookup("profile")
	assert.Equal(t, "", profileFlag.DefValue)

	versionFlag := installCmd.Flags().Lookup("version")
	assert.Equal(t, "", versionFlag.DefValue)

	idFlag := installCmd.Flags().Lookup("id")
	assert.Equal(t, "", idFlag.DefValue)

	fileFlag := installCmd.Flags().Lookup("file")
	assert.Equal(t, "", fileFlag.DefValue)

	yesFlag := installCmd.Flags().Lookup("yes")
	assert.Equal(t, "false", yesFlag.DefValue)
}

// TestInstallCmd_GameNotFound tests install with non-existent game
func TestInstallCmd_GameNotFound(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "non-existent-game"
	installModID = "12345"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(installCmd)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"install", "--id", "12345"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "game not found")

	// Reset
	installModID = ""
}

// TestFormatSize tests the formatSize function
func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.00 MB"},
		{1572864, "1.50 MB"},
		{1073741824, "1.00 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatSize(tt.bytes)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestProgressBar tests the progressBar function
func TestProgressBar(t *testing.T) {
	tests := []struct {
		percentage float64
		width      int
		expected   int // number of filled characters
	}{
		{0, 10, 0},
		{50, 10, 5},
		{100, 10, 10},
		{25, 20, 5},
		{110, 10, 10}, // capped at 100%
	}

	for _, tt := range tests {
		bar := progressBar(tt.percentage, tt.width)
		assert.Equal(t, tt.width, len([]rune(bar)))
	}
}

// TestFilterAndSortFiles tests file filtering and sorting
func TestFilterAndSortFiles(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "1", FileName: "optional.zip", Category: "OPTIONAL"},
		{ID: "2", FileName: "main.zip", Category: "MAIN"},
		{ID: "3", FileName: "archived.zip", Category: "ARCHIVED"},
		{ID: "4", FileName: "update.zip", Category: "UPDATE"},
		{ID: "5", FileName: "main2.zip", Category: "MAIN"},
		{ID: "6", FileName: "old.zip", Category: "OLD_VERSION"},
	}

	// Without archived
	filtered := filterAndSortFiles(files, false)
	assert.Len(t, filtered, 4) // excludes ARCHIVED and OLD_VERSION

	// Check order: MAIN, MAIN, OPTIONAL, UPDATE
	assert.Equal(t, "MAIN", filtered[0].Category)
	assert.Equal(t, "MAIN", filtered[1].Category)
	assert.Equal(t, "OPTIONAL", filtered[2].Category)
	assert.Equal(t, "UPDATE", filtered[3].Category)

	// With archived
	withArchived := filterAndSortFiles(files, true)
	assert.Len(t, withArchived, 6) // includes all

	// ARCHIVED should be at the end
	assert.Equal(t, "ARCHIVED", withArchived[4].Category)
	assert.Equal(t, "OLD_VERSION", withArchived[5].Category)
}

func TestDisplayFileLabel(t *testing.T) {
	tests := []struct {
		name     string
		file     domain.DownloadableFile
		expected string
	}{
		{
			name:     "uses filename when it looks normal",
			file:     domain.DownloadableFile{Name: "Main File", FileName: "mod-1.0.zip"},
			expected: "mod-1.0.zip",
		},
		{
			name:     "uses name for uuid-like opaque filename",
			file:     domain.DownloadableFile{Name: "MoreTreeResources 2x", FileName: "c3f2ac27-ca21-42f3-bb09-cc41e09db10d"},
			expected: "MoreTreeResources 2x",
		},
		{
			name:     "uses name for path-like filename",
			file:     domain.DownloadableFile{Name: "MoreTreeResources 2x", FileName: "c3/f2/ac/test-mod.zip"},
			expected: "MoreTreeResources 2x",
		},
		{
			name:     "falls back to name when filename missing",
			file:     domain.DownloadableFile{Name: "MoreTreeResources 2x"},
			expected: "MoreTreeResources 2x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, displayFileLabel(tt.file))
		})
	}
}

// TestFileCategoryPriority tests category priority ordering
func TestFileCategoryPriority(t *testing.T) {
	assert.Less(t, fileCategoryPriority("MAIN"), fileCategoryPriority("OPTIONAL"))
	assert.Less(t, fileCategoryPriority("OPTIONAL"), fileCategoryPriority("UPDATE"))
	assert.Less(t, fileCategoryPriority("UPDATE"), fileCategoryPriority("MISCELLANEOUS"))
	assert.Less(t, fileCategoryPriority("MISCELLANEOUS"), fileCategoryPriority("ARCHIVED"))

	// Case insensitive
	assert.Equal(t, fileCategoryPriority("main"), fileCategoryPriority("MAIN"))
	assert.Equal(t, fileCategoryPriority("Main"), fileCategoryPriority("MAIN"))
}

// TestInstallCmd_ShowArchivedFlag tests the show-archived flag exists
func TestInstallCmd_ShowArchivedFlag(t *testing.T) {
	flag := installCmd.Flags().Lookup("show-archived")
	assert.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

// TestParseRangeSelection tests parsing of range-style selections
func TestParseRangeSelection(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		expected []int
		wantErr  bool
	}{
		{"single number", "3", 10, []int{3}, false},
		{"comma separated", "1,3,5", 10, []int{1, 3, 5}, false},
		{"dash range", "2-5", 10, []int{2, 3, 4, 5}, false},
		{"double dot range", "2..5", 10, []int{2, 3, 4, 5}, false},
		{"mixed", "1,3-5,8", 10, []int{1, 3, 4, 5, 8}, false},
		{"with spaces", "1, 3, 5", 10, []int{1, 3, 5}, false},
		{"out of range", "15", 10, nil, true},
		{"invalid range", "5-3", 10, nil, true},
		{"zero", "0", 10, nil, true},
		{"negative", "-1", 10, nil, true},
		{"empty", "", 10, nil, true},
		{"non-numeric", "abc", 10, nil, true},
		{"duplicate removal", "1,1,2", 10, []int{1, 2}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseRangeSelection(tt.input, tt.max)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestPromptMultiSelection_Cancel tests that 'q' cancels cleanly (no error)
func TestPromptMultiSelection_Cancel(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"lowercase q", "q\n"},
		{"uppercase Q", "Q\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewBufferString(tt.input)
			selections, err := promptMultiSelectionFrom(r, "Select", 1, 10)
			assert.ErrorIs(t, err, ErrCancelled, "cancel should return ErrCancelled")
			assert.Nil(t, selections, "cancel should return nil selections")
		})
	}
}

// TestPromptMultiSelection_Default tests that empty input returns default choice
func TestPromptMultiSelection_Default(t *testing.T) {
	r := bytes.NewBufferString("\n")
	selections, err := promptMultiSelectionFrom(r, "Select", 3, 10)
	assert.NoError(t, err)
	assert.Equal(t, []int{3}, selections)
}

// TestPromptMultiSelection_ValidSelection tests normal selection
func TestPromptMultiSelection_ValidSelection(t *testing.T) {
	r := bytes.NewBufferString("1,3,5\n")
	selections, err := promptMultiSelectionFrom(r, "Select", 1, 10)
	assert.NoError(t, err)
	assert.Equal(t, []int{1, 3, 5}, selections)
}

// TestPromptMultiSelection_Range tests range selection
func TestPromptMultiSelection_Range(t *testing.T) {
	r := bytes.NewBufferString("2-4\n")
	selections, err := promptMultiSelectionFrom(r, "Select", 1, 10)
	assert.NoError(t, err)
	assert.Equal(t, []int{2, 3, 4}, selections)
}

// --- doInstall (Phase 5b Task 2 CLI refit) ---
//
// fakeInstallSource is a minimal source.ModSource for doInstall's refit
// tests, backed by a real httptest server so ApplyInstall's actual
// DownloadModToCache path runs end to end - mirrors deploy_test.go/
// profile_test.go's own real-source-over-httptest pattern for cmd/lmm tests,
// since internal/core's test-only mock sources (mockSource,
// mockSourceWithDownloads, ...) live in a different package and aren't
// visible here.

type fakeInstallSource struct {
	id        string
	mods      map[string]*domain.Mod
	files     map[string][]domain.DownloadableFile
	downloads map[string][]byte // fileID -> raw content
	srv       *httptest.Server
}

func newFakeInstallSource(id string) *fakeInstallSource {
	s := &fakeInstallSource{
		id:        id,
		mods:      make(map[string]*domain.Mod),
		files:     make(map[string][]domain.DownloadableFile),
		downloads: make(map[string][]byte),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fileID := strings.TrimPrefix(r.URL.Path, "/")
		if content, ok := s.downloads[fileID]; ok {
			_, _ = w.Write(content)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	s.srv = httptest.NewServer(mux)
	return s
}

func (s *fakeInstallSource) Close()          { s.srv.Close() }
func (s *fakeInstallSource) ID() string      { return s.id }
func (s *fakeInstallSource) Name() string    { return "Fake Install Source" }
func (s *fakeInstallSource) AuthURL() string { return "" }
func (s *fakeInstallSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, nil
}
func (s *fakeInstallSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (s *fakeInstallSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if mod, ok := s.mods[modID]; ok {
		return mod, nil
	}
	return nil, domain.ErrModNotFound
}
func (s *fakeInstallSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	if mod == nil {
		return nil, nil
	}
	return s.mods[mod.ID].Dependencies, nil
}
func (s *fakeInstallSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return s.files[mod.ID], nil
}
func (s *fakeInstallSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return s.srv.URL + "/" + fileID, nil
}
func (s *fakeInstallSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// AddMod registers mod (with mod.Dependencies already set, if any) and its
// downloadable files.
func (s *fakeInstallSource) AddMod(mod *domain.Mod, files []domain.DownloadableFile) {
	s.mods[mod.ID] = mod
	s.files[mod.ID] = files
}

// AddDownload stages fileID's raw download content. Filenames without an
// archive extension (e.g. ".esp") take DownloadModToCache's "not an
// archive - just copy" branch, so the raw bytes land directly in the cache
// under that filename - the same trick deploy_test.go's custom-manifest
// tests use.
func (s *fakeInstallSource) AddDownload(fileID string, content []byte) {
	s.downloads[fileID] = content
}

// setupDoInstallTest builds a *core.Service, a game configured for
// fakeInstallSource, and resets install's package-level flag globals to
// sane (mostly non-interactive) defaults for calling doInstall directly,
// following setupDoDeployTest/setupDoUninstallTest's pattern. Callers seed
// the source's mods/files/downloads themselves.
func setupDoInstallTest(t *testing.T) (*core.Service, *domain.Game, *fakeInstallSource) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"test-src": "g1"},
	}

	oldSource, oldProfile, oldVersion, oldModID, oldFileID := installSource, installProfile, installVersion, installModID, installFileID
	oldYes, oldShowArchived, oldSkipVerify, oldForce, oldNoDeps := installYes, installShowArchived, skipVerify, installForce, installNoDeps
	oldVerbose, oldNoColor, oldNoHooks := verbose, noColor, noHooks
	installSource = "test-src"
	installProfile = ""
	installVersion = ""
	installModID = "mod1"
	installFileID = ""
	installYes = true
	installShowArchived = false
	skipVerify = false
	installForce = false
	installNoDeps = false
	verbose = false
	noColor = true
	noHooks = false
	t.Cleanup(func() {
		installSource, installProfile, installVersion, installModID, installFileID = oldSource, oldProfile, oldVersion, oldModID, oldFileID
		installYes, installShowArchived, skipVerify, installForce, installNoDeps = oldYes, oldShowArchived, oldSkipVerify, oldForce, oldNoDeps
		verbose, noColor, noHooks = oldVerbose, oldNoColor, oldNoHooks
	})

	return svc, game, src
}

// captureStdoutErr redirects os.Stdout for the duration of fn, returning
// both the captured output and fn's own error - the stdout counterpart to
// uninstall_test.go's captureStderrErr, for exercising doInstall's
// declined-prompt/error paths (unlike captureStdout, which requires fn to
// succeed).
func captureStdoutErr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fnErr := fn()
	require.NoError(t, w.Close(), "closing write end of the pipe")
	out, readErr := io.ReadAll(r)
	require.NoError(t, r.Close())
	require.NoError(t, readErr)
	return string(out), fnErr
}

// TestDoInstall_Verbose_HappyPath_PrintsExpectedOutput guards doInstall's
// refit onto PlanInstall/ApplyInstall for the common case (fresh install, no
// deps, no conflicts): byte-identical console output to the pre-refit CLI.
func TestDoInstall_Verbose_HappyPath_PrintsExpectedOutput(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	verbose = true
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", Author: "Someone", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", Name: "Main File", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN"}})
	src.AddDownload("main", []byte("plugin content"))

	out := captureStdout(t, func() error {
		return doInstall(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "Fetching mod mod1 from test-src...\n")
	assert.Contains(t, out, "Selected: Mod One v1.0 by Someone\n")
	assert.Contains(t, out, "File: mod1.esp\n")
	assert.Contains(t, out, "Downloading mod1.esp...\n")
	assert.Contains(t, out, "Extracting to cache...\n")
	assert.Contains(t, out, "Deploying to game directory...\n")
	assert.Contains(t, out, "✓ Installed: Mod One v1.0\n")
	assert.Contains(t, out, "Files deployed: 1\n")
	assert.Contains(t, out, "Added to profile: default\n")

	_, err := os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.NoError(t, err, "file should be deployed to the game directory")

	installed, err := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.True(t, installed.Enabled)
	assert.True(t, installed.Deployed)
}

// TestDoInstall_DependencyConfirmPrompt_DeclinedYieldsZeroMutations guards
// the deps path: the dependency-tree printout and "Install N mod(s)?"
// confirm prompt must be byte-identical to the pre-refit CLI (now sourced
// from *core.InstallPlan instead of a locally re-resolved dependency list),
// and declining must leave zero mutations - PlanInstall (already run by the
// time the prompt appears) has zero side effects, so this only requires
// doInstall to actually return before ever calling ApplyInstall.
func TestDoInstall_DependencyConfirmPrompt_DeclinedYieldsZeroMutations(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installYes = false

	dep := &domain.Mod{ID: "dep1", SourceID: "test-src", Name: "Dep One", Version: "1.0", GameID: "g1"}
	root := &domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", Author: "Someone", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "test-src", ModID: "dep1"}}}
	src.AddMod(dep, []domain.DownloadableFile{{ID: "dep-file", FileName: "dep1.esp", IsPrimary: true}})
	src.AddMod(root, []domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})

	var out string
	var err error
	withStdin(t, "n\n", func() {
		out, err = captureStdoutErr(t, func() error {
			return doInstall(context.Background(), svc, game, nil)
		})
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "installation cancelled")
	assert.Contains(t, out, "Dependency tree (install order):\n")
	assert.Contains(t, out, "1. Dep One v1.0 (ID: dep1) [dependency]\n")
	assert.Contains(t, out, "2. Mod One v1.0 (ID: mod1) [target]\n")
	assert.Contains(t, out, "Install 2 mod(s)? [Y/n]: ")

	_, dbErr := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
	assert.Error(t, dbErr, "declining must result in zero mutations")
	_, dbErr = svc.GetInstalledMod("test-src", "dep1", "g1", "default")
	assert.Error(t, dbErr, "declining must result in zero mutations")
}

// seedConflictingMod installs and deploys "other", owning shared.esp, then
// pre-seeds mod1's own cache (at the version the fake source will report)
// with an overlapping shared.esp - the only way PlanInstall's Conflicts gets
// populated (see InstallPlan.Conflicts' doc comment: only a mod already
// cached at its exact version reports conflicts), mirroring
// TestService_PlanInstall_ConflictingFilesListsPathAndOwningMod's approach.
func seedConflictingMod(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "test-src", "other", "1.0", "shared.esp", []byte("o")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "other", SourceID: "test-src", Name: "Other Mod", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "other", SourceID: "test-src", Version: "1.0", GameID: "g1"}, "default"))
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "test-src", "mod1", "1.0", "shared.esp", []byte("n")))
}

// TestDoInstall_ConflictPrompt_ForceSkipsPrompt guards the conflict prompt
// path (sourced from plan.Conflicts, computed by PlanInstall - not a fresh
// GetConflicts call) and --force's skip.
func TestDoInstall_ConflictPrompt_ForceSkipsPrompt(t *testing.T) {
	t.Run("prompts and aborts on decline", func(t *testing.T) {
		svc, game, src := setupDoInstallTest(t)
		installYes = false
		seedConflictingMod(t, svc, game)
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", Author: "Someone", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})

		var out string
		var err error
		withStdin(t, "n\n", func() {
			out, err = captureStdoutErr(t, func() error {
				return doInstall(context.Background(), svc, game, nil)
			})
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "installation cancelled")
		assert.Contains(t, out, "File conflicts detected:")
		assert.Contains(t, out, "will be overwritten. Continue? [y/N]: ")

		_, dbErr := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
		assert.Error(t, dbErr)
	})

	t.Run("--force skips the prompt", func(t *testing.T) {
		svc, game, src := setupDoInstallTest(t)
		installForce = true
		seedConflictingMod(t, svc, game)
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", Author: "Someone", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
		src.AddDownload("main", []byte("mod1 content"))

		out := captureStdout(t, func() error {
			return doInstall(context.Background(), svc, game, nil)
		})

		assert.NotContains(t, out, "File conflicts detected")
		assert.Contains(t, out, "✓ Installed: Mod One v1.0\n")

		_, err := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
		assert.NoError(t, err)
	})
}

// TestDoInstall_ShowArchivedFlag_ThreadsThroughRefit is the MANDATORY
// reviewer-directed test: a mod whose only file is ARCHIVED must be
// rejected ("no downloadable files") without --show-archived, and actually
// offered/installed with it - proving installShowArchived reaches
// PlanInstall through the refit (a forgotten argument would fail silently,
// a bool zero value, indistinguishable from "false" either way without this
// end-to-end check).
func TestDoInstall_ShowArchivedFlag_ThreadsThroughRefit(t *testing.T) {
	seedArchivedMod := func(t *testing.T) (*core.Service, *domain.Game) {
		svc, game, src := setupDoInstallTest(t)
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Archived Mod", Version: "1.0", Author: "Someone", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "old", Name: "Old Main", FileName: "mod1.esp", Category: "ARCHIVED"}})
		src.AddDownload("old", []byte("archived content"))
		return svc, game
	}

	t.Run("without --show-archived, no downloadable files", func(t *testing.T) {
		svc, game := seedArchivedMod(t)
		installShowArchived = false

		err := doInstall(context.Background(), svc, game, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no downloadable files available for this mod")

		_, dbErr := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
		assert.Error(t, dbErr)
	})

	t.Run("--show-archived offers and installs the archived file", func(t *testing.T) {
		svc, game := seedArchivedMod(t)
		installShowArchived = true

		out := captureStdout(t, func() error {
			return doInstall(context.Background(), svc, game, nil)
		})

		assert.Contains(t, out, "✓ Installed: Archived Mod v1.0\n")

		_, err := os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
		assert.NoError(t, err, "the archived file must actually be deployed")

		installed, err := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
		require.NoError(t, err)
		assert.Equal(t, []string{"old"}, installed.FileIDs)
	})
}

// TestDoInstall_FailurePath_PrintsAccumulatedDiagnosticsBeforeError guards
// the failure-path convention: diagnostics accumulated before a later fatal
// error (here, a forced install.before_all warning, followed by a download
// failure) must still reach stderr - ApplyInstall's progress events fire
// live, so doInstall's progress handler has already printed them by the
// time the fatal error is returned.
func TestDoInstall_FailurePath_PrintsAccumulatedDiagnosticsBeforeError(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installForce = true
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", Author: "Someone", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
	// Deliberately no AddDownload - the download 404s deterministically.

	scriptsDir := t.TempDir()
	failScript := filepath.Join(scriptsDir, "before_all.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\necho boom >&2\nexit 1\n"), 0755))
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeAll: failScript}}

	stderr, err := captureStderrErr(t, func() error {
		return doInstall(context.Background(), svc, game, nil)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "download failed")
	assert.Contains(t, stderr, "Warning: install.before_all hook failed (forced): ")

	_, dbErr := svc.GetInstalledMod("test-src", "mod1", "g1", "default")
	assert.Error(t, dbErr)
}

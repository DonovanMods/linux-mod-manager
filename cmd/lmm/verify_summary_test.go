package main

// `lmm verify`'s closing summary (#413 re-review P-a). The empty-profile
// branch (#217) printed a tally only when there were WARNINGS, hard-coded
// "0 issue(s)", and always ended with "Run with --fix to remove stale
// lmm-deployed files" - so a loader_never_ran issue on an empty profile got
// an X row and no tally, and a loader_adapter_ignored warning was told to
// run a --fix that has nothing to do for it.

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupVerifySummaryGame is an empty-profile BepInEx game with the loader
// installed but never run, on production's adapters. adapter is the
// games.yaml key ("" derives bepinex).
func setupVerifySummaryGame(t *testing.T, adapterName string) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	app.RegisterAdapters(svc)

	root := t.TempDir()
	preloader := filepath.Join(root, filepath.FromSlash(domain.BepInExPreloaderPath))
	require.NoError(t, os.MkdirAll(filepath.Dir(preloader), 0o755))
	require.NoError(t, os.WriteFile(preloader, []byte("preloader"), 0o644))

	game := &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: root, ModPath: root,
		LinkMethod: domain.LinkSymlink, Adapter: adapterName,
		Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err = svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
	require.NoError(t, err)

	oldProfile, oldJSON, oldFix := verifyProfile, jsonOutput, verifyFix
	verifyProfile, jsonOutput, verifyFix = "default", false, false
	t.Cleanup(func() { verifyProfile, jsonOutput, verifyFix = oldProfile, oldJSON, oldFix })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd, svc, game
}

// verifyTally runs doVerify in text and in --json mode and returns the text
// and the result the text is meant to summarise.
func verifyTally(t *testing.T, cmd *cobra.Command, svc *core.Service, game *domain.Game) (string, *core.VerifyResult) {
	t.Helper()
	text := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })

	jsonOutput = true
	defer func() { jsonOutput = false }()
	doc := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })
	var report core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(doc), &report))
	return text, report.Result
}

func TestDoVerify_EmptyProfile_SummaryCountsWhatWasFound(t *testing.T) {
	t.Run("an issue", func(t *testing.T) {
		cmd, svc, game := setupVerifySummaryGame(t, "")
		text, result := verifyTally(t, cmd, svc, game)

		require.False(t, result.HasFiles, "fixture: the empty-profile branch")
		require.Positive(t, result.Issues, "fixture: loader_never_ran is an issue; findings %v", result.Findings)
		assert.Contains(t, text, "No installed mods to verify.")
		assert.Contains(t, text, fmt.Sprintf("%d issue(s), %d warning(s) found.", result.Issues, result.Warnings))
		assert.NotContains(t, text, "--fix", "no row here is one --fix repairs")
	})

	t.Run("a warning --fix cannot clear", func(t *testing.T) {
		cmd, svc, game := setupVerifySummaryGame(t, "generic-files")
		text, result := verifyTally(t, cmd, svc, game)

		require.NotNil(t, findingByStatus(result, "loader_adapter_ignored"), "findings %v", result.Findings)
		assert.Contains(t, text, fmt.Sprintf("%d issue(s), %d warning(s) found.", result.Issues, result.Warnings))
		assert.NotContains(t, text, "--fix")
	})
}

// TestDoVerify_EmptyProfile_FixHintSaysWhatFixWouldDo: the empty-profile
// branch's hint named one repair - "remove stale lmm-deployed files" -
// whatever the fixable row actually was, so a misplaced plugin, which
// --fix re-lays out, was offered a removal (#413 final review F6). The hint
// is built from the fixable rows the run found.
func TestDoVerify_EmptyProfile_FixHintSaysWhatFixWouldDo(t *testing.T) {
	cmd, svc, game := setupVerifySummaryGame(t, "")
	ctx := context.Background()

	// A locally imported loose plugin, deployed into the game root where
	// BepInEx never loads it: rows, but no checksums, so verify takes the
	// empty-profile branch.
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, domain.SourceLocal, "loose", "1.0", "Loose.dll", []byte("dll")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "loose", SourceID: domain.SourceLocal, Name: "Loose", Version: "1.0", GameID: game.ID},
		ProfileName: "default", Enabled: true,
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default",
		domain.ModReference{SourceID: domain.SourceLocal, ModID: "loose", Version: "1.0"}))
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	// And a link lmm left inside a nested BepInEx/ directory.
	stray := filepath.Join(game.ModPath, "BepInEx", "plugins", "BepInEx", "plugins", "Stray.dll")
	require.NoError(t, os.MkdirAll(filepath.Dir(stray), 0o755))
	require.NoError(t, os.Symlink(svc.GetGameCache(game).GetFilePath(game.ID, domain.SourceLocal, "loose", "1.0", "Loose.dll"), stray))

	text, result := verifyTally(t, cmd, svc, game)
	require.False(t, result.HasFiles, "fixture: the empty-profile branch; findings %v", result.Findings)
	require.NotNil(t, findingByStatus(result, "loader_deployed_outside_loader"), "findings %v", result.Findings)
	require.NotNil(t, findingByStatus(result, "loader_nested_tree"), "findings %v", result.Findings)
	assert.Contains(t, text, fmt.Sprintf("%d issue(s), %d warning(s) found.\n", result.Issues, result.Warnings)+
		"Run with --fix to move plugins deployed outside BepInEx/ under it and remove the links lmm left in a nested BepInEx/ directory.\n")
}

// TestDoVerify_SuggestsFixOnlyForAFixableFinding is the same rule on the
// branch with files: a run whose only findings are ones --fix does not
// repair is not told to run it.
func TestDoVerify_SuggestsFixOnlyForAFixableFinding(t *testing.T) {
	cmd, svc, game := setupVerifySummaryGame(t, "")
	seedVerifySummaryMod(t, svc, game)

	text, result := verifyTally(t, cmd, svc, game)
	require.True(t, result.HasFiles, "fixture: the branch with files")
	require.Positive(t, result.Issues+result.Warnings, "findings %v", result.Findings)
	for _, f := range result.Findings {
		require.False(t, f.Fixable, "fixture: nothing here is fixable, but %s is", f.Status)
	}
	assert.Contains(t, text, fmt.Sprintf("%d issue(s), %d warning(s) found.", result.Issues, result.Warnings))
	assert.NotContains(t, text, "Run with --fix")
}

// seedVerifySummaryMod installs one checksummed local mod whose plugin is
// deployed, so verify takes the branch with files and finds only the
// loader's own never-ran issue.
func seedVerifySummaryMod(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	ctx := context.Background()
	const rel = "BepInEx/plugins/Thing/Thing.dll"
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, domain.SourceLocal, "thing", "1.0", rel, []byte("dll")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "thing", SourceID: domain.SourceLocal, Name: "Thing", Version: "1.0", GameID: game.ID},
		ProfileName: "default", Enabled: true, FileIDs: []string{"Thing-1.0.zip"},
	}))
	require.NoError(t, svc.SaveFileChecksum(ctx, domain.SourceLocal, "thing", game.ID, "default", "Thing-1.0.zip", "deadbeef"))
	deployed := filepath.Join(game.ModPath, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(deployed), 0o755))
	require.NoError(t, os.WriteFile(deployed, []byte("dll"), 0o644))
}

func findingByStatus(result *core.VerifyResult, status string) *core.VerifyFinding {
	for i := range result.Findings {
		if result.Findings[i].Status == status {
			return &result.Findings[i]
		}
	}
	return nil
}

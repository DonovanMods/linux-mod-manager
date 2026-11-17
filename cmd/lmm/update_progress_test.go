package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #664: `lmm update` printed nothing between its header and its ✓ line while
// the update extracted, replaced the deployed files and recorded the new
// version. It now names those steps, the way `lmm install` does.

// setupZipUpdate is setupDoUpdateTest with Mod One 1.0 installed and its
// source offering 2.0 as a zip (mod1-2.0.zip) - an archive the update
// extracts.
func setupZipUpdate(t *testing.T) (*core.Service, *domain.Game, *domain.InstalledMod) {
	t.Helper()
	svc, game, src := setupDoUpdateTest(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")})
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-2.0.zip", IsPrimary: true}})
	archive := filepath.Join(t.TempDir(), "mod1-2.0.zip")
	createTestArchive(t, archive, map[string]string{"mod1-new.esp": "new-content"})
	payload, err := os.ReadFile(archive)
	require.NoError(t, err)
	src.AddDownload("new-1", payload)
	return svc, game, mod
}

func TestApplySingleUpdate_PrintsTheStepsAfterTheDownload(t *testing.T) {
	svc, game, mod := setupZipUpdate(t)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.Contains(t, out, "Updating Mod One 1.0 → 2.0...\n"+
		"  Extracting mod1-2.0.zip...\n"+
		"  Deploying to game directory...\n"+
		"\n✓ Updated: Mod One 1.0 → 2.0\n", "got:\n%s", out)
	assert.NotContains(t, out, "Running", "hook steps are --verbose-gated")
	assert.NotContains(t, out, "Recording", "the recording step is --verbose-gated")
}

func TestApplySingleUpdate_Verbose_PrintsHooksAndRecording(t *testing.T) {
	svc, game, mod := setupZipUpdate(t)
	verbose = true
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeEach: writeOKScript(t, "ib.sh")}}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	// The download's \r progress line is ended before the first step.
	assert.Contains(t, out, "Downloading: 100.0%\n  Extracting mod1-2.0.zip...\n", "got:\n%s", out)
	assert.Contains(t, out, "  Extracting mod1-2.0.zip...\n"+
		"  Running install.before_each hook...\n"+
		"  Deploying to game directory...\n"+
		"  Recording the update...\n"+
		"\n✓ Updated: Mod One 1.0 → 2.0\n", "got:\n%s", out)
}

func TestDoUpdate_FromFile_PrintsTheSteps(t *testing.T) {
	svc, game, dir := setupUpdateFromFileTest(t)
	updateFromFile = writeFromFileArchive(t, dir, "mod1-2.0.zip")

	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, []string{"mod1"}) })
	assert.Contains(t, out, "  Extracting mod1-2.0.zip...\n"+
		"  Deploying to game directory...\n", "got:\n%s", out)
}

// A batch (`lmm update --all`) stays one line per mod; its steps are
// --verbose detail, as its download percentage already is.
func TestDoUpdate_All_StepsAreVerboseDetail(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verbose bool
	}{{"quiet", false}, {"verbose", true}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, game, _ := setupZipUpdate(t)
			verbose = tc.verbose
			updateAll = true

			out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
			assert.Contains(t, out, "✓ Mod One 1.0 → 2.0")
			if tc.verbose {
				assert.Contains(t, out, "  Extracting mod1-2.0.zip...\n  Deploying to game directory...\n", "got:\n%s", out)
			} else {
				assert.NotContains(t, out, "Extracting", "got:\n%s", out)
				assert.NotContains(t, out, "Deploying", "got:\n%s", out)
			}
		})
	}
}

func writeOKScript(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/bash\nexit 0\n"), 0o755))
	return path
}

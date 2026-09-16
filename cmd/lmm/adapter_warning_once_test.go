package main

// #413 re-review M2: say it once. Design decision 11's adapter warning is a
// ~500-character sentence, and it reached a user twice in one command -
// on stderr when the service opened, then again in `lmm game show`'s loader
// section or `lmm verify`'s row. The command that FIXES the state printed
// it before succeeding, and the edit that CREATES the contradiction said
// nothing until the next command.
//
// These drive the real command tree against a real games.yaml, so the
// load-time half (internal/app) and the command's own half are both in the
// count.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two contradictions' openings, as the warning words them.
const (
	valheimContradiction = `game "valheim" declares the BepInEx loader, but its adapter is "generic-files"`
	lethalContradiction  = `game "lethal-company" declares the BepInEx loader, but its adapter is "generic-files"`
)

// setupAdapterWarningGames writes three BepInEx games through a production
// Service and closes it, so each command opens its own:
//
//	valheim         declares the loader, adapter generic-files - a contradiction
//	lethal-company  the same contradiction, never the command's subject
//	riskofrain      adapter generic-files and no loader - nothing to say
func setupAdapterWarningGames(t *testing.T) {
	t.Helper()
	configDir = t.TempDir()
	dataDir = t.TempDir()
	oldGameID, oldJSON := gameID, jsonOutput
	gameID, jsonOutput = "", false
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		resetChangedFlags(rootCmd)
		gameID, jsonOutput = oldGameID, oldJSON
	})
	resetGameEditFlags(t)

	svc, err := app.Open(context.Background(), app.Options{ConfigDir: configDir, DataDir: dataDir, WarnWriter: &strings.Builder{}})
	require.NoError(t, err)
	for _, g := range []*domain.Game{
		{ID: "valheim", Name: "Valheim", Adapter: "generic-files", Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx}},
		{ID: "lethal-company", Name: "Lethal Company", Adapter: "generic-files", Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx}},
		{ID: "riskofrain", Name: "Risk of Rain 2", Adapter: "generic-files"},
	} {
		root := t.TempDir()
		g.InstallPath, g.ModPath, g.SourceIDs = root, root, map[string]string{"thunderstore": g.ID}
		require.NoError(t, svc.SaveGame(context.Background(), g))
		_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), g.ID)
		require.NoError(t, err)
	}
	require.NoError(t, svc.Close())
}

// resetChangedFlags clears every flag's Changed bit and value under cmd, so
// a flag one test passed through rootCmd does not read as passed in the
// next (cobra keeps both on the command objects, which are globals).
func resetChangedFlags(cmd *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if f.Changed {
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace(nil)
			} else {
				_ = f.Value.Set(f.DefValue)
			}
			f.Changed = false
		}
	}
	cmd.Flags().VisitAll(reset)
	cmd.PersistentFlags().VisitAll(reset)
	for _, c := range cmd.Commands() {
		resetChangedFlags(c)
	}
}

// runLMM runs one command line through the real tree and returns everything
// it wrote, both streams.
func runLMM(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	// A --config/--data some earlier test passed would otherwise be reset
	// over the sandbox this test just pointed them at.
	cfg, data := configDir, dataDir
	resetChangedFlags(rootCmd)
	configDir, dataDir = cfg, data
	rootCmd.SetArgs(args)
	stdout, stderr, err := captureStdoutAndStderr(t, func() error { return rootCmd.ExecuteContext(context.Background()) })
	require.NoError(t, err, "lmm %s\nstdout: %s\nstderr: %s", strings.Join(args, " "), stdout, stderr)
	return stdout, stderr
}

func TestAdapterWarning_ACommandThatReportsItSaysItOnce(t *testing.T) {
	for _, args := range [][]string{
		{"game", "show", "valheim"},
		{"game", "show", "valheim", "--json"},
		{"verify", "--game", "valheim"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			setupAdapterWarningGames(t)
			stdout, stderr := runLMM(t, args...)
			// Under --json the sentence is inside a JSON string.
			stdout = strings.ReplaceAll(stdout, `\"`, `"`)

			assert.Equal(t, 1, strings.Count(stdout+stderr, valheimContradiction),
				"the command reports valheim's warning itself, so the load-time copy is not printed too\nstdout: %s\nstderr: %s", stdout, stderr)
			assert.Equal(t, 1, strings.Count(stderr, lethalContradiction),
				"a game the command is not about is still warned about at load")
		})
	}
}

func TestAdapterWarning_TheEditThatFixesItSaysNothing(t *testing.T) {
	setupAdapterWarningGames(t)
	stdout, stderr := runLMM(t, "game", "edit", "valheim", "--adapter", "bepinex")

	assert.Contains(t, stdout, "adapter set to bepinex")
	assert.NotContains(t, stdout+stderr, valheimContradiction, "the fix must not print the warning it fixes")
}

func TestAdapterWarning_TheEditThatCreatesItSaysSoOnce(t *testing.T) {
	t.Run("--adapter generic-files on a declared game", func(t *testing.T) {
		setupAdapterWarningGames(t)
		runLMM(t, "game", "edit", "valheim", "--adapter", "")
		stdout, stderr := runLMM(t, "game", "edit", "valheim", "--adapter", "generic-files")

		assert.Contains(t, stdout, "adapter set to generic-files")
		assert.Equal(t, 1, strings.Count(stderr, valheimContradiction), "stderr: %s", stderr)
	})

	t.Run("--loader bepinex on an explicit generic-files game", func(t *testing.T) {
		setupAdapterWarningGames(t)
		stdout, stderr := runLMM(t, "game", "edit", "riskofrain", "--loader", "bepinex")

		assert.Contains(t, stdout, "declares the bepinex loader")
		assert.Equal(t, 1, strings.Count(stderr, `game "riskofrain" declares the BepInEx loader, but its adapter is "generic-files"`), "stderr: %s", stderr)
	})

	t.Run("under --json, on stderr only", func(t *testing.T) {
		setupAdapterWarningGames(t)
		stdout, stderr := runLMM(t, "game", "edit", "riskofrain", "--loader", "bepinex", "--json")

		assert.NotContains(t, stdout, "declares the BepInEx loader", "stdout stays one JSON document")
		assert.Equal(t, 1, strings.Count(stderr, `game "riskofrain" declares the BepInEx loader`), "stderr: %s", stderr)
	})

	t.Run("game add with a loader and an explicit generic-files adapter", func(t *testing.T) {
		setupAdapterWarningGames(t)
		install := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(install, "BepInEx"), 0o755))
		stdout, stderr := runLMM(t, "game", "add", "--source", "thunderstore", "--id", "valheim",
			"--game-id", "valheim2", "--name", "Valheim 2", "--path", install,
			"--adapter", "generic-files", "--loader", "bepinex")

		assert.Contains(t, stdout+stderr, "Added Valheim 2", "cobra's cmd.Printf writes to stderr")
		assert.Equal(t, 1, strings.Count(stderr, `game "valheim2" declares the BepInEx loader`), "stderr: %s", stderr)
	})
}

// TestDoGameShow_OffersToDeclareTheLoaderOnlyWhereItHelps: an undeclared
// BepInEx install gets "declare it with `lmm game edit <id> --loader
// bepinex`" (re-review R3) - but only on a game lmm lays BepInEx archives
// out for. On a game whose adapter is another one, following that advice
// CREATES the contradiction decision 11 warns about (#413 re-review M1:
// every remedy offered must silence, never cause, a warning).
func TestDoGameShow_OffersToDeclareTheLoaderOnlyWhereItHelps(t *testing.T) {
	svc := setupGameEditTest(t)
	app.RegisterAdapters(svc)
	const hint = "declare it with `lmm game edit"
	for id, g := range map[string]func(root string) *domain.Game{
		"derived": func(root string) *domain.Game {
			return &domain.Game{ID: "derived", Name: "Derived", InstallPath: root, ModPath: root}
		},
		"explicit": func(root string) *domain.Game {
			return &domain.Game{ID: "explicit", Name: "Explicit", InstallPath: root, ModPath: root, Adapter: "generic-files"}
		},
		"plugins-dir": func(root string) *domain.Game {
			return &domain.Game{ID: "plugins-dir", Name: "Plugins Dir", InstallPath: root, ModPath: filepath.Join(root, "BepInEx", "plugins")}
		},
	} {
		root := t.TempDir()
		preloader := filepath.Join(root, filepath.FromSlash(domain.BepInExPreloaderPath))
		require.NoError(t, os.MkdirAll(filepath.Dir(preloader), 0o755))
		require.NoError(t, os.WriteFile(preloader, []byte("preloader"), 0o644))
		game := g(root)
		game.SourceIDs = map[string]string{"nexusmods": id}
		require.NoError(t, svc.SaveGame(context.Background(), game))
	}

	out := captureStdout(t, func() error { return doGameShow(context.Background(), svc, "derived") })
	assert.Contains(t, out, hint, "lmm acts on this BepInEx, so declaring it is the next step")
	for _, id := range []string{"explicit", "plugins-dir"} {
		out := captureStdout(t, func() error { return doGameShow(context.Background(), svc, id) })
		assert.Contains(t, out, "Installed:    yes")
		assert.NotContains(t, out, hint, "%s: declaring the loader here would create a contradiction", id)
	}
}

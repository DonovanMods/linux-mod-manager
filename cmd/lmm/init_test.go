package main

// init_test.go covers `lmm init` (#351): the non-interactive refusal, and
// each of the four steps' skippable / re-runnable behaviour.
//
// The wizard reads from a *bufio.Reader the caller supplies, which is what
// makes it testable at all - doInit takes one rather than reaching for
// os.Stdin, the same seam readPromptLineFrom exists for.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupInitTest sandboxes the environment, resets the flags doInit reads,
// and returns a Service with no games configured.
func setupInitTest(t *testing.T) *core.Service {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("NEXUSMODS_API_KEY", "")
	t.Setenv("CURSEFORGE_API_KEY", "")

	configDir = t.TempDir()
	dataDir = t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	// The delegated flows (`lmm import`'s scan confirmation) read os.Stdin
	// directly rather than the wizard's own reader, so every test in this
	// file points it somewhere safe: without it a scripted run that
	// reaches the import prompt blocks on the real terminal.
	stdinFromString(t, "n\n")

	oldJSON, oldVerbose, oldColor, oldSkip, oldDry := jsonOutput, verbose, noColor, importSkipMatch, importDryRun
	jsonOutput, verbose, noColor, importSkipMatch, importDryRun = false, false, true, false, false
	t.Cleanup(func() {
		jsonOutput, verbose, noColor, importSkipMatch, importDryRun = oldJSON, oldVerbose, oldColor, oldSkip, oldDry
	})
	return svc
}

// runInitWith drives doInit with a scripted stdin and returns everything it
// printed.
func runInitWith(t *testing.T, svc *core.Service, input string) string {
	cmdOut, stdout := runInitStreams(t, svc, input)
	return cmdOut + stdout
}

// runInitStreams is runInitWith with the two streams kept apart, for the
// few assertions that are ABOUT a stream - the delegated auth flow's own
// output, which the wizard has to separate from the answer above it and
// indent under its step (#384).
func runInitStreams(t *testing.T, svc *core.Service, input string) (string, string) {
	t.Helper()
	cmd := &cobra.Command{}
	// The delegated import flow re-derives its context from the command
	// (runImportScan's own first line), which cobra populates during
	// Execute and a bare struct literal does not - so a test driving
	// doInit directly has to set it, or that flow hands database/sql a nil
	// Context and panics.
	cmd.SetContext(context.Background())
	var out strings.Builder
	cmd.SetOut(&out)
	// The wizard prints through both cmd.Print* (captured here) and the
	// fmt.Print* the flows it delegates to use, so the two are combined:
	// what a user sees is one stream, and a test that only read one of
	// them would assert about half a screen.
	stdout := captureStdout(t, func() error {
		return doInit(context.Background(), cmd, bufio.NewReader(strings.NewReader(input)), svc)
	})
	return out.String(), stdout
}

// TestInit_NonInteractiveRefusesAndPrintsTheEquivalentCommands is the
// Ruling-2 half: a scripted caller does not want a wizard, it wants the
// commands the wizard would have run.
func TestInit_NonInteractiveRefusesAndPrintsTheEquivalentCommands(t *testing.T) {
	require.NotEmpty(t, initEquivalentCommands)
	for _, want := range []string{"lmm game detect", "lmm game set-default", "lmm auth login", "lmm import"} {
		assert.True(t, strings.Contains(strings.Join(initEquivalentCommands, "\n"), want),
			"the refusal must name %q - the point is to be useful to a script, not merely to decline", want)
	}
}

// TestInit_SkippingTheScanLeavesNothingConfigured pins the skippable rule
// at the very first prompt, where declining must not read as a failure.
func TestInit_SkippingTheScanLeavesNothingConfigured(t *testing.T) {
	svc := setupInitTest(t)

	out := runInitWith(t, svc, "n\n")
	assert.Contains(t, out, "Step 1 of 4")
	assert.Contains(t, out, "Skipped.")
	assert.Contains(t, out, "lmm game detect")
	assert.Contains(t, out, "No games are configured yet")
	assert.Empty(t, svc.ListGames())
}

// TestInit_WithAConfiguredGameOffersOnlyWhatIsMissing is the
// re-runnability rule: an already-configured install must not be walked
// through adding a game it already has.
func TestInit_WithAConfiguredGameOffersOnlyWhatIsMissing(t *testing.T) {
	svc := setupInitTest(t)
	game := seedInitGame(t, svc)

	// n = don't scan, then the default-game prompt (one game, so a yes/no),
	// then no source login is offered (the fixture's source is not
	// auth-capable), then n = don't import.
	out := runInitWith(t, svc, "n\ny\nn\n")

	assert.Contains(t, out, "Already configured: Fixture Game (g1)")
	assert.Contains(t, out, "Use Fixture Game as the default game?")
	assert.Contains(t, out, "Default game set to g1")

	def, err := svc.DefaultGame(context.Background())
	require.NoError(t, err)
	assert.Equal(t, game.ID, def)
}

// TestInit_ADefaultGameAlreadySetIsLeftAlone: re-running the wizard must
// not silently retarget every later command.
func TestInit_ADefaultGameAlreadySetIsLeftAlone(t *testing.T) {
	svc := setupInitTest(t)
	seedInitGame(t, svc)
	require.NoError(t, svc.SetDefaultGame(context.Background(), "g1"))

	out := runInitWith(t, svc, "n\nn\n")
	assert.Contains(t, out, "Already set to g1")
	assert.NotContains(t, out, "Use Fixture Game as the default game?")
}

// TestInit_EndsWithNextStepsScopedToTheDefaultGame pins the closing
// readout, and the small thing that makes it useful: with a default game
// set the commands carry no --game, and without one they do.
func TestInit_EndsWithNextStepsScopedToTheDefaultGame(t *testing.T) {
	t.Run("with a default", func(t *testing.T) {
		svc := setupInitTest(t)
		seedInitGame(t, svc)
		require.NoError(t, svc.SetDefaultGame(context.Background(), "g1"))

		out := runInitWith(t, svc, "n\nn\n")
		assert.Contains(t, out, "lmm search <term>")
		assert.NotContains(t, out, "lmm search <term> --game <game-id>")
		assert.Contains(t, out, "lmm serve")
	})

	t.Run("without one", func(t *testing.T) {
		svc := setupInitTest(t)
		seedInitGame(t, svc)

		// n = don't scan, n = don't set a default, n = don't import.
		out := runInitWith(t, svc, "n\nn\nn\n")
		assert.Contains(t, out, "--game <game-id>")
	})
}

// TestInit_OffersTheImportScanForTheDefaultGame pins step 4's target
// choice: it scans the game the user will actually be working on.
func TestInit_OffersTheImportScanForTheDefaultGame(t *testing.T) {
	svc := setupInitTest(t)
	game := seedInitGame(t, svc)
	require.NoError(t, svc.SetDefaultGame(context.Background(), "g1"))

	// A mod already sitting in the game's mod directory, which the scan
	// should find and offer.
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "handmade.pak"), []byte("x"), 0o644))

	// n = don't scan Steam, y = scan mod_path, n = don't import what it found.
	out := runInitWith(t, svc, "n\ny\nn\n")
	assert.Contains(t, out, "Step 4 of 4")
	assert.Contains(t, out, "Scan Fixture Game for mods already installed?")
	assert.Contains(t, out, "untracked")
}

// TestInit_SkippingTheImportSaysHowToRunItLater keeps the "a skip is not a
// failure" promise on the last step too.
func TestInit_SkippingTheImportSaysHowToRunItLater(t *testing.T) {
	svc := setupInitTest(t)
	seedInitGame(t, svc)
	require.NoError(t, svc.SetDefaultGame(context.Background(), "g1"))

	out := runInitWith(t, svc, "n\nn\n")
	assert.Contains(t, out, "lmm import --game g1")
}

func TestInitChoice_ParsesAMenuAnswer(t *testing.T) {
	for _, tt := range []struct {
		line  string
		count int
		want  int
	}{
		{"1", 3, 1}, {"3", 3, 3}, {" 2 ", 3, 2},
		{"0", 3, 0}, {"4", 3, 0}, {"none", 3, 0}, {"", 3, 0}, {"abc", 3, 0},
	} {
		assert.Equalf(t, tt.want, initChoice(tt.line, tt.count), "line %q", tt.line)
	}
}

func TestAskInitYes_BareEnterTakesTheDefault(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(&strings.Builder{})

	assert.True(t, askInitYes(cmd, bufio.NewReader(strings.NewReader("\n")), "?", true))
	assert.False(t, askInitYes(cmd, bufio.NewReader(strings.NewReader("\n")), "?", false))
	assert.True(t, askInitYes(cmd, bufio.NewReader(strings.NewReader("y\n")), "?", false))
	assert.False(t, askInitYes(cmd, bufio.NewReader(strings.NewReader("n\n")), "?", true))
	// An unreadable answer is a NO whatever the default: the wizard must
	// not go on to touch a game directory on input it could not read.
	assert.False(t, askInitYes(cmd, bufio.NewReader(strings.NewReader("")), "?", true))
}

func TestGameSourceIDs_IsSortedAndDeduplicated(t *testing.T) {
	games := []*domain.Game{
		{ID: "a", SourceIDs: map[string]string{"nexusmods": "x", "curseforge": "y"}},
		{ID: "b", SourceIDs: map[string]string{"nexusmods": "z"}},
	}
	assert.Equal(t, []string{"curseforge", "nexusmods"}, gameSourceIDs(games))
}

// seedInitGame configures one game with a profile, the shape every step
// after the first needs.
func seedInitGame(t *testing.T, svc *core.Service) *domain.Game {
	t.Helper()
	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"src": ""},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	return game
}

// TestIsInitCancellation_CoversTheDelegatedFlowsDeclines is #351 review
// minor 9, and the correction to its premise: errors.Is is the primary test,
// and since #382 every delegated flow's decline IS the shared sentinel. The
// string fallback stays as a backstop for a flow that words its own decline;
// without it a user's "no" to the import step would read as a failure.
func TestIsInitCancellation_CoversTheDelegatedFlowsDeclines(t *testing.T) {
	assert.True(t, isInitCancellation(ErrCancelled), "the sentinel itself")
	assert.True(t, isInitCancellation(fmt.Errorf("running the step: %w", ErrCancelled)),
		"a wrapped sentinel - what errors.Is is for")
	assert.True(t, isInitCancellation(errors.New("import cancelled")),
		"a hand-worded decline, which the string backstop still covers")
	assert.True(t, isInitCancellation(context.Canceled), "and a real ctrl-c")

	assert.False(t, isInitCancellation(nil))
	assert.False(t, isInitCancellation(errors.New("disk full")),
		"an actual failure must still read as one")
}

// seedInitAuthGame is seedInitGame with an auth-capable source mapped to
// the game, so the wizard's step 3 has something to offer a login for.
func seedInitAuthGame(t *testing.T, svc *core.Service) *domain.Game {
	t.Helper()
	svc.RegisterSource(&mockAuthSource{id: "acme-mods", name: "Acme Mods"})
	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"acme-mods": ""},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	return game
}

// TestInit_EmptyAPIKeyIsASkipNotAnError is #384. The wizard's banner says
// "Every step can be skipped - press Enter to take the default"; a user who
// did exactly that at the source step collected
// "Nexus Mods: API key cannot be empty" per configured source. An empty key
// at the WIZARD's prompt is a skip, worded like every other skip in the
// run; "API key cannot be empty" belongs to `lmm auth login`, where the
// user asked for the prompt.
func TestInit_EmptyAPIKeyIsASkipNotAnError(t *testing.T) {
	svc := setupInitTest(t)
	seedInitAuthGame(t, svc)
	require.NoError(t, svc.SetDefaultGame(context.Background(), "g1"))
	// os.Stdin answers the delegated key prompt (a bare Enter), then the
	// import step's own confirmation.
	stdinFromString(t, "\nn\n")

	// n = don't scan Steam, y = sign in to Acme Mods, n = don't import.
	out := runInitWith(t, svc, "n\ny\nn\n")

	assert.NotContains(t, out, "API key cannot be empty")
	assert.Contains(t, out, "Skipped. ('lmm auth login acme-mods' when you want it.)")
	assert.False(t, svc.IsSourceAuthenticated(context.Background(), "acme-mods"),
		"a skip stores nothing")
}

// TestInit_AuthInstructionsAreIndentedAndOnTheirOwnLine covers #384's two
// secondary defects: the source's instruction block ran on from the [Y/n]
// answer with no newline between them, and it was the only text in the
// wizard without the two-space step indentation.
func TestInit_AuthInstructionsAreIndentedAndOnTheirOwnLine(t *testing.T) {
	svc := setupInitTest(t)
	seedInitAuthGame(t, svc)
	require.NoError(t, svc.SetDefaultGame(context.Background(), "g1"))
	stdinFromString(t, "\nn\n")

	_, delegated := runInitStreams(t, svc, "n\ny\nn\n")

	assert.True(t, strings.HasPrefix(delegated, "\n"),
		"the instruction block must start on its own line, not run on from the [Y/n] answer; got %q", delegated)
	assert.Contains(t, delegated, "  Enter the API key for acme-mods.\n")
	assert.Contains(t, delegated, "  Enter API key: ")
	assert.NotContains(t, delegated, "  \n", "a blank line inside the block keeps no trailing indent")
}

// TestInit_NextStepsBlockIsAligned is #389: the first four lines
// interpolated the --game scope and padded around it, while the `lmm serve`
// line carried hard-coded padding - so its description sat six columns out
// with a default game set, and ~17 columns further out without one.
func TestInit_NextStepsBlockIsAligned(t *testing.T) {
	for _, tc := range []struct {
		name       string
		setDefault bool
	}{
		{"with a default game", true},
		{"without one", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := setupInitTest(t)
			seedInitGame(t, svc)
			input := "n\nn\n"
			if tc.setDefault {
				require.NoError(t, svc.SetDefaultGame(context.Background(), "g1"))
			} else {
				input = "n\nn\nn\n" // one more: decline the default-game prompt
			}

			out := runInitWith(t, svc, input)

			_, block, ok := strings.Cut(out, "Done. What next:\n")
			require.True(t, ok, "the closing block is missing from:\n%s", out)

			// A run of two or more spaces separates the command from what
			// it does; the descriptions themselves are single-spaced, so
			// the LAST such run on a line is where the description starts.
			gap := regexp.MustCompile(` {2,}`)
			columns := map[int]bool{}
			for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
				require.True(t, strings.HasPrefix(line, "  lmm "), "unexpected line %q", line)
				runs := gap.FindAllStringIndex(line[2:], -1)
				require.NotEmpty(t, runs, "no gap between command and description in %q", line)
				columns[2+runs[len(runs)-1][1]] = true
			}
			require.Len(t, columns, 1, "every description must start in the same column, got:\n%s", block)
		})
	}
}

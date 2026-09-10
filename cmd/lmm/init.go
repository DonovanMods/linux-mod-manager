package main

// init.go is `lmm init` (#351) - the guided first run.
//
// The web UI has had a setup wizard since #333 (games -> sources/auth ->
// adopt); the CLI has had every piece of it and no path through them, so a
// new user had to discover the order from the README. This is that path,
// and it is ONLY a path: every step calls the same core flow the individual
// command calls, so there is no rule here that `lmm game detect`,
// `lmm game add`, `lmm auth login` and `lmm import` do not already own.
//
// Three properties it has to keep, and each shapes the code below:
//
//   - EVERY STEP IS SKIPPABLE. A user who wants three of the four steps
//     must not be forced through the fourth, and a "no" must never look
//     like a failure - it prints nothing alarming and moves on.
//   - IT IS RE-RUNNABLE. On an already-configured install it offers only
//     what is missing: a configured game is marked and not re-added, an
//     authenticated source is not asked for a key again, and a default
//     game that is already set is left alone.
//   - IT IS INTERACTIVE-ONLY. Under --json (or with no terminal to read)
//     it refuses with core.ErrInteractiveOnly and PRINTS THE EQUIVALENT
//     COMMANDS, because a scripted caller does not want a wizard - it
//     wants the five commands this wizard would have run
//     (initEquivalentCommands).
//
// One consequence of delegating rather than reimplementing is worth
// writing down: the wizard's OWN prompts read the *bufio.Reader it is
// handed, while the flows it delegates to (`lmm auth login`'s key prompt,
// `lmm import`'s confirmation) read os.Stdin themselves. On a terminal
// that is the same input, read a line at a time, so nothing is swallowed -
// which is exactly why this command refuses to run when stdin is not a
// terminal rather than trying to serve a pipe it could mis-split.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Set lmm up: find your games, add them, sign in, import what is there",
	Long: `Walk through setting lmm up, once.

Four steps, each of which you can skip:

  1. Scan your Steam libraries for moddable games and add the ones you
     pick, filling in their paths and sources from the install itself.
  2. Set a default game, so you can leave --game off from then on.
  3. Sign in to each mod source your games use, with that source's own
     instructions.
  4. Scan the game's mod directory for mods that are already there and
     bring them under management.

Re-running it is safe and does the sensible thing: a game that is already
configured is marked and not added again, a source that is already
authenticated is not asked for a key, and a default game that is already
set is left as it is. So it doubles as "add the game I just installed".

This command is interactive. Under --json, or with nothing to read from,
it prints the equivalent commands instead of prompting - a script wants
those, not a wizard.

Every step is a command of its own if you would rather run them yourself:
'lmm game detect', 'lmm game set-default', 'lmm auth login' and
'lmm import'.`,
	Args: cobra.NoArgs,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

// initEquivalentCommands is what a non-interactive caller is told to run
// instead. It is the whole wizard, in order, as commands - the point of
// the refusal is to be USEFUL to a script, not merely to decline.
var initEquivalentCommands = []string{
	"lmm game detect --include-unknown   # list what is installed",
	"lmm game detect --all               # add every curated game it found",
	"lmm game set-default <game-id>",
	"lmm auth login <source> --key-from-env",
	"lmm import --game <game-id>         # adopt mods already in mod_path",
}

func runInit(cmd *cobra.Command, args []string) error {
	// Ruling 2: --json never reads stdin. A wizard has nothing it could
	// emit as a single document either, so the refusal is the answer -
	// with the commands that DO work without a terminal.
	if jsonOutput || !term.IsTerminal(int(os.Stdin.Fd())) {
		printInitEquivalents(cmd)
		return fmt.Errorf("%w: run the commands above instead", core.ErrInteractiveOnly)
	}
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doInit(ctx, cmd, bufio.NewReader(os.Stdin), service)
	})
}

// printInitEquivalents prints the non-interactive path. It goes to STDERR:
// the command is failing, and a caller redirecting stdout to a file should
// not find advice in it.
func printInitEquivalents(cmd *cobra.Command) {
	fmt.Fprintln(os.Stderr, "lmm init is interactive. The same setup, as commands:")
	for _, line := range initEquivalentCommands {
		fmt.Fprintf(os.Stderr, "  %s\n", line)
	}
}

// doInit runs the four steps in order. Each returns the state the next one
// needs; none of them is fatal to the run - a step that fails says so and
// the wizard carries on, because a failed source login is no reason to
// abandon a user halfway through setting up their games.
func doInit(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service) error {
	cmd.Println("Setting up lmm. Every step can be skipped - press Enter to take the default.")

	games, err := initStepGames(ctx, cmd, reader, service)
	if err != nil {
		return err
	}
	if len(games) == 0 {
		// Nothing configured means nothing the remaining steps can act on -
		// a source login needs a game's source map, and an import needs a
		// mod_path. Say what to do instead of walking through three steps
		// that would all no-op.
		cmd.Println()
		cmd.Println("No games are configured yet, so there is nothing else to set up.")
		cmd.Println("Add one by hand with 'lmm game add', then run 'lmm init' again.")
		return nil
	}

	defaultGame := initStepDefaultGame(ctx, cmd, reader, service, games)
	initStepAuth(ctx, cmd, reader, service, games)
	initStepImport(ctx, cmd, reader, service, games, defaultGame)
	printInitNextSteps(cmd, defaultGame)
	return nil
}

// initStepGames runs the detect-and-add step and returns the games
// configured AFTERWARDS - which on a re-run may be the ones that were
// already there, with nothing added.
func initStepGames(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service) ([]*domain.Game, error) {
	cmd.Println()
	cmd.Println("Step 1 of 4: your games")

	existing := service.ListGames()
	if len(existing) > 0 {
		cmd.Printf("Already configured: %s\n", gameLabelList(existing))
	}

	if !askInitYes(cmd, reader, "Scan Steam for moddable games?", true) {
		cmd.Println("  Skipped. ('lmm game detect' when you want it.)")
		return existing, nil
	}

	cmd.Println("  Scanning Steam libraries...")
	// IncludeUnknown, always: a game lmm has no curated entry for is still
	// the game the user came here to mod, and #206's whole point is that
	// they should at least see it and its app id rather than be told
	// nothing was found.
	detected, warnings, err := app.DetectGames(ctx, service.ConfigDir(), app.DetectOptions{IncludeUnknown: true})
	if err != nil {
		// Not fatal: a user with no Steam install can still add a game by
		// hand, and this step is only one of four.
		cmd.Printf("  Could not scan Steam: %v\n", err)
		cmd.Println("  Add a game by hand with 'lmm game add'.")
		return existing, nil
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	known, unknown := splitDetectedGames(detected)
	if len(known) == 0 {
		printInitNoCuratedGames(cmd, unknown)
		return existing, nil
	}

	configured := configuredSlugs(existing)
	cmd.Printf("  Found %d moddable game(s):\n", len(known))
	for i, g := range known {
		marker := ""
		if configured[g.Slug] {
			marker = " [already configured]"
		}
		cmd.Printf("    %d. %s (%s)%s\n", i+1, g.Name, g.Slug, marker)
		cmd.Printf("        %s\n", g.InstallPath)
	}
	if len(unknown) > 0 {
		cmd.Printf("  %d more installed game(s) have no built-in configuration.\n", len(unknown))
		cmd.Println("  Add one with 'lmm game add --from-detected <steam-app-id>' - see 'lmm game detect --include-unknown'.")
	}

	cmd.Printf("  Add which? [1-%d/all/none]: ", len(known))
	line, err := readPromptLineFrom(reader)
	if err != nil {
		return existing, err
	}
	// Reuses `game detect`'s own selection parser, which is also what
	// filters out the games already configured - so "all" on a re-run adds
	// only the new ones rather than failing on a duplicate.
	indices, err := gameDetectSelectionIndices(line, known, configuredGameMap(existing))
	if err != nil {
		cmd.Printf("  %v - skipping this step.\n", err)
		return existing, nil
	}
	if len(indices) == 0 {
		cmd.Println("  Nothing to add.")
		return existing, nil
	}

	selected := make([]domain.DetectedGame, len(indices))
	for i, n := range indices {
		selected[i] = known[n-1]
	}
	result, applyErr := service.ApplyGameDetect(ctx, selected)
	for i := range result.Profiles {
		cmd.Printf("  Added %s (%s)\n", selected[i].Name, selected[i].Slug)
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}
	if applyErr != nil {
		// Partial success is the honest report: result.Profiles named
		// exactly what did land, and the games that did not are still
		// addable by hand.
		cmd.Printf("  Stopped adding games: %v\n", applyErr)
	}

	// Re-read rather than appending to `existing`: ApplyGameDetect is what
	// wrote games.yaml, and the Service's own view is the one the
	// remaining steps will use.
	if reloaded, err := service.LoadGamesFromDisk(); err == nil {
		return gamesFromMap(reloaded), nil
	}
	return service.ListGames(), nil
}

// printInitNoCuratedGames handles the "nothing curated was found" case
// without dead-ending the user - the same rule #206's Important 3 settled
// for `game detect`.
func printInitNoCuratedGames(cmd *cobra.Command, unknown []domain.DetectedGame) {
	if len(unknown) == 0 {
		cmd.Println("  No moddable Steam games found.")
		cmd.Println("  Add one by hand with 'lmm game add'.")
		return
	}
	cmd.Printf("  Found %d installed game(s), none with a built-in configuration:\n", len(unknown))
	for _, g := range unknown {
		cmd.Printf("    %s (app id %s)\n", g.Name, g.SteamAppID)
	}
	cmd.Println("  Add one with 'lmm game add --from-detected <steam-app-id>'.")
}

// initStepDefaultGame offers to set the default game, and returns the
// default in force afterwards (empty when there is none). A default
// already set is left alone - re-running the wizard must not silently
// retarget every later command.
func initStepDefaultGame(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service, games []*domain.Game) string {
	cmd.Println()
	cmd.Println("Step 2 of 4: your default game")

	current, err := service.DefaultGame(ctx)
	if err != nil {
		cmd.Printf("  Could not read the current default: %v\n", err)
		return ""
	}
	if current != "" {
		cmd.Printf("  Already set to %s. ('lmm game set-default' to change it.)\n", current)
		return current
	}
	if len(games) == 1 {
		only := games[0]
		if !askInitYes(cmd, reader, fmt.Sprintf("Use %s as the default game?", only.Name), true) {
			cmd.Println("  Skipped. Pass --game to every command, or set it later.")
			return ""
		}
		if err := service.SetDefaultGame(ctx, only.ID); err != nil {
			cmd.Printf("  Could not set the default: %v\n", err)
			return ""
		}
		cmd.Printf("  Default game set to %s.\n", only.ID)
		return only.ID
	}

	cmd.Println("  Configured games:")
	for i, g := range games {
		cmd.Printf("    %d. %s (%s)\n", i+1, g.Name, g.ID)
	}
	cmd.Printf("  Which should be the default? [1-%d/none]: ", len(games))
	line, err := readPromptLineFrom(reader)
	if err != nil || line == "" || line == "none" || line == "n" {
		cmd.Println("  Skipped. Pass --game to every command, or set it later.")
		return ""
	}
	choice := initChoice(line, len(games))
	if choice == 0 {
		cmd.Printf("  %q is not one of the choices - skipping.\n", line)
		return ""
	}
	if err := service.SetDefaultGame(ctx, games[choice-1].ID); err != nil {
		cmd.Printf("  Could not set the default: %v\n", err)
		return ""
	}
	cmd.Printf("  Default game set to %s.\n", games[choice-1].ID)
	return games[choice-1].ID
}

// initStepAuth offers a login for each source the configured games
// actually MAP TO and that is not authenticated yet.
//
// Scoped to the games' own sources deliberately: offering every registered
// auth-capable source would ask a Skyrim-only user for a CurseForge key
// they have no use for, which is exactly the noise that makes people stop
// reading prompts.
func initStepAuth(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service, games []*domain.Game) {
	cmd.Println()
	cmd.Println("Step 3 of 4: your mod sources")

	report, err := app.AuthStatus(ctx, service)
	if err != nil {
		cmd.Printf("  Could not read the credential store: %v\n", err)
		return
	}
	authenticated := make(map[string]bool, len(report.Sources))
	capable := make(map[string]string, len(report.Sources))
	for _, row := range report.Sources {
		authenticated[row.ID] = row.Authenticated
		capable[row.ID] = row.Name
	}

	var needed []string
	for _, id := range gameSourceIDs(games) {
		if _, ok := capable[id]; !ok {
			continue // not an auth-capable source; nothing to log in to
		}
		if authenticated[id] {
			cmd.Printf("  %s: already authenticated.\n", capable[id])
			continue
		}
		needed = append(needed, id)
	}
	if len(needed) == 0 {
		cmd.Println("  Nothing to sign in to.")
		return
	}

	for _, id := range needed {
		if !askInitYes(cmd, reader, fmt.Sprintf("Sign in to %s now?", capable[id]), true) {
			cmd.Printf("  Skipped. ('lmm auth login %s' when you want it.)\n", id)
			continue
		}
		// The answer above was read without a newline of its own, and the
		// instruction block that follows is the delegated flow's, printed
		// on os.Stdout - so the separator goes to that same stream, or it
		// would land somewhere else entirely in a redirect (#384).
		fmt.Println()
		// doAuthLoginIndented is `lmm auth login <source>` itself: the
		// source's own instructions, the key prompt, the live validation
		// and the store, all unchanged bar the wizard's own two-space
		// indent. A failure here is reported and the loop continues - a
		// mistyped key must not end the wizard.
		err := doAuthLoginIndented(ctx, service, id, "  ")
		switch {
		case err == nil:
		case errors.Is(err, errAPIKeyEmpty):
			// The banner promised every step could be skipped by pressing
			// Enter; at this one it used to answer with an error per
			// configured source (#384). A bare Enter is a skip, worded
			// like every other skip in the run.
			cmd.Printf("  Skipped. ('lmm auth login %s' when you want it.)\n", id)
		default:
			cmd.Printf("  %s: %v\n", capable[id], err)
			cmd.Printf("  ('lmm auth login %s' to try again.)\n", id)
		}
	}
}

// initStepImport offers the mod_path scan for one game - the default game
// when there is one, otherwise the single configured game, otherwise it
// asks which.
//
// It runs runImportScan, which IS `lmm import`'s scan mode: the same plan,
// the same confirmation, the same readout. Nothing about adoption is
// re-implemented here.
func initStepImport(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service, games []*domain.Game, defaultGame string) {
	cmd.Println()
	cmd.Println("Step 4 of 4: mods you already have")

	game := initPickImportGame(cmd, reader, games, defaultGame)
	if game == nil {
		cmd.Println("  Skipped. ('lmm import --game <game-id>' when you want it.)")
		return
	}
	cmd.Printf("  This scans %s for mods lmm does not know about yet.\n", game.ModPath)
	if !askInitYes(cmd, reader, fmt.Sprintf("Scan %s for mods already installed?", game.Name), true) {
		cmd.Printf("  Skipped. ('lmm import --game %s' when you want it.)\n", game.ID)
		return
	}

	profileName, err := resolveProfile(ctx, service, game.ID, "")
	if err != nil {
		cmd.Printf("  Could not resolve a profile for %s: %v\n", game.Name, err)
		return
	}
	// The scan flow is `lmm import`'s own, verbatim - including its
	// confirmation prompt, which is why a "no" there needs no handling
	// here beyond not treating a cancellation as a wizard failure.
	if err := runImportScan(cmd, game, service, profileName); err != nil {
		if isInitCancellation(err) {
			cmd.Println("  Nothing imported.")
			return
		}
		cmd.Printf("  Import scan failed: %v\n", err)
	}
}

// initPickImportGame decides which game step 4 offers to scan.
func initPickImportGame(cmd *cobra.Command, reader *bufio.Reader, games []*domain.Game, defaultGame string) *domain.Game {
	if defaultGame != "" {
		for _, g := range games {
			if g.ID == defaultGame {
				return g
			}
		}
	}
	if len(games) == 1 {
		return games[0]
	}
	cmd.Println("  Configured games:")
	for i, g := range games {
		cmd.Printf("    %d. %s (%s)\n", i+1, g.Name, g.ID)
	}
	cmd.Printf("  Scan which? [1-%d/none]: ", len(games))
	line, err := readPromptLineFrom(reader)
	if err != nil {
		return nil
	}
	if choice := initChoice(line, len(games)); choice != 0 {
		return games[choice-1]
	}
	return nil
}

// printInitNextSteps closes the wizard with the three things a set-up user
// does next, written with their own game id where there is one.
func printInitNextSteps(cmd *cobra.Command, defaultGame string) {
	scope := ""
	if defaultGame == "" {
		scope = " --game <game-id>"
	}
	// Built as pairs and padded to the widest command, rather than with
	// per-line padding: the first four carry `scope` and the last does not,
	// so any hard-coded spacing is wrong for at least one of the two
	// layouts, and was (#389).
	next := []struct{ command, does string }{
		{"lmm search <term>" + scope, "find a mod"},
		{"lmm install <mod-id>" + scope, "install one"},
		{"lmm deploy" + scope, "put your mods in the game directory"},
		{"lmm snapshot create" + scope, "record a point you can come back to"},
		{"lmm serve", "the same thing in a browser"},
	}
	width := 0
	for _, n := range next {
		if len(n.command) > width {
			width = len(n.command)
		}
	}

	cmd.Println()
	cmd.Println("Done. What next:")
	for _, n := range next {
		cmd.Printf("  %-*s   %s\n", width, n.command, n.does)
	}
}

// askInitYes prints a yes/no prompt and reads the answer, with def as the
// answer for a bare Enter.
//
// It reads through readInitLine rather than readPromptLineFrom, for one
// reason: that helper treats io.EOF as an empty line (deliberately - "Ctrl
// -D and piped input both legitimately end the line"), which for a yes/no
// whose default is YES would turn Ctrl-D into consent to every remaining
// step. Here the end of input means STOP, so an EOF is a no whatever the
// default is - the wizard must not carry on doing things to a game
// directory on the strength of input that is not there.
func askInitYes(cmd *cobra.Command, reader *bufio.Reader, question string, def bool) bool {
	suffix := "[y/N]"
	if def {
		suffix = "[Y/n]"
	}
	cmd.Printf("  %s %s ", question, suffix)
	line, ok := readInitLine(reader)
	if !ok {
		return false
	}
	switch line {
	case "":
		return def
	case "y", "yes":
		return true
	default:
		return false
	}
}

// readInitLine reads one trim-spaced, lower-cased answer. ok is false when
// the input ENDED (io.EOF with nothing on the line) or could not be read at
// all - the two cases the wizard treats as "stop", distinct from the empty
// line a bare Enter produces.
func readInitLine(reader *bufio.Reader) (string, bool) {
	line, err := reader.ReadString('\n')
	trimmed := strings.ToLower(strings.TrimSpace(line))
	if err != nil {
		// A final line with no trailing newline is still an answer; an
		// error with nothing before it is the end of input.
		return trimmed, trimmed != ""
	}
	return trimmed, true
}

// initChoice parses a 1-based menu answer, returning 0 for anything that
// is not one of the choices (including "none" and an empty line).
func initChoice(line string, count int) int {
	choice := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &choice); err != nil {
		return 0
	}
	if choice < 1 || choice > count {
		return 0
	}
	return choice
}

// isInitCancellation reports whether err is a user declining a prompt
// rather than something going wrong - the one error the wizard reports as
// a plain "nothing happened".
func isInitCancellation(err error) bool {
	if errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled) {
		return true
	}
	// The string test is NOT redundant, and it is not about wrapping -
	// errors.Is covers that. Review minor 9 read it that way; what it
	// actually covers is that runImportScan's declined confirm returns a
	// bare fmt.Errorf("import cancelled") rather than the shared sentinel,
	// which import_characterize_test.go pins deliberately ("a future lift
	// decision to unify onto ErrCancelled is a deliberate, visible
	// change"). That flow is one of the two this wizard delegates to, so
	// dropping the fallback would turn a user's "no" into
	// "Import scan failed: import cancelled".
	return err != nil && strings.Contains(err.Error(), ErrCancelled.Error())
}

// gameLabelList renders configured games as "Name (id), Name (id)".
func gameLabelList(games []*domain.Game) string {
	labels := make([]string, len(games))
	for i, g := range games {
		labels[i] = fmt.Sprintf("%s (%s)", g.Name, g.ID)
	}
	return strings.Join(labels, ", ")
}

// configuredSlugs indexes the configured games by id, for the "[already
// configured]" marker in step 1's listing.
func configuredSlugs(games []*domain.Game) map[string]bool {
	out := make(map[string]bool, len(games))
	for _, g := range games {
		out[g.ID] = true
	}
	return out
}

// configuredGameMap rebuilds the map gameDetectSelectionIndices takes -
// the existing games keyed by id, which is what makes "all" on a re-run
// add only the games that are new.
func configuredGameMap(games []*domain.Game) map[string]*domain.Game {
	out := make(map[string]*domain.Game, len(games))
	for _, g := range games {
		out[g.ID] = g
	}
	return out
}

// gamesFromMap flattens a games.yaml map into the stable, id-sorted slice
// every menu in this file numbers. Sorted because a map's iteration order
// would renumber the menu between two prompts in the same run - the same
// reason `game add`'s source menu sorts its own list.
func gamesFromMap(games map[string]*domain.Game) []*domain.Game {
	out := make([]*domain.Game, 0, len(games))
	for _, g := range games {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// gameSourceIDs is every source id the configured games map to, sorted and
// deduplicated - step 3's candidate list.
func gameSourceIDs(games []*domain.Game) []string {
	seen := map[string]bool{}
	var ids []string
	for _, g := range games {
		for id := range g.SourceIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	return ids
}

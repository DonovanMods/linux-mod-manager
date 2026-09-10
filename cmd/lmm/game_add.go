package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/spf13/cobra"
)

var gameAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a game",
	Long: `Add a new game configuration.

With no flags this prompts for a mod source (every registered source,
built-in or custom, sorted by ID), then the game's identity and its
install path and mod path (defaulting to the install path plus "/mods"),
and saves the result to games.yaml along with an empty default profile.

Every prompt also has a flag, so the whole command runs non-interactively
- and therefore under --json (#307). A flag always wins; only the values
no flag supplied are prompted for, and under --json a missing value is an
error naming the flag rather than a prompt.

If the game is already installed via Steam, --from-detected with the
Steam app id prefills nearly everything from the install itself (#206):
the display name, the install path, the local game id, and either the
curated mod path and source mapping (for a game in lmm's known-games
list) or a default mod path of the install path plus "/mods" for one
that is not. List the app ids with 'lmm game detect --include-unknown'.
Every other flag still wins over the prefill, so
--mod-path/--game-id/--name correct a guess; for a game with no curated
sources, name one with --source, and either give --id or let the source's
catalog be searched by the game's own name (--pick chooses among the
matches; a single match whose name is exactly the game's name is taken
automatically).

For a game lmm ALREADY has curated sources for, naming --source (with
--id, --query or --pick) ADDS a mapping to the curated map rather than
replacing it, so detection's own sources are never discarded - use 'lmm
game edit --remove-source' to drop one afterwards. --id/--query/--pick
alone, with no --source, are refused: they need --source to say which
source the identifier belongs to.

Sources with a searchable game catalog (CurseForge today; any future
source that implements one) accept --query to search it, with --pick to
choose from the matches by number. Without --pick the matches are simply
printed (as the catalog document under --json), so a caller can search
first and choose second. All other registered sources (NexusMods today;
any custom source without a catalog) take the game's identifier with that
source directly via --id - for NexusMods, the slug from its URL (e.g.
https://www.nexusmods.com/skyrimspecialedition -> skyrimspecialedition).
A source that has no identifier to give - a directory source ignores the
mapped value entirely - may be left empty: press Enter at the prompt, or
pass --id "", and give --game-id so the entry still has a key. Only such a
source is offered that: NexusMods and Steam Workshop have no catalog
either, but their mapped value is a real game slug/appid and is required.

The LOCAL games.yaml key defaults to a slug derived from the catalog
match (the catalog path) or from --id (the manual path); --game-id sets
it explicitly on either path, e.g. to avoid a collision with an existing
game.

The install path must already exist. The mod path is not created here;
deploy creates it on demand. Either path may use "~", and the mod path may
be given relative to the install path - "Data" means "<install path>/Data",
exactly as it does in a hand-written games.yaml - with the resolved
absolute path being what lmm records.

Examples:
  lmm game add
  # Select source, then search/enter game details

  lmm game add --source nexusmods --id skyrimspecialedition \
    --name "Skyrim Special Edition" --path ~/games/skyrim

  lmm game add --source curseforge --query minecraft --json
  # Prints the matches; re-run with --pick to choose one
  lmm game add --source curseforge --query minecraft --pick 1 \
    --path ~/games/minecraft

  lmm game detect --include-unknown          # list installed app ids
  lmm game add --from-detected 526870 --source curseforge
  # Searches CurseForge for "Satisfactory" and prints the matches
  lmm game add --from-detected 526870 --source curseforge --pick 1`,
	Args: cobra.NoArgs,
	RunE: runGameAdd,
}

var (
	gameAddSource       string
	gameAddID           string
	gameAddQuery        string
	gameAddPick         int
	gameAddName         string
	gameAddGameID       string
	gameAddPath         string
	gameAddModPath      string
	gameAddAdapter      string
	gameAddFromDetected string
	// #359's loader declaration. --loader alone is enough (a game whose
	// runtime and bootstrap lmm can read off the install directory needs
	// nothing more); the other three refine it.
	gameAddLoader          string
	gameAddLoaderVersion   string
	gameAddLoaderRuntime   string
	gameAddLoaderBootstrap string
)

func init() {
	gameCmd.AddCommand(gameAddCmd)

	gameAddCmd.Flags().StringVar(&gameAddSource, "source", "", "mod source ID (skips the source prompt); with --from-detected on a curated game, adds a mapping rather than replacing the curated map")
	gameAddCmd.Flags().StringVar(&gameAddID, "id", "", "the game's identifier with that source (NexusMods slug, CurseForge game ID, ...)")
	gameAddCmd.Flags().StringVar(&gameAddQuery, "query", "", "search the source's game catalog instead of naming an identifier")
	gameAddCmd.Flags().IntVar(&gameAddPick, "pick", 0, "1-based choice among --query's matches (without it the matches are printed)")
	gameAddCmd.Flags().StringVar(&gameAddName, "name", "", "display name (defaults to the catalog match's name)")
	gameAddCmd.Flags().StringVar(&gameAddGameID, "game-id", "", "the LOCAL games.yaml key (default: derived from the catalog match's slug, or from --id)")
	gameAddCmd.Flags().StringVar(&gameAddPath, "path", "", "game install path (must exist)")
	gameAddCmd.Flags().StringVar(&gameAddModPath, "mod-path", "", "mod directory, absolute or relative to the install path (default: <install path>/mods)")
	gameAddCmd.Flags().StringVar(&gameAddAdapter, "adapter", "", "game adapter (default: generic-files; see `lmm game list` for each game's)")
	gameAddCmd.Flags().StringVar(&gameAddLoader, "loader", "",
		"declare a mod loader installed in the game directory (today: bepinex) - see 'lmm game show' for the launch option it needs")
	gameAddCmd.Flags().StringVar(&gameAddLoaderVersion, "loader-version", "",
		"the loader version installed in the game directory, e.g. 5.4.23.5 (checked by 'lmm verify')")
	gameAddCmd.Flags().StringVar(&gameAddLoaderRuntime, "loader-runtime", "",
		"the game's Unity scripting backend: mono or il2cpp (default: read from the install directory)")
	gameAddCmd.Flags().StringVar(&gameAddLoaderBootstrap, "loader-bootstrap", "",
		"how the loader is injected: native or proton (default: read from the install directory)")
	gameAddCmd.Flags().StringVar(&gameAddFromDetected, "from-detected", "",
		"Steam app id of an installed game to prefill from (list them with 'lmm game detect --include-unknown')")
	gameAddCmd.MarkFlagsMutuallyExclusive("id", "query")
}

// runGameAdd opens a service and runs the add flow. Unlike the pre-#307
// version it does NOT reject --json up front: every prompt now has a flag,
// so a fully-flagged invocation is non-interactive and emits the
// core.GameListEntry document. Only a value that no flag supplied - and
// that would therefore have to be read from stdin - refuses, with
// core.ErrInteractiveOnly naming the flag.
func runGameAdd(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		reader := bufio.NewReader(os.Stdin)
		return doGameAdd(ctx, cmd, reader, service)
	})
}

// doGameAdd resolves the source, then the game's identity (catalog search
// or a direct identifier), then its paths, and hands one core.GameSpec to
// core.Service.AddGame. Every decision it used to make itself - the
// catalog filter, the slug derivation, the install/mods default, the
// games.yaml write and the default profile - now lives in core, shared
// with `lmm serve` (#307).
func doGameAdd(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service) error {
	// --game-id sets the LOCAL games.yaml key on either path (#333 Minor
	// #4 - the manual path had no flag for it at all, a one-way parity
	// hole against POST /api/v1/games' game_id member). The catalog path
	// below still derives its own default from the match's slug when this
	// is empty; core.GameSpec.game() derives one from the identifier when
	// BOTH are empty.
	spec := core.GameSpec{
		Name: gameAddName, ID: gameAddGameID,
		InstallPath: gameAddPath, ModPath: gameAddModPath,
		Adapter: gameAddAdapter,
	}

	// #353: validated against the registry HERE, before any prompt runs,
	// so a typo is a one-line refusal naming the real names rather than a
	// failure after the whole walk-through. core re-checks it inside the
	// write's gate; this is the frontend courtesy, not the rule.
	if err := validateAdapterFlag(service, gameAddAdapter); err != nil {
		return err
	}

	// #359: the loader declaration, if any. Validated by core (which owns
	// the vocabulary and names the wire field a rejection is about); the
	// CLI only rejects the detail flags with no --loader to attach them to.
	loader, err := loaderSpecFromFlags(gameAddLoader, gameAddLoaderVersion, gameAddLoaderRuntime, gameAddLoaderBootstrap)
	if err != nil {
		return err
	}
	spec.Loader = loader

	// --from-detected prefills the spec from an installed Steam game
	// before any flag is consulted (#206). core.PrefillGameSpecFromDetected
	// is the whole rule - the CLI derives no slug, no mod path and no
	// source map of its own - and every flag above already won, field by
	// field. It is the Service method rather than the pure
	// GameSpecFromDetected because a candidate whose install path is
	// already a configured game keeps THAT game's id (#406 review F1), so
	// this refuses the duplicate by name instead of writing a second game
	// over the same directory.
	var candidate *domain.DetectedGame
	if gameAddFromDetected != "" {
		c, err := detectedGameCandidate(ctx, cmd, service)
		if err != nil {
			return err
		}
		candidate = &c
		if spec, err = service.PrefillGameSpecFromDetected(c, spec); err != nil {
			return err
		}
	}

	// A prefilled candidate that already carries a source map needs no
	// source named: that IS the curated mapping `lmm game detect` writes.
	// Anything else - a bare add, an unknown candidate, or an explicit
	// --source layered onto a curated one - resolves a source first.
	if candidate == nil || gameAddSource != "" || len(spec.Sources) == 0 {
		selected, err := resolveGameAddSource(cmd, reader, service)
		if err != nil {
			return err
		}
		spec.SourceID = selected.ID()
		done, err := resolveGameAddIdentity(ctx, cmd, reader, service, selected, candidate, &spec)
		if err != nil || done {
			// done: the catalog path already produced the command's whole
			// output (the matches, or "nothing matched") - search first,
			// pick second.
			return err
		}
	} else if gameAddID != "" || gameAddQuery != "" || gameAddPick != 0 {
		// The skip above takes the curated source map as-is and never reads
		// --id/--query/--pick, so silently taking this branch would discard
		// a value the user explicitly typed (#206 review, Important 3).
		// --source is what tells resolveGameAddIdentity which of the
		// curated map's entries the identifier names.
		return &core.GameSpecError{
			Field:  "source_id",
			Reason: "--id/--query/--pick need --source when the detected game is curated",
		}
	}

	if spec.Name == "" {
		return missingGameAddValue(cmd, reader, "Game name (display): ", "--name", &spec.Name)
	}
	if err := resolveGameAddPaths(cmd, reader, &spec); err != nil {
		return err
	}

	entry, err := service.AddGame(ctx, spec)
	if err != nil {
		return err
	}
	return reportGameAdded(cmd, entry)
}

// resolveGameAddIdentity fills spec.Identifier for the chosen source,
// either from the source's catalog or directly. candidate is nil for a
// bare `game add`; when it is set, the catalog is searched by the DETECTED
// game's own name (or --query, when the user would rather search for
// something else) and a single match carrying exactly that name is taken
// without asking (#206).
func resolveGameAddIdentity(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service, selected source.ModSource, candidate *domain.DetectedGame, spec *core.GameSpec) (done bool, err error) {
	_, hasCatalog := selected.(source.GameCatalog)
	query, autoPickName := gameAddQuery, ""
	if candidate != nil {
		autoPickName = candidate.Name
		if query == "" {
			query = candidate.Name
		}
	}

	switch {
	case gameAddQuery != "" || (gameAddID == "" && hasCatalog):
		return resolveGameAddFromCatalog(ctx, cmd, reader, service, selected, spec, query, autoPickName)
	default:
		return false, resolveGameAddManual(cmd, reader, selected, spec)
	}
}

// detectedGameCandidate resolves --from-detected against a live Steam scan
// that INCLUDES unknown games - the whole point of the flag is the games
// no known-games entry covers. The scan's own warnings go to stderr, never
// into stdout's one document (Ruling 15); the add's document is the
// core.GameListEntry row.
func detectedGameCandidate(ctx context.Context, cmd *cobra.Command, service *core.Service) (domain.DetectedGame, error) {
	if !jsonOutput {
		cmd.Println("Scanning Steam libraries...")
	}
	// The service's own logger, not a second one built from the same
	// --log-level (#368 review nit 10): app.Open was handed exactly this
	// logger. `lmm game detect` has to build one because its scan runs
	// BEFORE the service is opened; this scan does not.
	detected, warnings, err := app.DetectGames(ctx, service.ConfigDir(),
		app.DetectOptions{IncludeUnknown: true, Logger: service.Logger()})
	if err != nil {
		return domain.DetectedGame{}, fmt.Errorf("detecting games: %w", err)
	}
	if !jsonOutput {
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
	}
	return core.FindDetectedGame(detected, gameAddFromDetected)
}

// resolveGameAddSource resolves the mod source: --source when given
// (validated against the registry), otherwise the numbered menu over every
// registered source sorted by ID - svc.ListSources() carries no ordering
// guarantee, so the sort is what makes the menu stable.
func resolveGameAddSource(cmd *cobra.Command, reader *bufio.Reader, service *core.Service) (source.ModSource, error) {
	sources := service.ListSources()
	if len(sources) == 0 {
		return nil, fmt.Errorf("no mod sources are registered")
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID() < sources[j].ID() })

	if gameAddSource != "" {
		src, err := service.GetSource(gameAddSource)
		if err != nil {
			return nil, fmt.Errorf("unknown source %q (registered: %s)", gameAddSource, sourceIDList(sources))
		}
		return src, nil
	}
	// Built once and used by BOTH the --json refusal and the closed-stdin
	// one below: a piped run has no answer coming either way, so the two
	// must say the same thing, and sharing the value is what makes that
	// true rather than intended (#385, P1b review F10). Same shape at
	// every prompt in this file.
	noSource := interactiveOnlyVia(fmt.Sprintf("pass --source (registered: %s)", sourceIDList(sources)))
	if jsonOutput {
		return nil, noSource
	}

	cmd.Println("Select a mod source:")
	for i, src := range sources {
		cmd.Printf("  [%d] %s (%s)\n", i+1, src.Name(), src.ID())
	}
	cmd.Printf("Enter choice (1-%d): ", len(sources))

	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, promptReadErrorAs(err, noSource)
	}
	choice, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || choice < 1 || choice > len(sources) {
		return nil, fmt.Errorf("invalid choice: %s", strings.TrimSpace(line))
	}
	return sources[choice-1], nil
}

// sourceIDList renders registered source IDs for an error hint.
func sourceIDList(sources []source.ModSource) string {
	ids := make([]string, len(sources))
	for i, src := range sources {
		ids[i] = src.ID()
	}
	return strings.Join(ids, ", ")
}

// resolveGameAddFromCatalog drives the catalog path: search via
// core.Service.SearchGameCatalog, then pick a match by --pick or a prompt.
// done=true means the command has already produced its whole output - the
// no-matches case, and the "printed the matches, no --pick" case, which is
// how a non-interactive caller searches first and chooses second.
func resolveGameAddFromCatalog(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service, selected source.ModSource, spec *core.GameSpec, query, autoPickName string) (done bool, err error) {
	if query == "" {
		noQuery := interactiveOnlyVia(fmt.Sprintf("pass --query (to search %s's catalog) or --id", selected.ID()))
		if jsonOutput {
			return false, noQuery
		}
		cmd.Print("\nSearch for a game: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return false, promptReadErrorAs(err, noQuery)
		}
		query = strings.TrimSpace(line)
	}

	if !jsonOutput {
		cmd.Printf("Searching %s...\n", selected.Name())
	}
	report, err := service.SearchGameCatalog(ctx, selected.ID(), query)
	if err != nil {
		// Matches the convention at search.go/install.go/update.go's sibling
		// sites: a source signals a missing/invalid key by wrapping
		// domain.ErrAuthRequired, and the caller rewrites it into the
		// friendly "run lmm auth login <id>" prompt instead of surfacing the
		// raw API error.
		if errors.Is(err, domain.ErrAuthRequired) {
			return false, authPromptError(selected.ID())
		}
		return false, err
	}

	if len(report.Matches) == 0 {
		if jsonOutput {
			return true, emitJSON(report)
		}
		cmd.Printf("No games found matching %q\n", query)
		if autoPickName != "" {
			cmd.Printf("Pass --id with this game's identifier with %s instead.\n", selected.Name())
		}
		return true, nil
	}

	pick := gameAddPick
	// #206's suggestion path resolves itself only when the catalog answers
	// with exactly one entry carrying the detected game's own name - never
	// on "the only match", which for "Hades" can still be "Hades II".
	if pick == 0 && autoPickName != "" {
		if m := core.ExactGameCatalogMatch(report, autoPickName); m != nil {
			applyGameCatalogMatch(cmd, *m, spec)
			return false, nil
		}
	}
	if pick == 0 {
		if jsonOutput {
			// The document IS the answer: search first, then re-run with
			// --pick. Not an error - the caller asked what matched.
			return true, emitJSON(report)
		}
		cmd.Printf("Found %d game(s):\n", len(report.Matches))
		for i, m := range report.Matches {
			cmd.Printf("  [%d] %s (%s id: %s)\n", i+1, m.Name, report.SourceID, m.Identifier)
		}
		cmd.Print("Select a game (number): ")
		line, err := reader.ReadString('\n')
		if err != nil {
			// --json answers this prompt by EMITTING the matches (search
			// first, choose second), so the flag is what a closed stdin
			// names instead.
			return false, promptReadErrorAs(err, interactiveOnlyVia("pass --pick <n> to choose one of the matches"))
		}
		pick, err = strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			return false, fmt.Errorf("invalid selection")
		}
	}
	if pick < 1 || pick > len(report.Matches) {
		return false, fmt.Errorf("invalid selection: %d is not among the %d match(es)", pick, len(report.Matches))
	}

	applyGameCatalogMatch(cmd, report.Matches[pick-1], spec)
	return false, nil
}

// applyGameCatalogMatch writes a chosen catalog entry into the spec. The
// local games.yaml key core derived from the match's SLUG - passing it is
// what keeps a CurseForge add keyed "minecraft" rather than "432" - and
// the match's name are defaults only: --game-id, --name and a
// --from-detected prefill have all already filled them if they were going
// to, and a detected game's own name and slug must outrank a catalog
// entry's.
func applyGameCatalogMatch(cmd *cobra.Command, match core.GameCatalogMatch, spec *core.GameSpec) {
	spec.Identifier = match.Identifier
	if spec.ID == "" {
		spec.ID = match.GameID
	}
	if spec.Name == "" {
		spec.Name = match.Name
	}
	if !jsonOutput {
		cmd.Printf("\nConfiguring %s...\n", spec.Name)
	}
}

// resolveGameAddManual drives the identifier path for a source with no
// catalog: --id when given, otherwise the display name and identifier
// prompts, in the order the pre-#307 flow used them.
func resolveGameAddManual(cmd *cobra.Command, reader *bufio.Reader, selected source.ModSource, spec *core.GameSpec) error {
	// Changed("id") as well as a non-empty value, so `--id ""` is an
	// explicit "this source has nothing to map" and reaches core rather
	// than falling through to the prompt (or, under --json, to a refusal
	// naming the flag that was in fact passed) - #387.
	if gameAddID != "" || cmd.Flags().Changed("id") {
		spec.Identifier = gameAddID
		return nil
	}
	if jsonOutput {
		return fmt.Errorf("%w: pass --id (this game's identifier with %s)", core.ErrInteractiveOnly, selected.ID())
	}

	cmd.Printf("\n%s has no searchable game catalog; enter this game's identifier with %s directly.\n", selected.Name(), selected.Name())
	if spec.Name == "" {
		if err := missingGameAddValue(cmd, reader, "\nGame name (display): ", "--name", &spec.Name); err != nil {
			return err
		}
	}
	// Optional only where the SOURCE says so: a directory source ignores
	// the value entirely - the README's own words, and what
	// `lmm game edit --source localmods=` has always written - while
	// NexusMods and Steam Workshop have no catalogue either and still need
	// a real game slug/appid. Offering "Enter if it has none" there invited
	// a `nexusmods: ""` mapping that fails at first use (P1b review F5).
	// core applies the same rule to the value that arrives, and still
	// refuses the add when the result leaves no usable game id (#387).
	if source.IgnoresGameIdentifier(selected) {
		if err := optionalGameAddValue(cmd, reader, selected.Name()+" identifier (Enter if it has none): ", "--id", &spec.Identifier); err != nil {
			return err
		}
	} else if err := missingGameAddValue(cmd, reader, selected.Name()+" identifier: ", "--id", &spec.Identifier); err != nil {
		return err
	}
	cmd.Printf("\nConfiguring %s...\n", spec.Name)
	return nil
}

// resolveGameAddPaths fills the install path (required) and mod path
// (optional - core defaults it to <install>/mods) from flags, prompting
// for whatever a flag did not supply.
func resolveGameAddPaths(cmd *cobra.Command, reader *bufio.Reader, spec *core.GameSpec) error {
	if spec.InstallPath == "" {
		spec.InstallPath = gameAddPath
	}
	if spec.InstallPath == "" {
		if err := missingGameAddValue(cmd, reader, "Game install path: ", "--path", &spec.InstallPath); err != nil {
			return err
		}
	}

	if spec.ModPath == "" {
		spec.ModPath = gameAddModPath
	}
	if spec.ModPath == "" && !jsonOutput && gameAddPath == "" {
		// Only offered as part of the interactive walk-through; a run that
		// named --path but not --mod-path is taking core's documented
		// default deliberately, not skipping a question.
		cmd.Printf("Mod path [%s/mods]: ", spec.InstallPath)
		line, err := reader.ReadString('\n')
		if err != nil {
			return promptReadErrorAs(err, interactiveOnlyVia("pass --mod-path (or --path, which takes the default of <install>/mods)"))
		}
		spec.ModPath = strings.TrimSpace(line)
	}
	return nil
}

// missingGameAddValue prompts for one required value, or - under --json,
// where stdin is never read (Ruling 2) - refuses with core.ErrInteractiveOnly
// naming the flag that would have supplied it.
func missingGameAddValue(cmd *cobra.Command, reader *bufio.Reader, prompt, flag string, out *string) error {
	missing := interactiveOnlyVia("pass " + flag)
	if jsonOutput {
		return missing
	}
	cmd.Print(prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		return promptReadErrorAs(err, missing)
	}
	*out = strings.TrimSpace(line)
	if *out == "" {
		return fmt.Errorf("%s is required", strings.TrimPrefix(flag, "--"))
	}
	return nil
}

// optionalGameAddValue is missingGameAddValue for a value an empty answer
// is a legitimate answer to: it prompts and stores whatever comes back,
// including nothing (#387). Under --json it reads nothing at all
// (Ruling 2) and leaves the value as the caller had it - every current
// caller has already handled the flag that supplies it.
func optionalGameAddValue(cmd *cobra.Command, reader *bufio.Reader, prompt, flag string, out *string) error {
	if jsonOutput {
		return nil
	}
	cmd.Print(prompt)
	line, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		// An empty ANSWER is legitimate here; a closed stdin is not an
		// answer at all, so it names the flag - including the explicit
		// empty form - rather than reporting EOF (P1b review F10).
		return promptReadErrorAs(err, interactiveOnlyVia(`pass `+flag+` (`+flag+` "" if this source has none)`))
	}
	*out = strings.TrimSpace(line)
	return nil
}

// reportGameAdded renders AddGame's core.GameListEntry - the same row `lmm
// game list --json` emits, so the two commands agree on a game's shape.
func reportGameAdded(cmd *cobra.Command, entry *core.GameListEntry) error {
	if jsonOutput {
		return emitJSON(entry)
	}

	cmd.Printf("\n✓ Added %s (id: %s)\n", entry.Name, entry.ID)
	ids := make([]string, 0, len(entry.SourceIDs))
	for id := range entry.SourceIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		cmd.Printf("  %s: %s\n", id, entry.SourceIDs[id])
	}
	cmd.Printf("  Install path: %s\n", entry.InstallPath)
	cmd.Printf("  Mod path: %s\n", entry.ModPath)
	cmd.Println("\nYou can now search and install mods with:")
	cmd.Printf("  lmm search <query> --game %s\n", entry.ID)
	return nil
}

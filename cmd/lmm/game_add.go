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

Sources with a searchable game catalog (CurseForge today; any future
source that implements one) accept --query to search it, with --pick to
choose from the matches by number. Without --pick the matches are simply
printed (as the catalog document under --json), so a caller can search
first and choose second. All other registered sources (NexusMods today;
any custom source without a catalog) take the game's identifier with that
source directly via --id - for NexusMods, the slug from its URL (e.g.
https://www.nexusmods.com/skyrimspecialedition -> skyrimspecialedition).

The LOCAL games.yaml key defaults to a slug derived from the catalog
match (the catalog path) or from --id (the manual path); --game-id sets
it explicitly on either path, e.g. to avoid a collision with an existing
game.

The install path must already exist. The mod path is not created here;
deploy creates it on demand.

Examples:
  lmm game add
  # Select source, then search/enter game details

  lmm game add --source nexusmods --id skyrimspecialedition \
    --name "Skyrim Special Edition" --path ~/games/skyrim

  lmm game add --source curseforge --query minecraft --json
  # Prints the matches; re-run with --pick to choose one
  lmm game add --source curseforge --query minecraft --pick 1 \
    --path ~/games/minecraft`,
	Args: cobra.NoArgs,
	RunE: runGameAdd,
}

var (
	gameAddSource  string
	gameAddID      string
	gameAddQuery   string
	gameAddPick    int
	gameAddName    string
	gameAddGameID  string
	gameAddPath    string
	gameAddModPath string
)

func init() {
	gameCmd.AddCommand(gameAddCmd)

	gameAddCmd.Flags().StringVar(&gameAddSource, "source", "", "mod source ID (skips the source prompt)")
	gameAddCmd.Flags().StringVar(&gameAddID, "id", "", "the game's identifier with that source (NexusMods slug, CurseForge game ID, ...)")
	gameAddCmd.Flags().StringVar(&gameAddQuery, "query", "", "search the source's game catalog instead of naming an identifier")
	gameAddCmd.Flags().IntVar(&gameAddPick, "pick", 0, "1-based choice among --query's matches (without it the matches are printed)")
	gameAddCmd.Flags().StringVar(&gameAddName, "name", "", "display name (defaults to the catalog match's name)")
	gameAddCmd.Flags().StringVar(&gameAddGameID, "game-id", "", "the LOCAL games.yaml key (default: derived from the catalog match's slug, or from --id)")
	gameAddCmd.Flags().StringVar(&gameAddPath, "path", "", "game install path (must exist)")
	gameAddCmd.Flags().StringVar(&gameAddModPath, "mod-path", "", "mod directory (default: <install path>/mods)")
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
	selected, err := resolveGameAddSource(cmd, reader, service)
	if err != nil {
		return err
	}

	// --game-id sets the LOCAL games.yaml key on either path (#333 Minor
	// #4 - the manual path had no flag for it at all, a one-way parity
	// hole against POST /api/v1/games' game_id member). The catalog path
	// below still derives its own default from the match's slug when this
	// is empty; core.GameSpec.game() derives one from the identifier when
	// BOTH are empty.
	spec := core.GameSpec{SourceID: selected.ID(), Name: gameAddName, ID: gameAddGameID}
	_, hasCatalog := selected.(source.GameCatalog)

	switch {
	case gameAddQuery != "" || (gameAddID == "" && hasCatalog):
		done, err := resolveGameAddFromCatalog(ctx, cmd, reader, service, selected, &spec)
		if err != nil || done {
			return err
		}
	default:
		if err := resolveGameAddManual(cmd, reader, selected, &spec); err != nil {
			return err
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
	if jsonOutput {
		return nil, fmt.Errorf("%w: pass --source (registered: %s)", core.ErrInteractiveOnly, sourceIDList(sources))
	}

	cmd.Println("Select a mod source:")
	for i, src := range sources {
		cmd.Printf("  [%d] %s (%s)\n", i+1, src.Name(), src.ID())
	}
	cmd.Printf("Enter choice (1-%d): ", len(sources))

	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading input: %w", err)
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
func resolveGameAddFromCatalog(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service, selected source.ModSource, spec *core.GameSpec) (done bool, err error) {
	query := gameAddQuery
	if query == "" {
		if jsonOutput {
			return false, fmt.Errorf("%w: pass --query (to search %s's catalog) or --id", core.ErrInteractiveOnly, selected.ID())
		}
		cmd.Print("\nSearch for a game: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return false, fmt.Errorf("reading input: %w", err)
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
		return true, nil
	}

	pick := gameAddPick
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
			return false, fmt.Errorf("reading input: %w", err)
		}
		pick, err = strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			return false, fmt.Errorf("invalid selection")
		}
	}
	if pick < 1 || pick > len(report.Matches) {
		return false, fmt.Errorf("invalid selection: %d is not among the %d match(es)", pick, len(report.Matches))
	}

	match := report.Matches[pick-1]
	spec.Identifier = match.Identifier
	// The local games.yaml key core derived from the match's SLUG - passing
	// it is what keeps a CurseForge add keyed "minecraft" rather than
	// "432" - unless --game-id already named one explicitly.
	if spec.ID == "" {
		spec.ID = match.GameID
	}
	if spec.Name == "" {
		spec.Name = match.Name
	}
	if !jsonOutput {
		cmd.Printf("\nConfiguring %s...\n", spec.Name)
	}
	return false, nil
}

// resolveGameAddManual drives the identifier path for a source with no
// catalog: --id when given, otherwise the display name and identifier
// prompts, in the order the pre-#307 flow used them.
func resolveGameAddManual(cmd *cobra.Command, reader *bufio.Reader, selected source.ModSource, spec *core.GameSpec) error {
	if gameAddID != "" {
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
	if err := missingGameAddValue(cmd, reader, selected.Name()+" identifier: ", "--id", &spec.Identifier); err != nil {
		return err
	}
	cmd.Printf("\nConfiguring %s...\n", spec.Name)
	return nil
}

// resolveGameAddPaths fills the install path (required) and mod path
// (optional - core defaults it to <install>/mods) from flags, prompting
// for whatever a flag did not supply.
func resolveGameAddPaths(cmd *cobra.Command, reader *bufio.Reader, spec *core.GameSpec) error {
	spec.InstallPath = gameAddPath
	if spec.InstallPath == "" {
		if err := missingGameAddValue(cmd, reader, "Game install path: ", "--path", &spec.InstallPath); err != nil {
			return err
		}
	}

	spec.ModPath = gameAddModPath
	if spec.ModPath == "" && !jsonOutput && gameAddPath == "" {
		// Only offered as part of the interactive walk-through; a run that
		// named --path but not --mod-path is taking core's documented
		// default deliberately, not skipping a question.
		cmd.Printf("Mod path [%s/mods]: ", spec.InstallPath)
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
		spec.ModPath = strings.TrimSpace(line)
	}
	return nil
}

// missingGameAddValue prompts for one required value, or - under --json,
// where stdin is never read (Ruling 2) - refuses with core.ErrInteractiveOnly
// naming the flag that would have supplied it.
func missingGameAddValue(cmd *cobra.Command, reader *bufio.Reader, prompt, flag string, out *string) error {
	if jsonOutput {
		return fmt.Errorf("%w: pass %s", core.ErrInteractiveOnly, flag)
	}
	cmd.Print(prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	*out = strings.TrimSpace(line)
	if *out == "" {
		return fmt.Errorf("%s is required", strings.TrimPrefix(flag, "--"))
	}
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

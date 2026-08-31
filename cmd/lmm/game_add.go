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
	Short: "Add a game interactively",
	Long: `Interactively add a new game configuration.

Prompts for a mod source (every registered source, built-in or custom,
sorted by ID), then its install path and mod path (defaulting to the
install path plus "/mods"), and saves the result to games.yaml along with
an empty default profile.

Sources with a searchable game catalog (CurseForge today; any future
source that implements one) offer interactive search. All other
registered sources (NexusMods today; any custom source without a catalog)
prompt for the game's identifier with that source directly - for
NexusMods, the slug from its URL (e.g.
https://www.nexusmods.com/skyrimspecialedition -> skyrimspecialedition).

Examples:
  lmm game add
  # Select source, then search/enter game details`,
	Args: cobra.NoArgs,
	RunE: runGameAdd,
}

func init() {
	gameCmd.AddCommand(gameAddCmd)
}

// runGameAdd is interactive-only in Phase 3 (v2 Phase 3 Ruling 2 - a
// flag-driven form is a follow-up issue): under --json it rejects up front
// with core.ErrInteractiveOnly, before opening a service or printing any of
// doGameAdd's seven prompts.
func runGameAdd(cmd *cobra.Command, args []string) error {
	if jsonOutput {
		return core.ErrInteractiveOnly
	}
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		reader := bufio.NewReader(os.Stdin)
		return doGameAdd(ctx, cmd, reader, service)
	})
}

// doGameAdd builds the source menu from every registered source
// (service.ListSources(), sorted by ID - the registry itself carries no
// ordering guarantee), prompts for a selection, then dispatches to the
// catalog-search flow for sources implementing source.GameCatalog
// (CurseForge today; any future source for free) or the manual-identifier
// flow for everyone else (NexusMods today). Single-source-per-add: no
// multi-select, matching today's behavior (YAGNI).
func doGameAdd(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service) error {
	sources := service.ListSources()
	if len(sources) == 0 {
		return fmt.Errorf("no mod sources are registered")
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID() < sources[j].ID() })

	cmd.Println("Select a mod source:")
	for i, src := range sources {
		cmd.Printf("  [%d] %s (%s)\n", i+1, src.Name(), src.ID())
	}
	cmd.Printf("Enter choice (1-%d): ", len(sources))

	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	choice, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || choice < 1 || choice > len(sources) {
		return fmt.Errorf("invalid choice: %s", strings.TrimSpace(line))
	}
	selected := sources[choice-1]

	if catalog, ok := selected.(source.GameCatalog); ok {
		return runGameAddCatalog(ctx, cmd, reader, service, catalog, selected.ID(), selected.Name())
	}
	return runGameAddManual(ctx, cmd, reader, service, selected.ID(), selected.Name())
}

// runGameAddCatalog drives the interactive catalog-search flow - today's
// CurseForge path, generalized to any source.GameCatalog: search the
// source's game catalog, filter by substring match on name or slug
// (case-insensitive), let the user pick one, then collect paths and save.
func runGameAddCatalog(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service, catalog source.GameCatalog, sourceID, sourceName string) error {
	cmd.Print("\nSearch for a game: ")
	query, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return fmt.Errorf("search query cannot be empty")
	}

	cmd.Printf("Searching %s...\n", sourceName)
	entries, err := catalog.ListGames(ctx)
	if err != nil {
		// Matches the convention at search.go/install.go/update.go's sibling
		// sites: a source signals a missing/invalid key by wrapping
		// domain.ErrAuthRequired (e.g. CurseForge's ListGames -> GetGames ->
		// the shared httpclient's 401/403 mapping), and the caller rewrites
		// it into the friendly "run lmm auth login <id>" prompt instead of
		// surfacing the raw API error.
		if errors.Is(err, domain.ErrAuthRequired) {
			return authPromptError(sourceID)
		}
		return fmt.Errorf("fetching games from %s: %w", sourceName, err)
	}

	queryLower := strings.ToLower(query)
	var matches []source.GameEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), queryLower) ||
			strings.Contains(strings.ToLower(e.Slug), queryLower) {
			matches = append(matches, e)
		}
	}

	if len(matches) == 0 {
		cmd.Printf("No games found matching %q\n", query)
		return nil
	}

	cmd.Printf("Found %d game(s):\n", len(matches))
	for i, e := range matches {
		cmd.Printf("  [%d] %s (%s id: %s)\n", i+1, e.Name, sourceID, catalogIdentifier(e))
	}

	cmd.Print("Select a game (number): ")
	selection, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	selIdx, err := strconv.Atoi(strings.TrimSpace(selection))
	if err != nil || selIdx < 1 || selIdx > len(matches) {
		return fmt.Errorf("invalid selection")
	}
	selected := matches[selIdx-1]

	cmd.Printf("\nConfiguring %s...\n", selected.Name)
	// The local game ID prefers the entry's Slug but must never be empty:
	// GameEntry doesn't guarantee Slug (CurseForge sets it; the interface
	// doesn't), and an empty ID would save a games.yaml entry keyed ""
	// that later lookups can't address. Fall back to the catalog
	// identifier; a source populating neither field is unusable here.
	gameSlug := strings.ToLower(strings.ReplaceAll(selected.Slug, " ", "-"))
	if gameSlug == "" {
		gameSlug = strings.ToLower(strings.ReplaceAll(catalogIdentifier(selected), " ", "-"))
	}
	if gameSlug == "" {
		return fmt.Errorf("source %s returned a catalog entry with no usable identifier for %q", sourceID, selected.Name)
	}

	installPath, modPath, err := promptForPaths(cmd, reader)
	if err != nil {
		return err
	}

	return saveGameConfig(ctx, cmd, service, gameSlug, selected.Name, installPath, modPath,
		map[string]string{sourceID: catalogIdentifier(selected)})
}

// catalogIdentifier returns the value saved to games.yaml's SourceIDs map
// for a catalog entry: entry.ID when the source populates it - CurseForge's
// ListGames sets it to the numeric game ID as a string (see
// internal/source/curseforge/curseforge.go), matching exactly what today's
// CurseForge path in this file saved via strconv.Itoa(selected.ID) - falling
// back to entry.Slug for a source whose catalog only populates Slug. A
// source populating neither is a GameCatalog implementation bug; the caller
// (runGameAddCatalog) guards against it before saving, erroring rather than
// writing a games.yaml entry keyed "".
func catalogIdentifier(e source.GameEntry) string {
	if e.ID != "" {
		return e.ID
	}
	return e.Slug
}

// runGameAddManual drives the manual-identifier flow - today's NexusMods
// slug path, generalized: prompt for a display name and the game's
// identifier with sourceName directly, then collect paths and save.
func runGameAddManual(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service, sourceID, sourceName string) error {
	cmd.Printf("\n%s has no searchable game catalog; enter this game's identifier with %s directly.\n", sourceName, sourceName)

	cmd.Print("\nGame name (display): ")
	gameName, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	gameName = strings.TrimSpace(gameName)
	if gameName == "" {
		return fmt.Errorf("game name is required")
	}

	cmd.Printf("%s identifier: ", sourceName)
	identifier, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return fmt.Errorf("%s identifier is required", sourceName)
	}

	gameSlug := strings.ToLower(strings.ReplaceAll(identifier, " ", "-"))

	cmd.Printf("\nConfiguring %s...\n", gameName)
	installPath, modPath, err := promptForPaths(cmd, reader)
	if err != nil {
		return err
	}

	return saveGameConfig(ctx, cmd, service, gameSlug, gameName, installPath, modPath,
		map[string]string{sourceID: identifier})
}

func promptForPaths(cmd *cobra.Command, reader *bufio.Reader) (installPath, modPath string, err error) {
	cmd.Print("Game install path: ")
	installPath, err = reader.ReadString('\n')
	if err != nil {
		return "", "", fmt.Errorf("reading input: %w", err)
	}
	installPath = strings.TrimSpace(installPath)
	if installPath == "" {
		return "", "", fmt.Errorf("install path is required")
	}

	defaultModPath := installPath + "/mods"
	cmd.Printf("Mod path [%s]: ", defaultModPath)
	modPath, err = reader.ReadString('\n')
	if err != nil {
		return "", "", fmt.Errorf("reading input: %w", err)
	}
	modPath = strings.TrimSpace(modPath)
	if modPath == "" {
		modPath = defaultModPath
	}

	return installPath, modPath, nil
}

func saveGameConfig(ctx context.Context, cmd *cobra.Command, service *core.Service, gameSlug, gameName, installPath, modPath string, sourceIDs map[string]string) error {
	game := &domain.Game{
		ID:          gameSlug,
		Name:        gameName,
		InstallPath: installPath,
		ModPath:     modPath,
		SourceIDs:   sourceIDs,
		LinkMethod:  domain.LinkSymlink,
	}

	if err := service.SaveGame(ctx, game); err != nil {
		return fmt.Errorf("saving game: %w", err)
	}

	// Create default profile - the same core helper 'lmm game detect' uses
	// (ApplyGameDetect), so both call sites share the unconditional
	// overwrite-on-repair semantics documented there.
	if _, err := service.NewProfileManager().CreateOrResetDefaultAfterGameSave(ctx, gameSlug); err != nil {
		return fmt.Errorf("creating default profile: %w", err)
	}

	cmd.Printf("\n✓ Added %s (id: %s)\n", gameName, gameSlug)
	for source, id := range sourceIDs {
		cmd.Printf("  %s: %s\n", source, id)
	}
	cmd.Printf("  Install path: %s\n", installPath)
	cmd.Printf("  Mod path: %s\n", modPath)
	cmd.Println("\nYou can now search and install mods with:")
	cmd.Printf("  lmm search <query> --game %s\n", gameSlug)

	return nil
}

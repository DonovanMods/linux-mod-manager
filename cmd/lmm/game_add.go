package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
)

var gameAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a game interactively",
	Long: `Interactively add a new game configuration.

Supports adding games from:
  - CurseForge (searchable game list)
  - NexusMods (requires game slug)

Example:
  lmm game add
  # Select source, then search/enter game details`,
	Args: cobra.NoArgs,
	RunE: runGameAdd,
}

func init() {
	gameCmd.AddCommand(gameAddCmd)
}

func runGameAdd(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	// Step 1: Select source
	cmd.Println("Select a mod source:")
	cmd.Println("  [1] CurseForge (searchable)")
	cmd.Println("  [2] NexusMods (enter game slug)")
	cmd.Print("Enter choice (1-2): ")

	sourceChoice, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	sourceChoice = strings.TrimSpace(sourceChoice)

	switch sourceChoice {
	case "1":
		return runGameAddCurseForge(cmd, reader)
	case "2":
		return runGameAddNexusMods(cmd, reader)
	default:
		return fmt.Errorf("invalid choice: %s", sourceChoice)
	}
}

func runGameAddCurseForge(cmd *cobra.Command, reader *bufio.Reader) error {
	// Get CurseForge API key
	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	apiKey := getSourceAPIKey(service, "curseforge", "CURSEFORGE_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("CurseForge authentication required. Run: lmm auth login curseforge")
	}

	client := curseforge.NewClient(nil, apiKey)

	// Search for game
	cmd.Print("\nSearch for a game: ")
	query, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return fmt.Errorf("search query cannot be empty")
	}

	cmd.Println("Searching CurseForge...")
	ctx := context.Background()
	games, err := client.GetGames(ctx)
	if err != nil {
		return fmt.Errorf("fetching games from CurseForge: %w", err)
	}

	// Filter games by query (case-insensitive)
	queryLower := strings.ToLower(query)
	var matches []curseforge.Game
	for _, g := range games {
		if strings.Contains(strings.ToLower(g.Name), queryLower) ||
			strings.Contains(strings.ToLower(g.Slug), queryLower) {
			matches = append(matches, g)
		}
	}

	if len(matches) == 0 {
		cmd.Printf("No games found matching %q\n", query)
		return nil
	}

	// Display results
	cmd.Printf("Found %d game(s):\n", len(matches))
	for i, g := range matches {
		cmd.Printf("  [%d] %s (curseforge id: %d)\n", i+1, g.Name, g.ID)
	}

	// Select game
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

	// Configure game
	cmd.Printf("\nConfiguring %s...\n", selected.Name)
	gameSlug := strings.ToLower(strings.ReplaceAll(selected.Slug, " ", "-"))

	installPath, modPath, err := promptForPaths(cmd, reader)
	if err != nil {
		return err
	}

	// Save game config
	return saveGameConfig(cmd, gameSlug, selected.Name, installPath, modPath,
		map[string]string{"curseforge": strconv.Itoa(selected.ID)})
}

func runGameAddNexusMods(cmd *cobra.Command, reader *bufio.Reader) error {
	cmd.Println("\nNexusMods game slugs can be found in the URL.")
	cmd.Println("Example: https://www.nexusmods.com/skyrimspecialedition -> skyrimspecialedition")

	// Get game name
	cmd.Print("\nGame name (display): ")
	gameName, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	gameName = strings.TrimSpace(gameName)
	if gameName == "" {
		return fmt.Errorf("game name is required")
	}

	// Get NexusMods slug
	cmd.Print("NexusMods slug (from URL): ")
	nexusSlug, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	nexusSlug = strings.TrimSpace(nexusSlug)
	if nexusSlug == "" {
		return fmt.Errorf("NexusMods slug is required")
	}

	// Generate local game ID from slug
	gameSlug := strings.ToLower(strings.ReplaceAll(nexusSlug, " ", "-"))

	// Configure paths
	cmd.Printf("\nConfiguring %s...\n", gameName)
	installPath, modPath, err := promptForPaths(cmd, reader)
	if err != nil {
		return err
	}

	// Save game config
	return saveGameConfig(cmd, gameSlug, gameName, installPath, modPath,
		map[string]string{"nexusmods": nexusSlug})
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

func saveGameConfig(cmd *cobra.Command, gameSlug, gameName, installPath, modPath string, sourceIDs map[string]string) error {
	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}

	game := &domain.Game{
		ID:          gameSlug,
		Name:        gameName,
		InstallPath: installPath,
		ModPath:     modPath,
		SourceIDs:   sourceIDs,
		LinkMethod:  domain.LinkSymlink,
	}

	if err := config.SaveGame(svcCfg.ConfigDir, game); err != nil {
		return fmt.Errorf("saving game: %w", err)
	}

	// Create default profile
	defaultProfile := &domain.Profile{
		Name:       "default",
		GameID:     gameSlug,
		Mods:       nil,
		LinkMethod: domain.LinkSymlink,
		IsDefault:  true,
	}
	if err := config.SaveProfile(svcCfg.ConfigDir, defaultProfile); err != nil {
		return fmt.Errorf("creating default profile: %w", err)
	}

	cmd.Printf("\n\u2713 Added %s (id: %s)\n", gameName, gameSlug)
	for source, id := range sourceIDs {
		cmd.Printf("  %s: %s\n", source, id)
	}
	cmd.Printf("  Install path: %s\n", installPath)
	cmd.Printf("  Mod path: %s\n", modPath)
	cmd.Println("\nYou can now search and install mods with:")
	cmd.Printf("  lmm search <query> --game %s\n", gameSlug)

	return nil
}

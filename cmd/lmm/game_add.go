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
	Long: `Interactively add a new game by searching CurseForge.

Searches for games by name and guides you through configuration.

Example:
  lmm game add
  # Then search for "Hytale" or "Minecraft" etc.`,
	Args: cobra.NoArgs,
	RunE: runGameAdd,
}

func init() {
	gameCmd.AddCommand(gameAddCmd)
}

func runGameAdd(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	// Step 1: Get CurseForge API key
	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	apiKey, err := getSourceAPIKey(service, "curseforge", "CURSEFORGE_API_KEY"), nil
	if err != nil || apiKey == "" {
		return fmt.Errorf("CurseForge authentication required. Run: lmm auth login curseforge")
	}

	client := curseforge.NewClient(nil, apiKey)

	// Step 2: Search for game
	cmd.Print("Search for a game: ")
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

	// Step 3: Display results
	cmd.Printf("Found %d game(s):\n", len(matches))
	for i, g := range matches {
		cmd.Printf("  [%d] %s (curseforge id: %d)\n", i+1, g.Name, g.ID)
	}

	// Step 4: Select game
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

	// Step 5: Get game details
	cmd.Printf("\nConfiguring %s...\n", selected.Name)

	// Generate slug for game ID
	gameSlug := strings.ToLower(strings.ReplaceAll(selected.Slug, " ", "-"))

	// Prompt for install path
	cmd.Print("Game install path: ")
	installPath, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	installPath = strings.TrimSpace(installPath)
	if installPath == "" {
		return fmt.Errorf("install path is required")
	}

	// Prompt for mod path (default to install path + /mods)
	defaultModPath := installPath + "/mods"
	cmd.Printf("Mod path [%s]: ", defaultModPath)
	modPath, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	modPath = strings.TrimSpace(modPath)
	if modPath == "" {
		modPath = defaultModPath
	}

	// Step 6: Create game config
	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}

	game := &domain.Game{
		ID:          gameSlug,
		Name:        selected.Name,
		InstallPath: installPath,
		ModPath:     modPath,
		SourceIDs:   map[string]string{"curseforge": strconv.Itoa(selected.ID)},
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

	cmd.Printf("\n✓ Added %s (id: %s)\n", selected.Name, gameSlug)
	cmd.Printf("  CurseForge ID: %d\n", selected.ID)
	cmd.Printf("  Install path: %s\n", installPath)
	cmd.Printf("  Mod path: %s\n", modPath)
	cmd.Println("\nYou can now search and install mods with:")
	cmd.Printf("  lmm search <query> --game %s\n", gameSlug)

	return nil
}

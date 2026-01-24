package main

import (
	"context"
	"fmt"

	"lmm/internal/domain"

	"github.com/spf13/cobra"
)

var (
	installSource  string
	installProfile string
	installVersion string
)

var installCmd = &cobra.Command{
	Use:   "install <mod-id>",
	Short: "Install a mod",
	Long: `Install a mod from the configured source.

The mod will be added to the specified profile (or default profile if not specified).

Examples:
  lmm install 12345 --game skyrim-se
  lmm install 12345 --game skyrim-se --profile survival
  lmm install 12345 --game skyrim-se --source nexusmods --version 1.0.0`,
	Args: cobra.ExactArgs(1),
	RunE: runInstall,
}

func init() {
	installCmd.Flags().StringVarP(&installSource, "source", "s", "nexusmods", "mod source")
	installCmd.Flags().StringVarP(&installProfile, "profile", "p", "", "profile to install to (default: active profile)")
	installCmd.Flags().StringVar(&installVersion, "version", "", "specific version to install (default: latest)")

	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	modID := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	ctx := context.Background()

	// Fetch mod info
	if verbose {
		fmt.Printf("Fetching mod %s from %s...\n", modID, installSource)
	}

	mod, err := service.GetMod(ctx, installSource, gameID, modID)
	if err != nil {
		return fmt.Errorf("failed to fetch mod: %w", err)
	}

	fmt.Printf("Installing: %s v%s by %s\n", mod.Name, mod.Version, mod.Author)

	// Determine profile
	profileName := installProfile
	if profileName == "" {
		profileName = "default"
	}

	// Add to profile (actual download/deployment would happen here)
	// For now, just record the intent
	fmt.Printf("  Game: %s\n", game.Name)
	fmt.Printf("  Profile: %s\n", profileName)
	fmt.Printf("  Source: %s\n", installSource)

	// Save to database
	installedMod := &domain.InstalledMod{
		Mod:          *mod,
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}

	if err := service.DB().SaveInstalledMod(installedMod); err != nil {
		return fmt.Errorf("failed to save mod: %w", err)
	}

	fmt.Println("✓ Mod installed successfully")
	fmt.Println("\nNote: Mod files need to be downloaded separately (download feature coming soon)")

	return nil
}

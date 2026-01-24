package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"lmm/internal/core"
	"lmm/internal/domain"

	"github.com/spf13/cobra"
)

var (
	updateSource  string
	updateProfile string
	updateAll     bool
)

var updateCmd = &cobra.Command{
	Use:   "update [mod-id]",
	Short: "Check for or apply mod updates",
	Long: `Check for available updates or update specific mods.

Without arguments, checks all installed mods for updates.
With a mod ID, updates that specific mod.

Examples:
  lmm update --game skyrim-se                    # Check all mods for updates
  lmm update 12345 --game skyrim-se              # Update specific mod
  lmm update --game skyrim-se --all              # Apply all available updates`,
	Args: cobra.MaximumNArgs(1),
	RunE: runUpdate,
}

func init() {
	updateCmd.Flags().StringVarP(&updateSource, "source", "s", "nexusmods", "mod source")
	updateCmd.Flags().StringVarP(&updateProfile, "profile", "p", "", "profile to check (default: active profile)")
	updateCmd.Flags().BoolVar(&updateAll, "all", false, "apply all available updates")

	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

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

	// Determine profile
	profileName := updateProfile
	if profileName == "" {
		profileName = "default"
	}

	ctx := context.Background()

	// Get installed mods
	installed, err := service.GetInstalledMods(gameID, profileName)
	if err != nil {
		return fmt.Errorf("failed to get installed mods: %w", err)
	}

	if len(installed) == 0 {
		fmt.Println("No mods installed.")
		return nil
	}

	// If specific mod ID provided, filter to just that mod
	if len(args) > 0 {
		modID := args[0]
		var found bool
		for _, mod := range installed {
			if mod.ID == modID && mod.SourceID == updateSource {
				installed = []domain.InstalledMod{mod}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("mod %s not found in profile %s", modID, profileName)
		}
	}

	if verbose {
		fmt.Printf("Checking %d mod(s) for updates in %s (profile: %s)...\n", len(installed), game.Name, profileName)
	}

	// Check for updates
	updater := core.NewUpdater(service.Registry())
	updates, err := updater.CheckUpdates(ctx, installed)
	if err != nil {
		return fmt.Errorf("failed to check updates: %w", err)
	}

	if len(updates) == 0 {
		fmt.Println("All mods are up to date.")
		return nil
	}

	// Display available updates
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "MOD\tCURRENT\tAVAILABLE\n")
	fmt.Fprintf(w, "---\t-------\t---------\n")

	for _, update := range updates {
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			truncate(update.InstalledMod.Name, 40),
			update.InstalledMod.Version,
			update.NewVersion,
		)
	}
	w.Flush()

	fmt.Printf("\n%d update(s) available.\n", len(updates))

	// If --all flag, apply updates
	if updateAll {
		fmt.Println("\nApplying updates...")
		fmt.Println("Note: Automatic update application not yet implemented.")
	}

	return nil
}

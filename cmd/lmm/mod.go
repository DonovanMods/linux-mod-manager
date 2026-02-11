package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	modSource    string
	modProfile   string
	modSetAuto   bool
	modSetNotify bool
	modSetPin    bool
)

var modCmd = &cobra.Command{
	Use:   "mod",
	Short: "Manage mod settings",
	Long:  `Commands for managing individual mod settings.`,
}

var modSetUpdateCmd = &cobra.Command{
	Use:   "set-update <mod-id>",
	Short: "Set update policy for a mod",
	Long: `Set the update policy for an installed mod.

Policies:
  --auto    Automatically apply updates when checking
  --notify  Show available updates, require manual approval (default)
  --pin     Never update this mod automatically

Examples:
  lmm mod set-update 12345 --game skyrim-se --auto
  lmm mod set-update 12345 --game skyrim-se --pin
  lmm mod set-update 12345 --game skyrim-se --notify`,
	Args: cobra.ExactArgs(1),
	RunE: runModSetUpdate,
}

var modEnableCmd = &cobra.Command{
	Use:   "enable <mod-id>",
	Short: "Enable a disabled mod",
	Long: `Enable a mod that was previously disabled.

This deploys the mod files from the cache to the game directory.
The mod must already be installed and in the cache.

Examples:
  lmm mod enable 12345 --game skyrim-se
  lmm mod enable 12345 --game skyrim-se --profile survival`,
	Args: cobra.ExactArgs(1),
	RunE: runModEnable,
}

var modDisableCmd = &cobra.Command{
	Use:   "disable <mod-id>",
	Short: "Disable a mod without uninstalling",
	Long: `Disable a mod by removing its files from the game directory.

The mod remains installed and cached, so it can be re-enabled later
without downloading again.

Examples:
  lmm mod disable 12345 --game skyrim-se
  lmm mod disable 12345 --game skyrim-se --profile survival`,
	Args: cobra.ExactArgs(1),
	RunE: runModDisable,
}

var modFilesCmd = &cobra.Command{
	Use:   "files <mod-id>",
	Short: "List files deployed by a mod",
	Long: `Show all files that a mod has deployed to the game directory.

This helps identify which files a mod owns, useful for debugging
conflicts or understanding mod contents.

Examples:
  lmm mod files 12345 --game skyrim-se
  lmm mod files 12345 --game skyrim-se --profile survival`,
	Args: cobra.ExactArgs(1),
	RunE: runModFiles,
}

var modShowCmd = &cobra.Command{
	Use:   "show <mod-id>",
	Short: "Show mod details",
	Long: `Fetch and display mod details from the source (description, summary, image URL).

Does not require the mod to be installed. Use --json for scriptable output.

Examples:
  lmm mod show 12345 --game skyrim-se
  lmm mod show 12345 --game skyrim-se --json`,
	Args: cobra.ExactArgs(1),
	RunE: runModShow,
}

func init() {
	modCmd.PersistentFlags().StringVarP(&modSource, "source", "s", "", "mod source (default: first configured source alphabetically)")
	modCmd.PersistentFlags().StringVarP(&modProfile, "profile", "p", "", "profile (default: active profile)")

	modSetUpdateCmd.Flags().BoolVar(&modSetAuto, "auto", false, "enable auto-update")
	modSetUpdateCmd.Flags().BoolVar(&modSetNotify, "notify", false, "notify only (default)")
	modSetUpdateCmd.Flags().BoolVar(&modSetPin, "pin", false, "pin to current version")

	modCmd.AddCommand(modSetUpdateCmd)
	modCmd.AddCommand(modEnableCmd)
	modCmd.AddCommand(modDisableCmd)
	modCmd.AddCommand(modFilesCmd)
	modCmd.AddCommand(modShowCmd)
	rootCmd.AddCommand(modCmd)
}

func runModSetUpdate(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	modID := args[0]

	// Validate that exactly one policy is specified
	policyCount := 0
	if modSetAuto {
		policyCount++
	}
	if modSetNotify {
		policyCount++
	}
	if modSetPin {
		policyCount++
	}

	if policyCount == 0 {
		return fmt.Errorf("specify a policy: --auto, --notify, or --pin")
	}
	if policyCount > 1 {
		return fmt.Errorf("specify only one policy: --auto, --notify, or --pin")
	}

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	// Resolve source from game config
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}
	modSource, err = resolveSource(game, modSource, false)
	if err != nil {
		return err
	}

	profileName := profileOrDefault(modProfile)

	// Get the mod to verify it exists and get its name
	mod, err := service.GetInstalledMod(modSource, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	// Determine the new policy
	var policy domain.UpdatePolicy
	var policyStr string
	switch {
	case modSetAuto:
		policy = domain.UpdateAuto
		policyStr = "auto"
	case modSetPin:
		policy = domain.UpdatePinned
		policyStr = "pinned"
	default:
		policy = domain.UpdateNotify
		policyStr = "notify"
	}

	// Update the policy
	if err := service.SetModUpdatePolicy(modSource, modID, gameID, profileName, policy); err != nil {
		return fmt.Errorf("failed to update policy: %w", err)
	}

	fmt.Printf("✓ %s update policy: %s", mod.Name, policyStr)
	if modSetPin {
		fmt.Printf(" (v%s)", mod.Version)
	}
	fmt.Println()

	return nil
}

func runModEnable(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	modID := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	// Resolve source: use flag if set, otherwise first configured source
	modSource, err = resolveSource(game, modSource, false)
	if err != nil {
		return err
	}

	profileName := profileOrDefault(modProfile)

	// Get the mod to verify it exists
	mod, err := service.GetInstalledMod(modSource, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	if mod.Enabled {
		fmt.Printf("%s is already enabled.\n", mod.Name)
		return nil
	}

	// Check if mod is in cache
	if !service.GetGameCache(game).Exists(gameID, modSource, modID, mod.Version) {
		return fmt.Errorf("mod not found in cache - try reinstalling with 'lmm install --id %s'", modID)
	}

	ctx := context.Background()

	// Deploy mod files from cache
	installer := service.GetInstaller(game)

	if err := installer.Install(ctx, game, &mod.Mod, profileName); err != nil {
		return fmt.Errorf("failed to deploy mod: %w", err)
	}

	// Update enabled flag in database
	if err := service.DB().SetModEnabled(modSource, modID, gameID, profileName, true); err != nil {
		return fmt.Errorf("failed to update mod status: %w", err)
	}

	fmt.Printf("✓ Enabled: %s\n", mod.Name)
	return nil
}

func runModDisable(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	modID := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	// Resolve source: use flag if set, otherwise first configured source
	modSource, err = resolveSource(game, modSource, false)
	if err != nil {
		return err
	}

	profileName := profileOrDefault(modProfile)

	// Get the mod to verify it exists
	mod, err := service.GetInstalledMod(modSource, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	if !mod.Enabled {
		fmt.Printf("%s is already disabled.\n", mod.Name)
		return nil
	}

	ctx := context.Background()

	// Undeploy mod files from game directory
	installer := service.GetInstaller(game)

	if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
		// Warn but continue - files may have been manually removed
		if verbose {
			fmt.Printf("  Warning: failed to undeploy some files: %v\n", err)
		}
	}

	// Update enabled flag in database
	if err := service.DB().SetModEnabled(modSource, modID, gameID, profileName, false); err != nil {
		return fmt.Errorf("failed to update mod status: %w", err)
	}

	fmt.Printf("✓ Disabled: %s (files removed from game, kept in cache)\n", mod.Name)
	return nil
}

func runModFiles(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	modID := args[0]
	profileName := profileOrDefault(modProfile)

	svc, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() { _ = svc.Close() }()

	// Resolve source from game config
	game, err := svc.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}
	modSource, err = resolveSource(game, modSource, false)
	if err != nil {
		return err
	}

	// Get mod info for display
	mod, err := svc.GetInstalledMod(modSource, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	// Get deployed files from database
	files, err := svc.DB().GetDeployedFilesForMod(gameID, profileName, modSource, modID)
	if err != nil {
		return fmt.Errorf("getting deployed files: %w", err)
	}

	fmt.Printf("Files deployed by %s (%s):\n\n", mod.Name, modID)

	if len(files) == 0 {
		fmt.Println("  No deployed files tracked.")
		fmt.Println("  (Files are tracked on install; existing mods may need to be redeployed)")
		return nil
	}

	for _, f := range files {
		fmt.Printf("  %s\n", f)
	}
	fmt.Printf("\nTotal: %d file(s)\n", len(files))

	return nil
}

func runModShow(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	modID := args[0]

	svc, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() { _ = svc.Close() }()

	// Resolve source from game config
	game, err := svc.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}
	modSource, err = resolveSource(game, modSource, false)
	if err != nil {
		return err
	}

	ctx := context.Background()
	mod, err := svc.GetMod(ctx, modSource, gameID, modID)
	if err != nil {
		return fmt.Errorf("mod not found: %w", err)
	}

	if jsonOutput {
		type modShowJSON struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Version      string `json:"version"`
			Author       string `json:"author"`
			Summary      string `json:"summary"`
			Description  string `json:"description"`
			SourceURL    string `json:"source_url,omitempty"`
			PictureURL   string `json:"picture_url,omitempty"`
			Category     string `json:"category"`
			Endorsements int64  `json:"endorsements"`
		}
		out := modShowJSON{
			ID:           mod.ID,
			Name:         mod.Name,
			Version:      mod.Version,
			Author:       mod.Author,
			Summary:      mod.Summary,
			Description:  mod.Description,
			SourceURL:    mod.SourceURL,
			PictureURL:   mod.PictureURL,
			Category:     mod.Category,
			Endorsements: mod.Endorsements,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Human-readable output
	fmt.Printf("%s\n", strings.Repeat("=", 60))
	fmt.Printf("%s\n", mod.Name)
	fmt.Printf("%s\n", strings.Repeat("=", 60))
	fmt.Printf("ID: %s  Version: %s  Author: %s\n", mod.ID, mod.Version, mod.Author)
	if mod.Category != "" {
		fmt.Printf("Category: %s  Endorsements: %d\n", mod.Category, mod.Endorsements)
	}
	if mod.PictureURL != "" {
		fmt.Printf("Image: %s\n", mod.PictureURL)
	}
	fmt.Println()

	if mod.Summary != "" {
		fmt.Println("Summary:")
		fmt.Println(strings.TrimSpace(mod.Summary))
		fmt.Println()
	}

	if mod.Description != "" {
		fmt.Println("Description:")
		// Limit length for terminal; description can be long HTML
		desc := strings.TrimSpace(mod.Description)
		const maxDesc = 2000
		if len(desc) > maxDesc {
			desc = desc[:maxDesc] + "\n... (truncated; view on site for full description)"
		}
		fmt.Println(desc)
	}

	return nil
}

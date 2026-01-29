package main

import (
	"context"
	"fmt"

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

func init() {
	modCmd.PersistentFlags().StringVarP(&modSource, "source", "s", "nexusmods", "mod source")
	modCmd.PersistentFlags().StringVarP(&modProfile, "profile", "p", "", "profile (default: active profile)")

	modSetUpdateCmd.Flags().BoolVar(&modSetAuto, "auto", false, "enable auto-update")
	modSetUpdateCmd.Flags().BoolVar(&modSetNotify, "notify", false, "notify only (default)")
	modSetUpdateCmd.Flags().BoolVar(&modSetPin, "pin", false, "pin to current version")

	modCmd.AddCommand(modSetUpdateCmd)
	modCmd.AddCommand(modEnableCmd)
	modCmd.AddCommand(modDisableCmd)
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
	defer service.Close()

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
	defer service.Close()

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
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
	defer service.Close()

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
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

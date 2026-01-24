package main

import (
	"fmt"

	"lmm/internal/domain"

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

func init() {
	modCmd.PersistentFlags().StringVarP(&modSource, "source", "s", "nexusmods", "mod source")
	modCmd.PersistentFlags().StringVarP(&modProfile, "profile", "p", "", "profile (default: active profile)")

	modSetUpdateCmd.Flags().BoolVar(&modSetAuto, "auto", false, "enable auto-update")
	modSetUpdateCmd.Flags().BoolVar(&modSetNotify, "notify", false, "notify only (default)")
	modSetUpdateCmd.Flags().BoolVar(&modSetPin, "pin", false, "pin to current version")

	modCmd.AddCommand(modSetUpdateCmd)
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

	profileName := modProfile
	if profileName == "" {
		profileName = "default"
	}

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

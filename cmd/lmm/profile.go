package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"lmm/internal/core"
	"lmm/internal/linker"

	"github.com/spf13/cobra"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage mod profiles",
	Long: `Manage mod profiles for organizing different mod configurations.

Profiles allow you to maintain different sets of mods for the same game.
For example, you might have a "vanilla plus" profile and a "total conversion" profile.`,
}

var profileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all profiles",
	Long: `List all profiles for the specified game.

Examples:
  lmm profile list --game skyrim-se`,
	RunE: runProfileList,
}

var profileCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new profile",
	Long: `Create a new empty profile for the specified game.

Examples:
  lmm profile create survival --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileCreate,
}

var profileDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a profile",
	Long: `Delete a profile and its configuration.

Note: This does not remove the installed mods, only the profile configuration.

Examples:
  lmm profile delete old-profile --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileDelete,
}

var profileSwitchCmd = &cobra.Command{
	Use:   "switch <name>",
	Short: "Switch to a different profile",
	Long: `Switch to a different profile, deploying its mods to the game directory.

This will undeploy mods from the current profile and deploy mods from the new profile.

Examples:
  lmm profile switch survival --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileSwitch,
}

var profileExportCmd = &cobra.Command{
	Use:   "export <name>",
	Short: "Export a profile",
	Long: `Export a profile to a portable YAML file.

The exported file can be shared with others or used as a backup.

Examples:
  lmm profile export survival --game skyrim-se > survival.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileExport,
}

var profileImportCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Import a profile",
	Long: `Import a profile from a YAML file.

Examples:
  lmm profile import survival.yaml --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileImport,
}

func init() {
	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileCreateCmd)
	profileCmd.AddCommand(profileDeleteCmd)
	profileCmd.AddCommand(profileSwitchCmd)
	profileCmd.AddCommand(profileExportCmd)
	profileCmd.AddCommand(profileImportCmd)

	rootCmd.AddCommand(profileCmd)
}

func getProfileManager(service *core.Service) *core.ProfileManager {
	lnk := linker.New(service.GetDefaultLinkMethod())
	return core.NewProfileManager(service.ConfigDir(), service.DB(), service.Cache(), lnk)
}

func runProfileList(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	pm := getProfileManager(service)

	profiles, err := pm.List(gameID)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}

	if len(profiles) == 0 {
		fmt.Println("No profiles found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tMODS\tDEFAULT")
	fmt.Fprintln(w, "----\t----\t-------")

	for _, p := range profiles {
		defaultMark := ""
		if p.IsDefault {
			defaultMark = "*"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\n", p.Name, len(p.Mods), defaultMark)
	}
	w.Flush()

	return nil
}

func runProfileCreate(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	name := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	pm := getProfileManager(service)

	profile, err := pm.Create(gameID, name)
	if err != nil {
		return fmt.Errorf("creating profile: %w", err)
	}

	fmt.Printf("✓ Created profile: %s\n", profile.Name)
	return nil
}

func runProfileDelete(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	name := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	pm := getProfileManager(service)

	if err := pm.Delete(gameID, name); err != nil {
		return fmt.Errorf("deleting profile: %w", err)
	}

	fmt.Printf("✓ Deleted profile: %s\n", name)
	return nil
}

func runProfileSwitch(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	name := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	pm := getProfileManager(service)
	ctx := context.Background()

	if verbose {
		fmt.Printf("Switching to profile: %s\n", name)
	}

	if err := pm.Switch(ctx, game, name); err != nil {
		return fmt.Errorf("switching profile: %w", err)
	}

	fmt.Printf("✓ Switched to profile: %s\n", name)
	return nil
}

func runProfileExport(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	name := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	pm := getProfileManager(service)

	data, err := pm.Export(gameID, name)
	if err != nil {
		return fmt.Errorf("exporting profile: %w", err)
	}

	fmt.Print(string(data))
	return nil
}

func runProfileImport(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	filePath := args[0]

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	pm := getProfileManager(service)

	profile, err := pm.Import(data)
	if err != nil {
		return fmt.Errorf("importing profile: %w", err)
	}

	fmt.Printf("✓ Imported profile: %s\n", profile.Name)
	return nil
}

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
)

var (
	uninstallSource  string
	uninstallProfile string
	uninstallKeep    bool
	uninstallForce   bool
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall <mod-id>",
	Short: "Uninstall a mod",
	Long: `Uninstall a mod from the specified profile.

By default, the mod files are removed from the game directory and the cache.
Use --keep-cache to preserve the cached files for potential reinstallation.

Examples:
  lmm uninstall 12345 --game skyrim-se
  lmm uninstall 12345 --game skyrim-se --profile survival
  lmm uninstall 12345 --game skyrim-se --keep-cache`,
	Args: cobra.ExactArgs(1),
	RunE: runUninstall,
}

func init() {
	uninstallCmd.Flags().StringVarP(&uninstallSource, "source", "s", "", "mod source (if omitted, searches all sources for mod ID)")
	uninstallCmd.Flags().StringVarP(&uninstallProfile, "profile", "p", "", "profile to uninstall from (default: active profile)")
	uninstallCmd.Flags().BoolVar(&uninstallKeep, "keep-cache", false, "keep cached mod files")
	uninstallCmd.Flags().BoolVarP(&uninstallForce, "force", "f", false, "continue even if hooks fail")

	rootCmd.AddCommand(uninstallCmd)
}

func runUninstall(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doUninstall(ctx, service, game, args[0])
	})
}

func doUninstall(ctx context.Context, service *core.Service, game *domain.Game, modID string) error {
	// Determine profile
	profileName := profileOrDefault(uninstallProfile)

	if verbose {
		fmt.Printf("Uninstalling mod %s from %s (profile: %s)...\n", modID, game.Name, profileName)
	}

	// Find the mod - try specified source first, then search all installed mods
	var installedMod *domain.InstalledMod
	var err error
	if uninstallSource != "" {
		// Source explicitly specified
		if uninstallSource != domain.SourceLocal {
			if _, ok := game.SourceIDs[uninstallSource]; !ok {
				return fmt.Errorf("source %q is not configured for %s", uninstallSource, game.Name)
			}
		}
		installedMod, err = service.GetInstalledMod(uninstallSource, modID, game.ID, profileName)
		if err != nil {
			return fmt.Errorf("mod %s not found in profile %s (source: %s)", modID, profileName, uninstallSource)
		}
	} else {
		// No source specified - search all installed mods by ID
		allMods, err := service.GetInstalledMods(game.ID, profileName)
		if err != nil {
			return fmt.Errorf("listing installed mods: %w", err)
		}
		for i := range allMods {
			if allMods[i].ID == modID {
				installedMod = &allMods[i]
				break
			}
		}
		if installedMod == nil {
			return fmt.Errorf("mod %s not found in profile %s", modID, profileName)
		}
	}

	opts := core.UninstallOptions{
		KeepCache:   uninstallKeep,
		Hooks:       getResolvedHooks(service, game, profileName),
		HookRunner:  getHookRunner(service),
		HookContext: makeHookContext(game),
		Force:       uninstallForce,
	}

	result, err := service.UninstallMod(ctx, game, profileName, installedMod.SourceID, modID, opts)
	if err != nil {
		// UninstallMod's error-path convention returns any diagnostics
		// accumulated before the fatal error alongside it (see
		// UninstallResult's doc comment); print them now, or they'd
		// otherwise be lost even though they already happened.
		printUninstallDiagnostics(result)
		return err
	}

	printUninstallDiagnostics(result)

	fmt.Printf("✓ Uninstalled: %s\n", installedMod.Name)
	fmt.Printf("  Removed from profile: %s\n", profileName)

	if uninstallKeep {
		fmt.Println("  Cache files preserved")
	}

	return nil
}

// printUninstallDiagnostics prints result's accumulated diagnostics using
// the display contract documented on core.UninstallResult: Notes go to
// stdout, only under --verbose (each entry already carries its historical
// prefix word); Warnings go to stderr, unconditionally. Safe to call with a
// nil result (nothing to print) - result is nil only when UninstallMod
// failed before it could allocate the result struct.
func printUninstallDiagnostics(result *core.UninstallResult) {
	if result == nil {
		return
	}

	if verbose {
		for _, n := range result.Notes {
			fmt.Printf("  %s\n", n)
		}
	}

	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", w)
	}
}

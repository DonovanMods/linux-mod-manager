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
	deploySource  string
	deployProfile string
	deployMethod  string
	deployPurge   bool
	deployAll     bool
	deployForce   bool
)

var deployCmd = &cobra.Command{
	Use:   "deploy [mod-id]",
	Short: "Deploy mods to game directory",
	Long: `Deploy mod files from cache to game directory.

Use this when changing deployment methods (symlink, hardlink, copy)
or if mod files need to be refreshed.

Without a mod ID, deploys all enabled mods in the current profile.
With a mod ID, deploys only that specific mod.

Use --purge to remove all deployed mods before deploying. This ensures
a clean slate, useful when mods have gotten out of sync.

Use --all to deploy all mods including disabled ones (e.g., after a purge).

Examples:
  lmm deploy --game skyrim-se
  lmm deploy --game skyrim-se --all
  lmm deploy --game skyrim-se --method hardlink
  lmm deploy --game skyrim-se --purge
  lmm deploy 12345 --game skyrim-se
  lmm deploy 12345 --game skyrim-se --method copy`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDeploy,
}

func init() {
	deployCmd.Flags().StringVarP(&deploySource, "source", "s", "nexusmods", "mod source")
	deployCmd.Flags().StringVarP(&deployProfile, "profile", "p", "", "profile (default: active profile)")
	deployCmd.Flags().StringVarP(&deployMethod, "method", "m", "", "link method: symlink, hardlink, or copy (default: game's configured method)")
	deployCmd.Flags().BoolVar(&deployPurge, "purge", false, "purge all deployed mods before deploying")
	deployCmd.Flags().BoolVarP(&deployAll, "all", "a", false, "deploy all mods including disabled ones")
	deployCmd.Flags().BoolVarP(&deployForce, "force", "f", false, "continue even if hooks fail")

	rootCmd.AddCommand(deployCmd)
}

func runDeploy(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doDeploy(ctx, service, game, args)
	})
}

func doDeploy(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	profileName := profileOrDefault(deployProfile)

	var linkMethodOverride *domain.LinkMethod
	if deployMethod != "" {
		var m domain.LinkMethod
		switch deployMethod {
		case "symlink":
			m = domain.LinkSymlink
		case "hardlink":
			m = domain.LinkHardlink
		case "copy":
			m = domain.LinkCopy
		default:
			return fmt.Errorf("invalid link method: %s (use: symlink, hardlink, or copy)", deployMethod)
		}
		linkMethodOverride = &m
	}
	methodName := service.GetGameLinkMethod(game).String()
	if linkMethodOverride != nil {
		methodName = linkMethodOverride.String()
	}

	opts := core.DeployOptions{
		Purge:       deployPurge,
		LinkMethod:  linkMethodOverride,
		All:         deployAll,
		Hooks:       getResolvedHooks(service, game, profileName),
		HookRunner:  getHookRunner(service),
		HookContext: makeHookContext(game),
		Force:       deployForce,
	}
	if len(args) > 0 {
		opts.ModID = args[0]
		opts.SourceID = deploySource
	}

	// deployHeaderPrinted tracks whether we ever reached the deploy loop (as
	// opposed to only a --purge pass, or neither): DeployProfile fires no
	// per-mod progress events at all when there is nothing to deploy, which
	// is exactly the "No mods to deploy" case the pre-extraction CLI checked
	// via len(modsToDeploy) before it had been folded into the flow.
	deployHeaderPrinted := false
	printDeployHeaderOnce := func(total int) {
		if deployHeaderPrinted {
			return
		}
		deployHeaderPrinted = true
		fmt.Printf("Deploying %d mod(s) using %s...\n\n", total, methodName)
	}

	// progress prints every diagnostic and per-mod status line at its exact
	// point of occurrence, driven entirely by core.DeployProfile's progress
	// events - including diagnostics that also land in result.Warnings/
	// .Notes (see core.DeployResult's doc comment). Those slices are never
	// separately batch-printed below: every entry has a corresponding event
	// here, so doing so would double-print. Phases that occur before the
	// deploy loop starts (a forced before_all warning, anything from the
	// --purge pass) return early, without calling printDeployHeaderOnce -
	// they must print before "Deploying N mod(s)..." even exists.
	progress := func(p core.DeployProgress) {
		switch p.Phase {
		case core.DeployBeforeAllForced:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
			return
		case core.DeployPurging:
			fmt.Printf("Purging %d mod(s) before deploy...\n", p.Total)
			return
		case core.PurgeWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
			return
		case core.PurgeNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
			return
		case core.PurgeComplete:
			fmt.Println()
			return
		case core.PurgeModSkipped, core.PurgeModPurged:
			// PurgeProfile-only phases that never fire during a deploy
			// --purge pass; handled so a future change can't accidentally
			// route them into printDeployHeaderOnce below.
			return
		}

		printDeployHeaderOnce(p.Total)
		switch p.Phase {
		case core.DeployBeforeEachSkipped:
			fmt.Printf("  Skipped: %s\n", p.Detail)
		case core.DeployRedownloading:
			fmt.Printf("  %s %s - cache missing, re-downloading...\n", colorYellow("⚠"), p.ModName)
		case core.DeployFallbackUsed:
			fmt.Printf("  %s %s - stored file IDs not found, using primary\n", colorYellow("⚠"), p.ModName)
		case core.DeployDownloading:
			fmt.Printf("\r  ⬇ %s: %.1f%%", p.ModName, p.Percent)
		case core.DeployDownloadDone:
			fmt.Println()
		case core.DeployDownloadFailed:
			fmt.Println()
			fmt.Printf("  %s %s - %s\n", colorRed("✗"), p.ModName, p.Detail)
			fmt.Println()
		case core.DeploySkipped:
			fmt.Printf("  %s %s - %s\n", colorRed("✗"), p.ModName, p.Detail)
		case core.DeployDeployed:
			fmt.Printf("  %s %s\n", colorGreen("✓"), p.ModName)
		case core.DeployNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.DeployWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	result, err := service.DeployProfile(ctx, game, profileName, opts, progress)
	if err != nil {
		// Diagnostics accumulated before a fatal error (DeployProfile's
		// error-path convention returns them alongside it) were already
		// printed above, live, via progress - nothing left to print here.
		return err
	}

	if !deployHeaderPrinted {
		if deployAll {
			fmt.Println("No mods to deploy.")
		} else {
			fmt.Println("No enabled mods to deploy. Use --all to deploy disabled mods.")
		}
		return nil
	}

	fmt.Printf("\nDeployed: %d", result.Deployed)
	if failed := len(result.Skipped); failed > 0 {
		fmt.Printf(", Failed: %d", failed)
	}
	fmt.Println()

	if deployMethod != "" {
		fmt.Printf("\nNote: Used %s method for this deployment.\n", methodName)
		fmt.Printf("To make this permanent, update your games.yaml config.\n")
	}

	return nil
}

// findFilesByIDs finds downloadable files matching the given IDs
func findFilesByIDs(files []domain.DownloadableFile, fileIDs []string) []*domain.DownloadableFile {
	idSet := make(map[string]bool)
	for _, id := range fileIDs {
		idSet[id] = true
	}

	var result []*domain.DownloadableFile
	for i := range files {
		if idSet[files[i].ID] {
			result = append(result, &files[i])
		}
	}
	return result
}

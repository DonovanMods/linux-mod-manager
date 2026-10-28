package main

import (
	"cmp"
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

// modSkipVersion is `lmm mod skip-update --version`.
var modSkipVersion string

var modSkipUpdateCmd = &cobra.Command{
	Use:   "skip-update <mod-id>",
	Short: "Skip a mod's pending update without pinning it",
	Long: `Skip one pending update of an installed mod.

The mod stays unpinned: 'lmm update' leaves the skipped version out of its
table and of bulk updates (--all, and auto-policy mods) until the source
offers a newer one, which is then reported as normal. 'lmm update <mod-id>'
still applies the skipped version, and clears the skip.

With no --version, skips the version the source offers now.

Examples:
  lmm mod skip-update 12345 --game skyrim-se
  lmm mod skip-update 12345 --game skyrim-se --version 1.3.0`,
	Args: cobra.ExactArgs(1),
	RunE: runModSkipUpdate,
}

var modUnskipUpdateCmd = &cobra.Command{
	Use:   "unskip-update <mod-id>",
	Short: "Offer a skipped update again",
	Long: `Clear a mod's skipped update, so 'lmm update' offers it again.

Examples:
  lmm mod unskip-update 12345 --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runModUnskipUpdate,
}

func init() {
	modSkipUpdateCmd.Flags().StringVar(&modSkipVersion, "version", "", "version to skip (default: the version the source offers now)")
	modCmd.AddCommand(modSkipUpdateCmd)
	modCmd.AddCommand(modUnskipUpdateCmd)
}

func runModSkipUpdate(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doModSkipUpdate(ctx, service, game, args[0])
	})
}

func runModUnskipUpdate(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doModUnskipUpdate(ctx, service, game, args[0])
	})
}

// doModSkipUpdate skips modID's pending update (#542) - modSkipVersion, or
// the version its source offers now.
func doModSkipUpdate(ctx context.Context, service *core.Service, game *domain.Game, modID string) error {
	profileName, err := resolveModTarget(ctx, service, game, modID)
	if err != nil {
		return err
	}
	if _, err := service.GetInstalledMod(ctx, modSource, modID, game.ID, profileName); err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	result, err := service.SkipModUpdate(ctx, game, modSource, modID, profileName, modSkipVersion)
	if err != nil {
		return err
	}

	// Ruling 15: the ModSettingResult document.
	if jsonOutput {
		return emitJSON(result)
	}

	fmt.Printf("%s %s: update %s skipped — hidden until a newer version appears ('lmm mod unskip-update %s' to show it again)\n",
		colorGreen("✓"), result.Mod.Name, skippedTarget(service, result.Mod, result.SkippedVersion), modID)
	return nil
}

// doModUnskipUpdate clears modID's skipped update (#542).
func doModUnskipUpdate(ctx context.Context, service *core.Service, game *domain.Game, modID string) error {
	profileName, err := resolveModTarget(ctx, service, game, modID)
	if err != nil {
		return err
	}
	if _, err := service.GetInstalledMod(ctx, modSource, modID, game.ID, profileName); err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	result, err := service.UnskipModUpdate(ctx, modSource, modID, game.ID, profileName)
	if err != nil {
		return err
	}

	// Ruling 15: the ModSettingResult document.
	if jsonOutput {
		return emitJSON(result)
	}

	fmt.Printf("%s %s: skipped update cleared — 'lmm update' offers it again\n", colorGreen("✓"), result.Mod.Name)
	return nil
}

// skippedTarget renders a skipped version for a readout: the version
// itself, or, for a Workshop item whose version is a content id (#428),
// "the newer revision".
func skippedTarget(service *core.Service, mod domain.InstalledMod, version string) string {
	if workshopVersioned(service, &mod.Mod, mod.External) {
		return "to the newer revision"
	}
	return version
}

// printSkippedUpdates names the updates `lmm update` left out because the
// user skipped that version (#542), each with the command that offers it
// again. Silent when there are none.
func printSkippedUpdates(service *core.Service, report *core.UpdateCheckReport) {
	n := len(report.SkippedUpdates)
	if n == 0 {
		return
	}
	fmt.Printf("\n%d skipped update%s (not offered until a newer version appears):\n", n, plural(n))
	for _, u := range report.SkippedUpdates {
		m := u.InstalledMod
		fmt.Printf("  %s %s — lmm mod unskip-update %s -s %s\n", cmp.Or(m.Name, m.ID), skippedTarget(service, m, u.NewVersion), m.ID, m.SourceID)
	}
}

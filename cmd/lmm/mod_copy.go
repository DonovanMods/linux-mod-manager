package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var (
	copyTo      string
	copyAll     bool
	copyReplace bool
	copyDryRun  bool
)

var modCopyCmd = &cobra.Command{
	Use:   "copy [<mod-id>] --to <profile>",
	Short: "Copy a mod (or every mod) from one profile into another",
	Long: `Copy an installed mod from one profile into another profile of the same game.

Nothing is downloaded or re-installed: the mod's files are already in the
cache, and the copy records the mod in the destination profile at the same
version. A lock on the mod is carried over, and a mod marked disabled is
copied disabled.

The source profile is the 'lmm mod' group's -p/--profile (default: the active
profile) and -s/--source names the source the mod comes from. If the
destination is the active profile the mod is deployed to the game directory;
otherwise it is only recorded, and deployed on the next 'lmm profile switch'
or 'lmm deploy'. The destination's own link method is used, so a destination
that differs from the source profile is called out.

If the destination already lists the mod, a single-mod copy refuses; pass
--replace to overwrite it in place (keeping its load-order position).

With --all, every mod of the source profile is copied (no mod ID is given).
Mods already listed in the destination are skipped unless --replace is given.
Steam Workshop items are game-global and are never copied. Use --dry-run to
see what would happen without changing anything.

Examples:
  lmm mod copy abc123 --to experiment
  lmm mod copy abc123 -p default --to experiment --replace
  lmm mod copy --all --to experiment --dry-run`,
	Args: func(cmd *cobra.Command, args []string) error {
		if copyAll {
			if len(args) != 0 {
				return errors.New("--all copies every mod of the profile; do not give a mod ID")
			}
			return nil
		}
		if len(args) != 1 {
			return errors.New("give the ID of the mod to copy, or --all")
		}
		return nil
	},
	RunE: runModCopy,
}

func init() {
	modCopyCmd.Flags().StringVar(&copyTo, "to", "", "destination profile (required)")
	modCopyCmd.Flags().BoolVar(&copyAll, "all", false, "copy every mod of the source profile")
	modCopyCmd.Flags().BoolVar(&copyReplace, "replace", false, "overwrite a mod the destination already lists")
	modCopyCmd.Flags().BoolVar(&copyDryRun, "dry-run", false, "print what the copy would do without changing anything")
	_ = modCopyCmd.MarkFlagRequired("to")

	modCmd.AddCommand(modCopyCmd)
}

func runModCopy(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		id := ""
		if len(args) > 0 {
			id = args[0]
		}
		return doModCopy(ctx, service, game, id)
	})
}

func doModCopy(ctx context.Context, service *core.Service, game *domain.Game, modID string) error {
	from, err := resolveProfile(ctx, service, game.ID, modProfile)
	if err != nil {
		return err
	}

	req := core.CopyModRequest{From: from, To: copyTo, All: copyAll}
	if !copyAll {
		allMods, err := service.GetInstalledMods(ctx, game.ID, from)
		if err != nil {
			return fmt.Errorf("getting installed mods: %w", err)
		}
		var matches []domain.InstalledMod
		for i := range allMods {
			if allMods[i].ID == modID && (modSource == "" || allMods[i].SourceID == modSource) {
				matches = append(matches, allMods[i])
			}
		}
		// The shared ambiguity refusal, as mod edit/uninstall/update give it.
		installed, err := core.ResolveInstalledByID(matches, modID, from, "-s/--source")
		if err != nil {
			return err
		}
		req.SourceID, req.ModID = installed.SourceID, installed.ID
	}

	plan, err := service.PlanCopyMod(ctx, game, req)
	if err != nil {
		return err
	}

	if copyDryRun {
		if jsonOutput {
			return emitJSON(plan)
		}
		renderCopyModPlan(plan)
		return nil
	}

	sink := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.ModCopyCopied:
			fmt.Printf("  ✓ %s\n", p.ModName)
		case core.ModCopySkipped:
			fmt.Printf("  - %s: skipped (%s)\n", p.ModName, p.Detail)
		case core.ModCopyWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	result, err := service.ApplyCopyMod(ctx, game, plan, core.CopyModOptions{Replace: copyReplace}, quietSink(sink))
	if err != nil {
		var listed *core.ModAlreadyListedError
		if errors.As(err, &listed) {
			return fmt.Errorf("%w; use --replace to overwrite it", err)
		}
		return err
	}

	if jsonOutput {
		return emitJSON(result)
	}

	fmt.Printf("Copied %d mod(s) from %s to %s", len(result.Copied), result.From, result.To)
	if n := len(result.Skipped); n > 0 {
		fmt.Printf(", skipped %d", n)
	}
	fmt.Println()
	// Skipped mods the plan already knew of (external, uncached) have no
	// event; the ones Apply skipped were printed above as they happened.
	for _, s := range plan.Skipped {
		fmt.Printf("  - %s: skipped (%s)\n", copySkipName(s), s.Reason)
	}
	return nil
}

func copySkipName(s core.CopyModSkip) string {
	if s.Name != "" {
		return s.Name
	}
	return domain.ModKey(s.SourceID, s.ModID)
}

// renderCopyModPlan prints a core.CopyModPlan under a "(dry run)" header.
func renderCopyModPlan(plan *core.CopyModPlan) {
	fmt.Printf("Copy plan: %s → %s (dry run)\n\n", plan.From, plan.To)
	if plan.NoChanges {
		fmt.Println("Nothing to copy.")
	}
	for _, e := range plan.Mods {
		line := fmt.Sprintf("  + %s v%s", e.Name, e.Version)
		if e.Locked {
			line += " (lock carried)"
		}
		if e.Disabled {
			line += " (disabled)"
		}
		switch {
		case e.AlreadyListed && copyReplace:
			line += fmt.Sprintf(" - replaces the listed v%s", e.ExistingVersion)
		case e.AlreadyListed && plan.All:
			line += fmt.Sprintf(" - already listed (v%s), would be skipped; use --replace to overwrite", e.ExistingVersion)
		case e.AlreadyListed:
			line += fmt.Sprintf(" - already listed (v%s); needs --replace", e.ExistingVersion)
		}
		fmt.Println(line)
	}
	for _, s := range plan.Skipped {
		fmt.Printf("  - %s: skipped (%s)\n", copySkipName(s), s.Reason)
	}
	fmt.Println()
	if plan.DestinationActive {
		fmt.Printf("%s is the active profile: the copied mods would be deployed.\n", plan.To)
	} else {
		fmt.Printf("%s is not the active profile: the mods would be recorded only, and deployed on the next switch or deploy.\n", plan.To)
	}
	if plan.LinkMethodDiffers {
		fmt.Printf("Note: %s links by %s, %s by %s.\n", plan.To, plan.LinkMethod, plan.From, plan.SourceLinkMethod)
	}
}

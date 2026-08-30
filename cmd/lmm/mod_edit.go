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
	editName    string
	editVersion string
	editAuthor  string
	editSource  string
	editID      string
	editProfile string
)

var modEditCmd = &cobra.Command{
	Use:   "edit <current-id>",
	Short: "Edit mod details (name, version, author, source, ID)",
	Long: `Manually edit mod details after import.

Useful for:
- Fixing names/versions on locally imported mods
- Re-linking a local mod to its CurseForge or NexusMods ID
- Adding missing metadata

Providing --source and/or --source-id re-links the mod: whichever of the
two you omit keeps its current value. If the resulting source is
configured for this game and isn't "local", metadata (name, author,
version, summary, URL) is fetched from it automatically and applied to
any field you didn't explicitly override with --name/--author/--version.
Re-linking moves the mod to its new source:id in the database and
profile - it is not merely a display change.

A locked mod (see 'lmm mod lock') refuses --version other than the
locked version itself: move the lock to the desired version, or unlock.
It refuses re-linking outright: unlock is the only remedy, since
re-linking would replace the locked profile entry and moving the lock
does not help. Metadata-only edits (--name/--author) are always allowed.

Examples:
  lmm mod edit abc123 --name "Better Mod Name" --version 1.2.3
  lmm mod edit abc123 --source curseforge --source-id 12345
  lmm mod edit abc123 --author "ModAuthor"`,
	Args: cobra.ExactArgs(1),
	RunE: runModEdit,
}

func init() {
	modEditCmd.Flags().StringVar(&editName, "name", "", "new mod name")
	modEditCmd.Flags().StringVar(&editVersion, "version", "", "new version")
	modEditCmd.Flags().StringVar(&editAuthor, "author", "", "new author")
	modEditCmd.Flags().StringVar(&editSource, "source", "", "new source (e.g. curseforge, nexusmods)")
	modEditCmd.Flags().StringVar(&editID, "source-id", "", "new source-specific mod ID")
	modEditCmd.Flags().StringVarP(&editProfile, "profile", "p", "", "profile (default: active profile)")

	modCmd.AddCommand(modEditCmd)
}

func runModEdit(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doModEdit(ctx, service, game, args[0])
	})
}

func doModEdit(ctx context.Context, service *core.Service, game *domain.Game, currentID string) error {
	profileName, err := resolveProfile(ctx, service, game.ID, editProfile)
	if err != nil {
		return err
	}

	// Find the mod - search all sources
	var installedMod *domain.InstalledMod
	allMods, err := service.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}
	for i := range allMods {
		if allMods[i].ID == currentID {
			installedMod = &allMods[i]
			break
		}
	}
	if installedMod == nil {
		return fmt.Errorf("mod %s not found in profile %s", currentID, profileName)
	}

	plan, err := service.PlanRelinkMod(ctx, game, profileName, installedMod.SourceID, installedMod.ID, editSource, editID)
	if err != nil {
		return err
	}

	// #146: a LOCKED profile ref converges only via explicit lock/unlock -
	// PlanRelinkMod.Refusal is populated whenever a re-link would replace
	// one. Without this gate, the re-link path dropped the Locked marker
	// (RemoveMod deletes the locked ref, then UpsertMod appends a fresh ref
	// with zero-value Locked), and the --version path wrote the DB row
	// first and only then hit UpsertMod's ErrModLocked guard - demoted to a
	// verbose-only warning, i.e. success output plus silent DB-vs-profile
	// divergence. ApplyRelinkMod re-checks both guards itself (a plan is a
	// snapshot); this early return only avoids computing changes/printing
	// anything for a re-link request already known to fail.
	if plan.Refusal != "" {
		return fmt.Errorf("%w: %s", core.ErrModLocked, plan.Refusal)
	}

	opts := core.RelinkOptions{Name: editName, Version: editVersion, Author: editAuthor}

	sink := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.RelinkFetching:
			fmt.Printf("%s\n", p.Detail)
		case core.RelinkProfileNote:
			if verbose {
				fmt.Printf("%s\n", p.Detail)
			}
		case core.RelinkWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	result, err := service.ApplyRelinkMod(ctx, game, plan, opts, quietSink(sink))
	if err != nil {
		return err
	}

	// Ruling 15: the RelinkResult document - NoChanges and Changes below
	// are both fields of it.
	if jsonOutput {
		return emitJSON(result)
	}

	if result.NoChanges {
		fmt.Println("No changes specified. Use --name, --version, --author, --source, or --source-id.")
		return nil
	}

	fmt.Printf("Updated %s:\n", result.Mod.Name)
	for _, change := range result.Changes {
		fmt.Printf("  %s\n", change)
	}

	return nil
}

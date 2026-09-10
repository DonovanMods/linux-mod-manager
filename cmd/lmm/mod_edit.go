package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var (
	editName     string
	editVersion  string
	editAuthor   string
	editToSource string
	editToID     string
	editProfile  string
)

var modEditCmd = &cobra.Command{
	Use:   "edit <current-id>",
	Short: "Edit mod details (name, version, author, source, ID)",
	Long: `Manually edit mod details after import.

Useful for:
- Fixing names/versions on locally imported mods
- Re-linking a local mod to its CurseForge or NexusMods ID
- Adding missing metadata

Providing --to-source and/or --to-source-id re-links the mod: whichever of
the two you omit keeps its current value. (They are named apart from the
'lmm mod' group's own -s/--source, which says which source the mod you are
editing is IN - the two mean opposite ends of the same move.) If the
resulting source is configured for this game and isn't "local", metadata
(name, author,
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
  lmm mod edit abc123 --to-source curseforge --to-source-id 12345
  lmm mod edit abc123 -s local --author "ModAuthor"`,
	Args: cobra.ExactArgs(1),
	RunE: runModEdit,
}

func init() {
	modEditCmd.Flags().StringVar(&editName, "name", "", "new mod name")
	modEditCmd.Flags().StringVar(&editVersion, "version", "", "new version")
	modEditCmd.Flags().StringVar(&editAuthor, "author", "", "new author")
	// NOT "source"/"source-id": a local --source replaces the mod group's
	// persistent -s/--source outright, so `lmm mod edit alpha -s repo`
	// failed with "unknown shorthand flag: 's'" and no flag was left to say
	// WHICH of two same-id mods to edit (#396).
	modEditCmd.Flags().StringVar(&editToSource, "to-source", "", "re-link the mod to this source (e.g. curseforge, nexusmods)")
	modEditCmd.Flags().StringVar(&editToID, "to-source-id", "", "re-link the mod to this source-specific mod ID")
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

	// Find the mod, narrowed by the mod group's -s/--source when it was
	// given. Two sources can hold the same mod id in one profile, and this
	// used to take whichever the scan reached first - silently editing the
	// other one (#396, final review finding 3).
	allMods, err := service.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}
	var matches []*domain.InstalledMod
	for i := range allMods {
		if allMods[i].ID != currentID {
			continue
		}
		if modSource != "" && allMods[i].SourceID != modSource {
			continue
		}
		matches = append(matches, &allMods[i])
	}
	switch len(matches) {
	case 0:
		if modSource != "" {
			return fmt.Errorf("mod %s not found in profile %s for source %s", currentID, profileName, modSource)
		}
		return fmt.Errorf("mod %s not found in profile %s", currentID, profileName)
	case 1:
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.SourceID)
		}
		sort.Strings(ids)
		return fmt.Errorf("%d mods with id %s in profile %s (%s); pass -s/--source to choose one",
			len(matches), currentID, profileName, strings.Join(ids, ", "))
	}
	installedMod := matches[0]

	plan, err := service.PlanRelinkMod(ctx, game, profileName, installedMod.SourceID, installedMod.ID, editToSource, editToID)
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
		fmt.Println("No changes specified. Use --name, --version, --author, --to-source, or --to-source-id.")
		return nil
	}

	fmt.Printf("Updated %s:\n", result.Mod.Name)
	for _, change := range result.Changes {
		fmt.Printf("  %s\n", change)
	}

	return nil
}
